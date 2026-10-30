# gengar-pro Web 化设计（桌面应用 → 自托管 Web 系统）

日期：2026-10-22
状态：已与用户确认

## 背景与动机

gengar-pro 当前是 Wails 桌面应用（React 前端 + Go 核心）。用户决定改为 web 系统，动机：

1. **团队协作** —— 共享主机清单、凭据、集中审计
2. **免安装/随处访问** —— 浏览器即用

已确认的边界决策：

| 决策点 | 结论 |
|---|---|
| 信任模型 | 内网小团队，简单登录，不做 RBAC |
| 桌面端去留 | **web 完全替换**，Wails 层删除，单一代码路径 |
| 协作深度 | 共享资产 + 审计；对话会话各自独立，不做实时旁观 |
| 设置归属 | **全局共享**（一个 API Key，审计按人区分） |
| 架构形态 | 单二进制自托管服务器（方案 A），保留零外部依赖特征 |

## 总体架构

新增入口 `cmd/gengar-server`（与 `gengar-headless` 并列）：

```
gengar-server --data <dir> --listen :8080
```

- 单 Go 二进制，`go:embed` 内嵌 `frontend/dist`（与原 Wails 嵌入同理），SPA fallback 到 `index.html`
- 新包 `internal/webapi`：HTTP 路由 + WS hub + 登录会话，向下调 `appcore`
- `appcore` 主体复用，新增 `Session` 类型（Core 之上的单用户视图：钉住集合/默认目标/Agent 会话按用户隔离；Core 原有 API 供 headless 使用的部分不变）——多用户下「省略 hosts = 钉住的默认目标」必须按用户解析
- `internal/ui/gui`（Wails 事件桥）的职责由 webapi 的 **WS hub + 待决议注册表** 承接：ChatEvent 推送、ConfirmBatch/AskUser 挂起等待决议回填，事件模型与现 `gui.Sink` 一致，仅传输层从 Wails EventsEmit 换为 WS 帧

数据流：

```
浏览器 ──REST──> webapi ──> appcore ──> executor/SSH ──> 目标主机
      <─WS(事件流)─┘         │
                            └── agent(ReAct) ──> Claude API（Key 全局共享）
```

## 认证与用户

- SQLite `users` 表：`username` / `bcrypt hash` / `created_at`
- 管理命令：`gengar-server --add-user <name> --data <dir>`，交互式读密码（不走网页注册）
- 会话 cookie：HttpOnly、SameSite=Lax；`/api/*` 与 WS 全部要求登录
- 内网 HTTP 直连；TLS 由前置反代负责（文档说明，本项目不做）

## 凭据加密（替代系统钥匙串）

- `host.SecretStore` 接口不变；新增 `EncryptedStore` 实现：
  - 主密钥 32 字节：`$DATA/secret.key` 首次自动生成（0600），或 `GENGAR_MASTER_KEY` 环境变量注入
  - SSH 密码/私钥以 AES-GCM 加密后存 SQLite（secrets 表：host → ciphertext）
- `known_hosts` TOFU 逻辑保留，落服务端数据目录
- 删除 `KeyringStore`（系统钥匙串实现）；`SecretStore` 接口与内存实现保留（测试用）

## API 面（映射原 app.go 的 25 个绑定方法）

REST：

| 端点 | 原方法 |
|---|---|
| `POST /api/login` `POST /api/logout` `GET /api/me` | —（新增） |
| `GET /api/hosts`（含状态） | GetHosts |
| `POST /api/hosts` / `DELETE /api/hosts/:id` | SaveHost / DeleteHost |
| `POST /api/hosts/:id/test` | TestHost |
| `GET /api/settings` `PUT /api/settings` | GetSettings / SaveSettings（全局共享） |
| `GET /api/pinned` `PUT /api/pinned` | GetPinned / SetPinned（**每用户独立**，内存态按用户隔离——与桌面端同为不持久化，服务重启重置） |
| `POST /api/chat` | SubmitMessage |
| `POST /api/chat/cancel` | CancelTask |
| `POST /api/confirm/:id` | ResolveConfirm |
| `POST /api/ask/:id` | ResolveAsk |
| `POST /api/xshell/import` | DetectXshellDirs/ScanXshell/BrowseXshellDir 合并为**上传解析**：请求体带多个 .xsh 文件内容，服务端 `internal/xshell` 解析，返回预览列表 |
| `POST /api/xshell/commit` | ImportXshell（提交预览中选中的主机） |

