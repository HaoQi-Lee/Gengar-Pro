---
psy_kind: factual
psy_version: 1
package: internal/plugin
---

# internal/plugin

## 概述

`internal/plugin` 是 Gengar 项目的**工具插件体系**核心包。它定义统一的 `Plugin` 接口（`Execute` 收到注入的单目标 `executor.Executor`）并实现十一大功能域插件：Shell 命令执行、文件操作、Docker 管理、systemd 服务管理、系统诊断、软件包管理、网络诊断、Redis 中间件运维、环境共识查询、按主机的持久化记忆以及用户意图澄清。`Registry` 注册中心聚合全部插件，对目标型工具（`Targeting=true`，输入带 `hosts` 参数）经 `TargetResolver` 解析引用并按主机 fan-out、聚合为单一结果；控制面工具直发。每个工具声明自身的风险等级，供上层安全审批机制使用。

## 文件

- `types.go` — 核心类型 `ToolDefinition`（含 `Targeting` 标记）、`ToolResult`（含 `Hosts`）和 `Plugin` 接口（Execute 注入 executor.Executor）
- `types_test.go` — 类型序列化与输出格式测试
- `registry.go` — 插件注册中心 `Registry`：注册、路由执行、目标型工具的 fan-out 与聚合、风险等级与目标型查询、`Target`/`TargetResolver` 定义
- `registry_test.go` — 注册、执行分发、fan-out 聚合/截断/panic 降级、未知工具报错和风险等级查询测试
- `fakes_test.go` — 测试用 `fakeExec`（记录调用、可注入错误的假执行器）
- `shell.go` / `shell_test.go` — ShellExecutor 插件，提供 `execute_command` 工具
- `file.go` / `file_test.go` — FileManager 插件，提供 `read_file`、`write_file`、`list_directory`、`find_files`、`grep_files` 工具
- `docker.go` / `docker_test.go` — DockerManager 插件，提供 `docker_ps`、`docker_logs`、`docker_compose_up/down`、`docker_exec` 工具
- `systemd.go` / `systemd_test.go` — SystemdManager 插件，提供 systemctl 和 journalctl 工具
- `diagnostic.go` / `diagnostic_test.go` — DiagnosticKit 插件，提供端口/进程/磁盘/内存/网络/日志诊断工具以及 `system_info`
- `network.go` / `network_test.go` — NetworkPlugin 插件，提供 `ping`、`dns_lookup`、`http_check` 工具
- `redis.go` / `redis_test.go` — RedisPlugin 插件，提供 `redis_discover`（实例发现）、`redis_cli`（命令透传，Risky）、`redis_rdb`（RDB 离线分析）工具
- `pkgmgr.go` / `pkgmgr_test.go` — PackageManager 插件，提供包安装/卸载/搜索/列表工具，自动检测 apt/yum/dnf
- `knowledge.go` / `knowledge_test.go` — KnowledgeBase 插件，提供 `query_consensus` 工具（控制面）
- `memory.go` / `memory_test.go` — MemoryManager 插件，提供按主机分域的 `memory_read/write/delete` 工具（控制面，输入带 `host` 参数而非 `hosts`）
- `clarify.go` / `clarify_test.go` — ClarifyPlugin 插件，提供 `ask_user` 工具（由 Agent 循环直接处理，控制面）

## API

### 核心接口与类型

- **`Plugin`** (interface) — 插件接口：`Tools()` + `Execute(ctx, toolName, input, exec)`；exec 是绑定单台目标机的执行器，控制面工具忽略（传 nil 不触发执行）
- **`ToolDefinition`** — 工具定义（名称、描述、InputSchema、RiskLevel、Targeting）。`Targeting` 标记目标型工具：输入带 `hosts` 参数，由 Registry 解析并按主机 fan-out；控制面工具为 false
- **`ToolResult`** — 工具执行结果（Content、IsError、Hosts）。`Hosts` 记录本次调用实际生效的目标主机名（fan-out 时由 Registry 填写），供 Agent 层审计与 UI 归属展示
- **`Target`** — 解析后的执行目标：主机名 + 绑定该主机的 `executor.Executor`
- **`TargetResolver`** (interface) — 由组装层（appcore）实现：`Names(refs)`（免连接投影）与 `Resolve(refs)`（绑定执行器）；refs 为空表示默认目标（UI 钉住集合）

### Registry

- `NewRegistry() *Registry` — 创建注册中心
- `Registry.Register(p Plugin)` — 注册插件
- `Registry.SetResolver(res TargetResolver)` — 注入目标解析器（目标型工具的 fan-out 依赖它）
- `Registry.SetGate(g *safety.Gate)` — 把全部工具声明的 RiskLevel 灌进 gate 静态表（分级单一真相源，评审 ①-4）；须在 Register 之后调用
- `Registry.Tools() []ToolDefinition` — 返回全部工具定义
- `Registry.Execute(ctx, toolName, input) (ToolResult, error)` — 路由执行：控制面工具直发（nil exec）；目标型工具解析 hosts → 有界并行逐主机执行 → 聚合为单一结果
- `Registry.IsTargeting(toolName) bool` — 报告工具是否目标型
- `Registry.TargetNames(toolName, input) []string` — 展开输入里的目标引用为主机名，供确认弹窗展示影响范围；尽力而为，失败返回 nil，绝不阻塞确认流程
- `Registry.RiskLevel(toolName) safety.Level` — 查询风险等级，未知工具默认返回最高风险等级

