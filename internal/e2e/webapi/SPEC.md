---
psy_kind: factual
psy_version: 1
package: internal/e2e/webapi
---

# internal/e2e/webapi

## 概述

webapi 档 E2E：驱动真实 `gengar-server` 二进制子进程，用带 cookie jar 的 HTTP 客户端与 gorilla WebSocket 客户端走 Web 层全链路（登录/限速、主机 CRUD、chat 确认流、断线自动取消与重连续传、多用户隔离），断言 REST/WS 行为与审计链。全部源文件挂 `//go:build e2e`。复用 headless 档（`internal/e2e`）的 `StartStubLLM`/`StartHosts`/`ReadAudit`/`RepoRoot`。

## 文件

- `serverproc.go` — 子进程夹具：`buildServer`（编译缓存）、`startServer`（空闲端口→`--add-user admin` 引导（serve 路径 ensureAdmin 提权）→正式启动（env 注入 `ANTHROPIC_*` + `GENGAR_KNOWN_HOSTS`）→ health 轮询含进程死亡分支）；`lockedBuf`（exec 双拷贝 goroutine 与超时读者同锁串行的输出缓冲）；`stop` 幂等收尸
- `client.go` — `apiClient`（cookie jar REST：`login` 返状态码 / `do` 裸 / `json` 断言 200）+ `wsClient`（`dialChatWS`——cookiejar 对 ws:// 返回空的陷阱用 http 镜像 URL 取 cookie 手工设头；`next`/`waitEvent`（帧 t）/ `waitChatEvent`（chat 帧内 ev.type）预算递减跳读）
- `webapi_test.go` — 用例：TestServerSmoke（登录/登出最小闭环）、W01-W05
- `helpers_test.go` — W 用例共用 helper：`loginOK`/`provisionExecHost`（REST 建真容器主机——server 子进程不能用 SeedHosts 的密码引用）/ 帧解码 / `assertAuditChain`（有序子序列匹配，背景条目穿插不破坏）/ `waitAuditEntries`（异步审计轮询）
- `serverproc_test.go` / `client_test.go` — 无 Docker 单测（假服务器/假进程手法）

## API（测试内）

- `startServer(t, dataDir, stubURL, apiKey) *serverProc` — 每用例独立数据目录 + 独立进程，t.Cleanup 收尸
- `loginOK(t, p, user, pass) *apiClient` — 登录并断言 200（多用户用例各持独立 jar = 服务端独立 token 会话）
- W01-W05 见用例注释

## 依赖

**内部包：** `internal/e2e`（StartStubLLM/StartHosts/ReadAudit/RepoRoot）、`internal/audit`

**第三方库：** `github.com/gorilla/websocket`

**外部系统：** Docker（W02+ 需要；冒烟/W01 不需要）、Go 工具链（现场编译 server）

## 设计重点

### 测试模式（W06/W07 扩展位）

- **多用户引导**：`--add-user` 种子 admin → `POST /api/users` 建普通用户 → 各自 `loginOK` 独立 jar
- **跨用户探测表**：对他用户资源真 id 的 method/path 探测均 404，配「无审计痕」扫描（tool × User 归因）
- **审计读取决策规则**：同步 REST 路径（处理器内落审计先于响应）→ 直接 `ReadAudit`；异步链（WS 生命周期/Agent 任务 goroutine）→ `waitAuditEntries` 轮询（上界 10s）
- **agentSID 关联**：webapi 事件 Session=裸 sid，agent 条目=`<ts>-chat-<sid>`——`strings.HasSuffix(e.Session, "-chat-"+sid)` 跨层关联
- **Risky 命令构造**：纯 `df` 在只读白名单（Safe 不弹窗）——复合命令（如 `df -h; touch ...`）分段判定才 Risky 必弹确认

### WS 帧两层语义

帧类型（t）是 hello|chat|confirm|ask|confirm_done|ask_done|reset；`tool_start/tool_result/done` 等对话事件是 chat 帧内 `ev.type`——两个等待 API 对应两层。重放断言按 seq 严格序（hub 锁内 publish 保证）。

### 已知边界

W06（XShell 导入 + 设置审计）/ W07（终端 WS attach/回放）为框架就绪的扩展位，本期未做；上下文压缩用例不建（桩按请求序号回放，构造压缩条件会 flaky）。
