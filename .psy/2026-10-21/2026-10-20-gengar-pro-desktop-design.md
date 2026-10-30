# gengar-pro 设计文档：从单机 CLI 到多服务器桌面管理工具

- 日期：2026-10-20
- 状态：已评审通过（Alpha 范围已确认）
- 评审方式：brainstorming 会话（浏览器架构图 + 线框图 + 终端逐节确认）

## 1. 背景与目标

gengar 现在是一个基于 LLM 的 Linux 智能运维 Agent：单二进制、登录到每台服务器上运行，在本机执行命令（ReAct 循环 + 10 个运维插件 + 2 级安全确认）。

**gengar-pro 的目标**：进化为一个桌面管理工具——运维工程师在自己的电脑上打开一个应用，通过自然语言自由选择不同服务器，进行数据收集、命令执行和变更操作。不再逐台登录。

### 成功标准

- 新服务器只要有 SSH 就能纳管（零服务器侧部署）
- 一句自然语言可以指定目标（机器名 / 机组 / 标签 / 钉住集合），Agent 解析后在目标机上执行
- 危险操作仍走 2 级安全确认，且明确展示受影响的机器清单
- 首个交付物为 **Alpha 版本**（见第 8 节）

## 2. 决策记录

| # | 决策点 | 结论 | 备选与否决理由 |
|---|--------|------|----------------|
| D1 | 架构模型 | **SSH 直连 · 无 Agent**（Ansible 模式）：LLM Agent、安全门控、记忆全在桌面端，工具调用经 SSH 在目标机执行 | 否决「服务器常驻 daemon」（要管理 agent 本身，工作量数倍）；否决「桌面编排 + 服务器端子 Agent」（双层 LLM 成本高、确认链路复杂） |
| D2 | 桌面形态 | **Wails v2**（Go + 系统 WebView，前端 Web 技术） | 否决「本地 Web 服务 + 浏览器」（非真桌面窗口）；否决 Electron（捆绑 Chromium、与 Go 代码库割裂） |
| D3 | 服务器选择 UX | **混合模型**：侧栏钉住默认目标 + Agent 解析自然语言；工具调用带主机徽标；危险操作展示影响机器清单后一次决议 | 它是「纯 NL 驱动」的超集（不钉住即退化）；否决「每服务器会话标签页」（对话割裂） |
| D4 | v1 纳管范围 | **SSH 密钥 + 密码认证**，GUI 手动录入 | ssh-config 导入、跳板机 → 后续版本 |
| D5 | 旧 CLI 去留 | **废弃**。gengar-pro 是唯一产品形态；插件接口可自由重构，不保本地执行产品路径 | —— |

## 3. 总体架构

```
┌─────────────────── gengar-pro 桌面 App（Wails：前端 WebView + 后端 Go 单进程）───────────────────┐
│  前端（React + TS）                            后端（Go，现有代码库改造）                          │
│  ┌────────────────────┐   bindings   ┌────────────────────────────────────────────────┐        │
│  │ 服务器侧栏（钉住/状态点）│ ──────────→ │ Agent ReAct 循环（多主机系统提示词）              │        │
│  │ 对话流（主机徽标）     │              │   ├ Claude Client（SSE 流式）                  │        │
│  │ 确认弹窗 / ask_user  │ ←────────── │   ├ Safety Gate（2 级，确认请求含 hosts）        │        │
│  │ 服务器管理 / 设置     │   events    │   ├ Plugin Registry（fan-out 分发 + 结果聚合）    │        │
│  └────────────────────┘  (Sink 桥接)  │   └ Host Registry（清单 / 标签机组 / 目标解析）    │        │
│                                       │ 记忆（按主机分域）· 审计 JSONL · 环境共识          │        │
│                                       └────────────────┬───────────────────────────────┘        │
│                                              Executor 接口（RunShell/RunArgv/WriteFile）       │
│                                           ├ SSHExecutor（连接缓存/重连/有界并发）              │
│                                           └ LocalExecutor（测试 / localhost 特殊主机）          │
└──────────────────────────────────────────────────┼─────────────────────────────────────────────┘
                                                    │ SSH（密钥/密码）
                                        ┌───────────┼───────────┐
                                        ▼           ▼           ▼
                                     server-1     server-2    server-N（无需安装任何东西）
```

一次典型数据流：「在所有 web 机器上检查 nginx」→ ① 前端提交 → ② Agent 解析 "web" 机组（Host Registry → web-01, web-02）→ ③ 模型发出一次工具调用 `systemctl_status(service="nginx", hosts=["web"])` → ④ Registry fan-out 并行 SSH 到 2 台 → ⑤ 按主机聚合结果回传模型 → ⑥ UI 工具行显示主机徽标与各自结果。