WS：

- `/api/ws/chat` —— 服务端→客户端 ChatEvent 流（含 confirm/ask 请求事件）；客户端帧：心跳。**重连后服务端重放该用户的事件缓冲**
- `/api/ws/term/:hostID` —— 双向帧：`write` / `resize` 上行，输出 base64 下行。**显式 `close` 帧才关闭 PTY**（任一附加用户可关）；WS 断开本身不关 PTY（他人可能仍附加）

## 会话与多用户语义

- **对话会话**：每用户一个 in-memory agent 会话（与原单人单会话一致）；同一用户多标签页 = 多个 WS 附加到同一会话，事件 fan-out；任务进行中重复提交报错（沿用现状）；设置变更重置会话（保留现行为）
- **事件缓冲**：服务端按用户持有有界缓冲（约 2000 条，FIFO 截断），浏览器刷新/断线重连即重放；服务重启丢会话 = 现 alpha 已知限制，不引入会话持久化（YAGNI）
- **终端**：沿用 `term.go` 按主机单 PTY 模型——两个用户开同一主机 = 附加到同一 PTY（共享终端，符合团队工具语义）；需要的小改：事件流从单消费者改为多 WS fan-out，写入互斥已有
- **审计**：`audit.jsonl` 每条新增 `user` 字段（操作者）；不做审计 UI

## 前端改造

- 新增 `frontend/src/api/`：
  - `client.ts` —— fetch 封装（含登录态 401 跳转）
  - `events.ts` —— WS 订阅、重连退避、重放对齐（收到 `hello` 帧带缓冲序号，跳过已渲染部分）
- 5 个组件（App / TerminalPane / HostForm / Settings / ImportDialog）的 `wailsjs` 调用替换为 api client；`App.tsx` 事件订阅从 Wails EventsOn 换 WS
- 新增登录页 + 路由守卫
- XShell 导入 UI：从「扫本机目录」三步流程改为「文件多选上传 → 预览 → 提交」
- 纯函数层（`timing.ts` / `form.ts` / `folders.ts`）与 vitest 测试**零改动**

## 删除清单

- `main.go`（Wails 入口）、`app.go`、`wails.json`、`build/`（Wails 打包资源）
- `frontend/wailsjs/`
- `internal/ui/gui/`
- `internal/host` 的 `KeyringStore`（保留接口与内存实现）
- `internal/ui/tea/`（旧单机 CLI 的 Bubble Tea TUI，确认无引用后删）
- README：桌面段落重写为服务端部署/使用指南，构建命令从 `wails build` 改为 `go build ./cmd/gengar-server` + `cd frontend && npm run build`

## 测试策略

- **headless E2E 套件零改动**（驱动 appcore 产品逻辑，web 化不影响有效性）；Docker sshd 集成测试复用
- `internal/webapi` httptest 单测：登录/会话、主机 CRUD、确认与提问决议回填、WS 事件流与重连重放、终端 fan-out、EncryptedStore 往返
- 前端：现有 vitest 不动；`api/client` 加 mock 测试（登录跳转、错误映射）
- 更新 README 人工验收清单（登录、双用户并发对话、刷新恢复、共享终端）

## 实施顺序

1. webapi 骨架：embed 静态 + auth（users 表/登录/cookie）+ 主机 CRUD + 设置 + pin
2. 对话：`/api/chat` + WS 事件流 + 每用户事件缓冲与重放 + 确认/提问决议
3. 终端：`/api/ws/term` + term.go 多消费者 fan-out
4. XShell 上传导入 + 前端传输层收尾 + **删除 Wails 层**（main.go / app.go / wailsjs / ui/gui / KeyringStore / tea）
5. README 重写 + 验收清单 + 打磨

## 风险与对策

| 风险 | 对策 |
|---|---|
| 凭据从 OS 钥匙串变为服务端加密落盘，安全性依赖部署机权限 | 主密钥支持 env 注入；文档强调 data 目录权限；keyfile 0600 |
| 多用户并发触发同一主机的 Risky 批量确认 | 确认请求按用户会话隔离（各自会话各自的待决议），审计记录决议人 |
| WS 重放缓冲内存膨胀 | 有界 FIFO（2000 条/用户），超限截断头部 |
| 浏览器侧无 XShell 目录访问 | 导入改为文件上传语义，反而任何成员可用 |
