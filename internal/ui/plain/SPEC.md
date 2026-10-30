---
psy_kind: factual
psy_version: 1
package: internal/ui/plain
---

# internal/ui/plain

## 概述

`internal/ui/plain` 包提供一个面向**非 TTY 环境**的 `ui.Sink` 实现，把智能体事件以纯文本（带 ANSI 颜色）写到任意 `io.Writer`。它的输出格式与重构前老 `*ui.Terminal` 在 `--batch` 模式下产生的字节序列逐字节一致，因此 E2E 测试、管道消费、`--batch` 流式读取等非交互场景的输出不受 Bubble Tea 接管 TTY 渲染的影响。

## 文件

- `sink.go` — `Sink` 结构体及其全部 `ui.Sink` 方法实现，外加编译期接口断言
- `sink_test.go` — `Sink` 的单元测试

## API

### 结构体与构造函数

- **`Sink`** (struct) — plain 实现，持有目标 `io.Writer`、流式文本缓冲区（`streamBuf`）与上一次工具调用的成败标记（`lastToolOK`）
- `New(w io.Writer) *Sink` — 创建 plain Sink 实例，`lastToolOK` 初值为 true

### Sink 方法（实现 `ui.Sink`）

- `UserSubmit(text string)` — 空实现：批处理路径无需回显用户输入
- `Thinking(start bool)` — 空实现：非 TTY 不渲染 spinner，保持输出稳定
- `AssistantTextDelta(delta string)` — 把 delta 累积到内部 `streamBuf`
- `AssistantTextEnd()` — 把 `streamBuf` 整体冲刷为一行绿色文本后清空缓冲区；缓冲区为空时是 no-op（这就是"字节稳定"的关键：plain sink 不做增量打印，只在 `End` 时一次性输出完整段落）
- `ToolCallStart(id, display string)` — 输出 `\n⏺ <display>` 头（青色 `⏺`）
- `ToolCallResult(id, body string, ok bool)` — 输出 `⎿ <body>` 块；`ok=false` 时 `⎿` 染红、其余染灰；多行 body 用两空格缩进续接
- `Step(current, total int)` — 输出 `\n--- 步骤 N/M ---`（黄色），保留是为了不改 batch 输出
- `Forbidden(reason string)` — 输出红色 `🚫 操作被安全策略禁止` + 原因
- `Warning(msg string)` — 输出黄色 `⚠️ <msg>`
- `Error(msg string)` — 输出红色 `❌ <msg>`
- `ConfirmBatch(reqs []ui.ConfirmRequest) bool` — 直接返回 `ui.ConfirmAutoApprove`（批处理路径恒为 true，永远不弹窗；与 `tea.Sink` 的批量确认方法同名）
- `AskUser(input json.RawMessage) (string, error)` — 解析 `ask_user` 载荷，校验选项数 ≥ 2，自动选第一个选项的 Label 作为答案并打印一行提示；解析失败或选项不足时返回错误
- `Close()` — 空实现

### 内部常量

- ANSI 颜色转义：`colorReset`/`colorRed`/`colorGreen`/`colorYellow`/`colorCyan`/`colorGray`/`colorBold`

## 依赖

**内部包：** `gengar-pro/internal/ui`（`Sink` 接口、`ConfirmRequest`、`ConfirmAutoApprove`）

**第三方库：** 仅 Go 标准库 `encoding/json`、`fmt`、`io`、`strings`

## 设计重点

### 字节稳定优先

plain sink 的核心约束是**输出必须与重构前的 batch 输出逐字节一致**，这样 E2E 黄金文件不需要随 TUI 重构更新。具体体现在两处：一是流式文本不在 delta 时增量打印，而是在 `AssistantTextEnd` 时整体冲刷（避免 tea 那种边收边渲染的抖动）；二是 `Step` 保留旧格式（`tea.Sink` 已忽略 `Step`，但 plain 仍打印，因为旧 batch 快照里有）。

### 非 TTY 退化

plain sink 假定写入端不是真正的终端：不渲染 spinner（`Thinking` 空）、不回显用户输入（`UserSubmit` 空）、所有交互弹窗退化成自动决策（`ConfirmBatch` 永真、`AskUser` 选首项）。这与 tea sink 的"全交互"形成两极，`main` 根据 `--batch` 与 TTY 探测在两者间二选一。