## 4. 核心抽象

### 4.1 Executor —— 统一执行接缝（最大改造点）

现状：10 个插件里散落 4 种执行方式（`defaultShellRun`、可注入 `runArgv`、`ShellExecutor.RunFunc`、`os.WriteFile`）。统一为一个接口，按目标主机绑定实现：

```go
// internal/executor/executor.go
type Executor interface {
    RunShell(ctx context.Context, cmd string) ([]byte, error)          // bash -c 形式
    RunArgv(ctx context.Context, name string, args ...string) ([]byte, error)  // argv 形式
    WriteFile(ctx context.Context, path string, content []byte) error  // 替代 os.WriteFile
}
```

- **SSHExecutor**：`golang.org/x/crypto/ssh`；连接管理器按主机缓存 `*ssh.Client`（每命令一个 session，多路复用），断线重连、有界并发（默认并发数可配）；`WriteFile` 走 SFTP（`github.com/pkg/sftp`）
- **LocalExecutor**：现有 `exec.Command` 路径收拢，供测试与 `localhost` 特殊主机使用

### 4.2 Host 模型与清单

```go
type Host struct {
    ID, Name, Addr string
    Port int
    User string
    Auth AuthConfig     // 密钥路径 / 密码（引用系统钥匙串，不明文入库）
    Tags []string       // "web"、"prod"… 机组 = 标签
    OS, Role string     // 元数据，喂给系统提示词
}
```

`HostRegistry`：清单 CRUD + **目标解析**——`"web-01"` → 单机；`"web"` → 标签机组；`"pinned"` → UI 当前钉住集合；`"all"` → 全部。解析结果为主机列表 + 无法解析的剩余项（回传模型自行处理）。

### 4.3 fan-out 发生在工具层，不在 LLM 层（关键决策）

模型发出**一次**带目标参数的调用（如 `check_port(port=80, hosts=["web"])`），而非对每台机器各发一次。Registry 解析目标 → 并行执行 → 结果按主机分节聚合成单个 ToolResult 回传模型。Token 效率高、模型心智简单、UI 天然按主机渲染。单机目标是单元素的特例，同一代码路径。

### 4.4 工具分两类

- **目标型**（Shell / File / Docker / Systemd / Package / Diagnostic / Network，共 7 个插件）：入参增加 `hosts` 字段，走 Executor fan-out。网络工具（ping/dns/http_check）在**目标机上**执行——支持「web-01 到 db-01 连通性」这类检查
- **控制面**（ask_user、query_consensus、memory_read/write）：无 fan-out，控制端直接执行；memory 工具入参带 host 域读写该机记忆

### 4.5 Agent 循环与确认流

- 系统提示词重写为「多主机运维指挥台」：注入主机清单目录（名字/标签/角色/钉住状态）、教学 hosts 参数用法（省略时默认钉住集合；无钉住则反问）
- `Sink` 事件（`ToolCallStart/Result` 等）与 `ConfirmRequest` 增加 host 归属字段；Risky fan-out 确认弹窗展示「命令 × 受影响机器清单」，一次决议整批；黑名单高风险仍强制输入 `yes`
- 目标歧义时 Agent 用现有 `ask_user` 反问（如「prod 有 3 台，全查还是只查负载高的？」）

### 4.6 记忆分域与审计

- 记忆：`~/.gengar-pro/memory/<host>.json`，每机保留现有两类（system_info / history），存控制端；新服务器保存时自动采集基础信息写入
- 审计：`~/.gengar-pro/audit.jsonl`，在 Executor 层埋点（时间 / 会话 / 主机 / 命令 / 结果 / 是否经确认）

## 5. 桌面应用

### 5.1 工程结构（Wails v2，单仓库）

```
gengar-pro/
├── frontend/                  # React + TypeScript + Vite + Tailwind
│   └── src/（侧栏 / 对话流 / 弹窗 / 设置页组件）
├── app.go                     # Wails 绑定：提交消息 / 主机 CRUD / 决议确认 / 设置读写
├── cmd/gengar-pro/main.go     # 桌面入口（组装 Wails app）
└── internal/…                 # 现有核心包保留复用
```

### 5.2 Sink → GUI 桥接（关键机制）

`ui.Sink` 接口新增第三个实现 `gui.Sink`（现有 TUI / plain 之外）：

- **单向事件**：`AssistantTextDelta`、`ToolCallStart/Result`、`Step` 等转 Wails `Events.Emit`，前端订阅渲染
- **阻塞调用**（`ConfirmBatch` / `AskUser`）：Go 侧发事件后挂 channel 等待；前端弹窗决议后调绑定方法回填。Agent 循环保持同步语义，无需改造

