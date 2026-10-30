---
psy_kind: factual
psy_version: 1
package: internal/ui
---

# internal/ui

## 概述

`internal/ui` 包定义了 Gengar UI 层的**渲染无关抽象**。它只声明 `Sink` 接口（智能体用来驱动 UI 的事件表面，而非渲染调用）、`ConfirmRequest` 结构体（确认弹窗所需数据，含干净命令摘要 `Summary` 与受影响主机清单 `Hosts`）以及 `ConfirmAutoApprove` 全局开关（批处理/CI 模式下短路所有交互）。`Sink` 上的 `ConfirmBatch` 把一次 API 响应里的多条 risky 命令一次性提交确认（全部执行 / 全部取消）。当前两个实现：`internal/ui/plain`（非 TTY 字节稳定输出，headless 用）与 `internal/webapi` 的 `userSink`（按用户 WS 事件流）。本包还保留若干纯函数：`FormatToolDisplay`（工具名→友好显示名）、`FormatConfirmSummary`（工具调用→一行干净摘要，供弹窗展示）、`TruncateMiddle`（保留首尾的中间省略号截断）、`NewLineWriter`（行缓冲写入适配器）。

历史上本包曾直接持有 `Terminal`/`Spinner`/`slashHint` 等终端实现，TUI 重构后这些都已删除，仅留下接口与共享的纯函数。

## 文件

- `sink.go` — `Sink` 接口（含 `ConfirmBatch`）、`ConfirmRequest` 结构体、`ConfirmAutoApprove` 全局变量
- `spinner.go` — 仅保留 `FormatToolDisplay` 工具函数（原 `Spinner` 结构体已随 TUI 重构移除）
- `truncate.go` — `TruncateMiddle`（保留首尾、中间省略号截断）与 `FormatConfirmSummary`（工具调用→一行干净摘要）纯函数
- `writer.go` — `NewLineWriter` 行缓冲写入适配器（让 fmt.Fprintln 式输出按行流经 Sink）
- `sink_test.go` — `ConfirmRequest` 标记字段的单元测试
- `spinner_test.go` — `FormatToolDisplay` 的单元测试
- `truncate_test.go` — `TruncateMiddle`、`FormatConfirmSummary` 的单元测试
- `writer_test.go` — `NewLineWriter` 的单元测试

## API

### 类型

- **`Sink`** (interface) — 智能体驱动 UI 的事件表面。每个方法描述对话中的一个事件（用户提交、思考中、助手文本增量、工具调用开始/结果、步骤、禁止/警告/错误、批量确认、结构化提问），而非一次渲染调用。两个实现：`plain.Sink`（非 TTY）、webapi 的 `userSink`（WS 事件流）
- **`ConfirmBatch(reqs []ConfirmRequest) bool`** (interface method) — 把一次 API 响应里所有需确认的 risky 命令一次性提交确认；返回单个决议（true=全部执行 / false=全部取消）。各 Sink 在 `ConfirmAutoApprove=true` 时短路返回 true
- **`ConfirmRequest`** (struct) — 确认弹窗数据载体。`ToolName` 为工具名、`Summary` 为弹窗展示的一行干净摘要（命令/路径，替代旧的原始 JSON `Input`）；`HighRisk` 为真时该条命令标记为高风险（批次中任一高风险则整批走"必须输入 yes"严格模式）；`Hosts` 为该调用将影响的主机名（目标引用已展开，尽力而为、可能为 nil），GUI 据此渲染「命令 × 机器」清单，文本 Sink 可忽略
- **`ConfirmAutoApprove`** (var `bool`) — 全局开关，`gengar-headless` 在 `--yes` 模式下设为 `true`；各 Sink 实现用它短路 `ConfirmBatch`/`AskUser`，跳过交互弹窗

### 函数

- `FormatToolDisplay(name string, input json.RawMessage) string` — 把 API 层工具名映射为 Claude-Code 风格显示名（如 `execute_command` → `Bash(ls -la)`、`read_file` → `Read(/etc/hosts)`、`write_file` → `Write(...)`、`edit_file` → `Edit(...)`），未知工具回退为 `<name>(<截断的输入>)`
- `FormatConfirmSummary(name string, input json.RawMessage) string` — 产出工具调用的一行干净摘要，供确认弹窗展示：`execute_command` → 裸命令、`read_file`/`write_file`/`edit_file` → 裸路径、其余回退到 `FormatToolDisplay` 的 `Name(keyarg)` 形式
- `TruncateMiddle(s string, max int) string` — 把字符串截断到最多 max 个 rune：不超长原样返回；超长保留首尾、中间用单个 `…` 连接（按 rune 计数，命令基本 ASCII，够用）
- `NewLineWriter(emit func(line string)) io.Writer` — 行缓冲写入适配器：按完整行（到 `\n` 为止）逐行回调 emit，让斜杠命令的 fmt.Fprintln 式输出流经 `Sink.Info` 而非裸写 stdout

## 依赖

**内部包：** 无（纯接口与函数定义；实现方为 `internal/ui/plain` 子包与 `internal/webapi` 的 userSink，工具名常量由调用方传入）

**第三方库：** 仅 Go 标准库 `encoding/json`、`fmt`

## 设计重点

### Sink 是事件表面而非渲染表面

`Sink` 接口的每个方法都描述**对话中的一个事件**（"用户提交了一行"、"助手流式产出一个 delta"、"工具调用开始"、"工具调用结束"），而不是一次渲染调用（不是 "PrintBanner"、"PrintAssistant"）。这让同一个智能体循环可以驱动完全不同的渲染后端：plain sink 把事件直接落到 stdout（保持 E2E 字节稳定），webapi 的 userSink 把事件包装成帧推入按用户的 hub、经 WS 送达浏览器（带缓冲重放）。

### 自动确认短路

`ConfirmAutoApprove` 是包级全局变量，意图是批处理/CI 场景下统一关闭交互。各 Sink 实现需在 `ConfirmBatch`/`AskUser` 入口先检查它：`plain.Sink` 直接返回该变量（批处理路径恒为 true），webapi 的 `userSink` 返回 true 并在 `AskUser` 里自动选择第一个选项（服务进程恒不置位，供未来自动化路径复用）。把开关定义在接口所在的根包，是为了让各实现方无需相互引用即可共享语义。

### 批量确认 ConfirmBatch

`ConfirmBatch(reqs []ConfirmRequest) bool` 把**一次 API 响应**里所有需要确认的 risky 命令合并成一次确认（true=全部执行 / false=全部取消），而非逐条弹窗。`ConfirmRequest.Summary` 携带每条命令的干净摘要供弹窗展示并按宽度 `TruncateMiddle` 截断；`HighRisk` 标记高危项（批次中任一为高风险则整批走"必须输入 yes"严格模式）；`Hosts` 携带目标引用展开后的真实主机清单（Agent 经 `Registry.TargetNames` 填充，尽力而为、可能为 nil）。这是 `agent` 侧两段式流程（先收集、单次确认、再按序执行）在接口上的体现。