### 各插件构造函数

- `NewShellExecutor()` — Shell 命令执行器
- `NewFileManager()` — 文件管理器
- `NewDockerManager()` — Docker 管理器
- `NewSystemdManager()` — systemd 管理器
- `NewDiagnosticKit()` — 诊断工具集
- `NewPackageManager()` — 包管理器（自动检测 apt/yum/dnf）
- `NewNetworkPlugin()` — 网络诊断插件
- `NewRedisPlugin()` — Redis 中间件运维插件（discover/cli/rdb 三工具）
- `NewKnowledgeBase(consensus)` — 共识知识库
- `NewMemoryManager(store *memory.Store)` — 持久化内存管理器
- `NewClarifyPlugin()` — 用户澄清插件

## 依赖

**内部包：** `gengar-pro/internal/executor`（执行接缝）、`gengar-pro/internal/safety`（风险等级）、`gengar-pro/internal/consensus`（共识查询）、`gengar-pro/internal/memory`（按主机持久化存储）、`gengar-pro/internal/rdbparser`（RDB 离线解析与报告，redis_rdb 工具消费）

**标准库：** `encoding/json`、`fmt`、`context`、`strings`、`sync`、`unicode/utf8` 等

**第三方库：** 无（仅通过内部包间接依赖）

## 设计重点

### 统一插件接口

所有功能域通过 `Plugin` 接口抽象，仅 `Tools()` 和 `Execute()` 两个方法。新增能力只需实现这两个方法即可注册，实现开放-封闭原则。超时与本地/远程的差别全部下沉到 Executor 实现，插件本身无状态、构造零参数。

### 注册中心模式与 fan-out

`Registry` 在 `Register` 时建立工具名到插件实例的映射表，`Execute` 时按工具名路由。目标型工具由 resolver 解析 hosts 引用（refs 为空 = 默认目标）后，以 `defaultMaxParallel = 8` 的有界并发逐主机执行，单主机 panic / 失败降级为该主机的 `[FAILED]` 分节（不杀死桌面进程）。聚合语义：单目标原样透传（与旧行为逐字节一致）；多目标 `===== host =====` 分节，每主机内容超预算时头偏置截断（总预算 10000 字符、每主机下限 500，截断点回退到 rune 边界），**全部失败**才算工具级 IsError；未解析的引用附在末尾回传模型。hosts 字段类型错误显式拒绝，不静默改道默认目标。

### TargetResolver 契约（Names ≡ Resolve）

`Names` 与 `Resolve` 对相同 refs、同一清单快照必须产出相同的主机名集合（Names 是 Resolve 的免连接投影）——确认弹窗显示的主机必须恒等于实际执行的主机，这是 `ConfirmRequest.Hosts` 特性的承重不变量。

### 风险分级机制（单一真相源）

`ToolDefinition.RiskLevel` 使用 `json:"-"` 不序列化到发送给 LLM 的 schema 中，仅用于内部安全审批。`Registry.RiskLevel()` 供上层执行前查询，未知工具默认返回最高风险等级。组装时 `Registry.SetGate(gate)` 把全部声明灌进 `safety` 静态表——`agent.Classify` 与 `Registry.RiskLevel` 读的是同一份声明，不再有第二份手工表（评审 ①-4：双表曾漂移，find/grep/ping/dns_lookup/http_check/query_consensus 六个只读工具误弹确认）。

### 执行形式（RunArgv vs RunShell）

模型可控字段（容器名、服务名、包名、查询串、路径）一律 argv 形式执行——参数不经 shell 重新解释，`nginx; reboot` 只会得到 cli 参数错误（FileManager/DiagnosticKit/NetworkPlugin 原生示范，DockerManager/SystemdManager/PackageManager 已于 2026-10 评审 ①-4 对齐）。`docker_exec` 的 command 保留 shell 语义（本就 Risky=1 需确认），container 名仍 argv 化。compose 用 `-f path` 免 `cd`。`lines` 负数 clamp 到默认。 PackageManager 的探测（cat /etc/os-release、command -v）命令固定无注入面，仍走 RunShell。

### Redis 插件的三工具面

`redis_discover`/`redis_rdb` 为 Safe（只读探测/离线分析），`redis_cli` 为 Risky——静态分级意味着每次调用（含只读 GET）都过确认门，同一轮多条调用共享 Agent 循环的一次批量确认；工具描述如实说明该行为。密码经 `-a` 参数传递且会出现在对话与审计中（本期明确接受的设计决策，见 `.psy/2026-10-31/` 设计文档），不加 `--no-auth-warning`（redis-cli 3.x/5.x 不识别该 flag）。`redis_rdb` 走 `executor.FetchFile` 把 dump.rdb 拉到本地临时文件（`defer os.Remove` 清理），路径自动探测用 `CONFIG GET dir+dbfilename`（`path.Join`，远端 Linux 语义），>1GB 需 `force:true`。

### 澄清插件的特殊处理

`ClarifyPlugin.Execute()` 始终返回错误，因为 `ask_user` 工具由 Agent 循环在更上层拦截并直接处理。该工具的存在仅让 LLM 能在工具列表中"看到"这个能力。