### 5.3 会话模型

- Alpha：**单会话**，上下文与钉住集合为内存态，重启不恢复（可接受的 alpha 简化）
- 后续版本：多会话列表（每会话独立上下文 + 钉住集合 + 自动命名），持久化
- **全局单任务锁**：同一时刻仅一个任务在执行（延续现状），避免并发 SSH 与确认语义复杂化

### 5.4 本地持久化（`~/.gengar-pro/`）

| 数据 | 存储 |
|---|---|
| 主机清单 / 设置 | SQLite（`gengar.db`，纯 Go 驱动 `modernc.org/sqlite`，无 CGO） |
| SSH 密码 | 系统钥匙串（Windows Credential Manager / macOS Keychain，`zalando/go-keyring`） |
| 每机记忆 | `memory/<host>.json`（沿用现有 JSON 结构） |
| 审计日志 | `audit.jsonl` |

API 配置从环境变量迁到设置页（仍兼容环境变量作默认值）；Profile 共识机制照旧。

### 5.5 交互保留项

斜杠命令体系（含 Skill：`/inspect`、`/install`）在输入框照常触发，执行目标为「钉住集合或 Agent 解析结果」（钉住 3 台跑 `/inspect` 即三机巡检）。Enter 提交 / Esc 取消 / Shift+Enter 换行语义带入 GUI。

## 6. 错误处理

| 场景 | 行为 |
|---|---|
| fan-out 部分失败（常态） | 每台独立执行，单台失败不中断其他；聚合结果中失败主机单独成节（含原因）；全部失败才算工具级错误 |
| 认证失败 | 明确报错（密钥被拒 / 密码错误），有限次重试；UI 引导去设置页修复 |
| 执行超时 | 现有 `CommandTimeout` 应用到每主机执行；SSH 会话随 ctx 取消终止 |
| 任务中断 | Esc → ctx cancel → 进行中 SSH 会话全部终止；重启后可见历史、不恢复中断任务 |
| 目标解析失败 | 工具层报错回传模型 → 模型对照清单或 `ask_user` 反问 |
| Risky 批量部分失败 | 确认后逐台执行、逐台标注；**无跨机事务、不自动回滚**——Agent 可提议补偿操作，由用户决定 |

## 7. 测试策略

- **单元**：插件测试全部注入 `Executor` mock（现有 `runArgv` mock 平移，改动机械）；HostRegistry 目标解析、fan-out 聚合语义（含部分失败）表驱动测试
- **集成**：Docker 起 `sshd` 容器当「远程主机」，SSHExecutor 真连真执行——替代原「容器内本地执行」的 E2E 思路
- **headless 入口**（`--headless`，测试设施非产品功能）：stdin 驱动 Agent 循环 + plain Sink，多机编排逻辑在无 GUI 下可 E2E
- **GUI 层不写 E2E**：渲染与产品逻辑经 Sink/bindings 解耦，headless 已覆盖产品逻辑；前端仅轻量组件测试

## 8. 里程碑（首个交付物 = Alpha 垂直切片）

**Alpha —— 核心体验闭环，一次交付**（原 M1+M2 核心与 M3 精华的垂直合并）：

1. `internal/executor`：接口 + LocalExecutor + SSHExecutor（密钥/密码认证）+ 连接管理（缓存 / 重连 / 有界并发 / SFTP）
2. `internal/host`：Host 模型 + Registry（CRUD / 目标解析）+ SQLite 存储 + 钥匙串集成
3. 10 插件接缝统一到 Executor；目标型工具加 `hosts` 参数；Registry fan-out + 按主机聚合
4. Agent 循环多主机化：系统提示词、Sink 事件 host 字段、ConfirmRequest.Hosts、批量确认
5. 记忆分域 + 审计 JSONL 埋点
6. Wails 工程：主窗口（服务器侧栏 + 钉住 + 状态点、单会话对话流、主机徽标渲染、批量确认弹窗、ask_user 弹窗）、服务器录入表单（含连通性测试）、设置页
7. headless 入口 + sshd 容器集成测试

**Alpha 明确不做**：多会话 UI、机组管理专页（标签只在录入表单里填）、多机巡检报告汇总、审计查看 UI、ssh-config 导入、跳板机、多任务并行。

**Alpha 后**：多会话与持久化 → 巡检报告 → 审计 UI → ssh-config 导入 → 跳板机 → （远期可选）服务器侧 agent 常驻能力叠加。

## 9. 非目标（v1 全局）

- 跨机事务 / 自动回滚
- 多用户 / 团队协作 / 权限体系（单用户桌面工具）
- 非 Linux 目标机支持（Windows Server 等）
- 服务器侧常驻 agent（保留为远期可选演进）
