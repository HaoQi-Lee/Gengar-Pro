---
psy_kind: factual
psy_version: 1
package: internal/agent
---

# internal/agent

## 概述

`internal/agent` 包是 Gengar 项目的核心"智能体"层，负责驱动一个面向多主机 Linux 运维场景的 AI 助手的完整对话循环。它接收用户输入，将其注入对话历史，调用 Claude API（**流式**，逐 token 消费 SSE 事件），解析返回的文本与工具调用（tool_use），执行已注册的插件工具（fan-out 已下沉到 Registry，Agent 看到的是聚合结果），在执行前通过安全门控（safety gate）对高危操作进行审批拦截，并将工具结果回注对话上下文，循环往复直到模型发出 `end_turn` 信号或达到最大步数上限。同时，该包还管理多主机系统提示词的动态重建（主机清单目录）、操作审计流水（audit）、对话上下文的 token 估算与压缩判定，以及结构化的事件日志记录。

## 文件

- `agent.go` — Agent 核心结构体定义、构造函数、多主机系统提示词（含记忆指南）、主循环 `ProcessInput`（消费 `MessagesStream` 的 `<-chan StreamEvent`，逐 delta 推送给 UI；每步 LLM 前上下文压缩检查；取消路径补写 tool_result 防会话报废）、批量确认、审计落盘，以及 `History`/`Reset` 方法
- `agent_test.go` — Agent 的单元测试，覆盖纯文本响应、工具调用链、最大步数限制、Consensus 注入、审计事件、日志事件捕获、ask_user 拦截、取消路径 tool_result 补写等场景
- `conversation.go` — `Conversation` 结构体，封装对话历史的增删改查（添加用户消息、助手消息、工具结果、清空）与系统提示词替换
- `conversation_test.go` — Conversation 的单元测试，覆盖构造、各类消息添加、完整对话流程和清空操作
- `token.go` — Token 估算（`EstimateTokens` ASCII/CJK 区分；`EstimateConversationTokens` 正确认 []claude.ContentBlock 消息）、压缩判定 `ShouldCompress` 与压缩执行 `CompressConversation`（旧 tool_result 占位替换，保留最近 6 条完整）
- `token_test.go` — Token 估算、ContentBlock 计入回归（旧实现漏算恒 false）、压缩幂等/保结构/边界测试

## API

### 类型与结构体

- **`Agent`** — 智能体主结构体，持有配置、Claude 客户端、插件注册表、安全门控、**`ui.Sink`**（不再是 `*Terminal`）、对话管理器、操作历史、动态主机清单函数（catalog）、审计日志与会话标识
- **`ClaudeClient`** (interface) — 对 Claude API 的抽象接口，定义两个方法：
  - `SendMessage(ctx, system, messages, tools) (*APIResponse, error)` — 非流式调用（保留兼容）
  - `MessagesStream(ctx, system, messages, tools) (<-chan claude.StreamEvent, func(), error)` — 流式调用，返回事件 channel 与 cancel 函数
- **`Option`** (func type) — Agent 的函数选项类型，用于构造时注入可选依赖
- **`Conversation`** — 对话上下文管理器，封装系统提示词和消息列表

### 构造函数

- `New(cfg, client, registry, gate, sink ui.Sink, consensus, opts...)` — 创建并初始化 Agent 实例；第五个参数从老的 `*Terminal` 改为 `ui.Sink`，因此同一份 agent 代码可同时驱动 plain（headless）与 webapi userSink（Web 事件流）两种渲染
- `NewConversation(systemPrompt)` — 创建新的对话上下文实例

### Agent 方法

- `ProcessInput(ctx, input) string` — 核心主循环：接收用户输入，每次输入开始时以 basePrompt + catalog() 重建系统提示词，对每一步调用 `MessagesStream` 打开流式通道，逐事件消费（content_block_start/delta/stop、message_delta、message_stop、error），把文本 delta 实时推给 `ui.AssistantTextDelta`，把工具调用累积后批量确认、按序执行并落审计，直到 `end_turn` 或步数上限。返回最后一段助手文本；`stop_reason=max_tokens`（输出被上限掐断）不静默——`ui.Warning` 留痕提示用户发「继续」补全
- `History()` — 返回当前会话中已执行的工具调用历史记录
- `Reset()` — 清空对话上下文和操作历史

### Conversation 方法

- `SystemPrompt()` — 返回当前系统提示词
- `SetSystemPrompt(p)` — 替换系统提示词（主机清单可能随会话中途增删而变化）
- `Messages()` — 返回对话消息列表
- `AddUserMessage(text)` — 追加一条用户消息
- `AddAssistantMessage(blocks)` — 追加一条助手消息（含文本和工具调用块；空回合记为 "(no content)"，API 拒绝 content:null）
- `AddToolResult(toolUseID, content)` — 追加一条工具执行结果消息
- `Clear()` — 清空所有对话消息（保留系统提示词）

### Token 管理

- `EstimateTokens(text)` — 估算文本的 token 数量（ASCII 4 字符/token，CJK 1.5 字符/token）
- `EstimateConversationTokens(conv)` — 估算整段对话（系统提示词 + 全部消息）——消息内容认 string 与 []claude.ContentBlock 两形态（tool_use 按输入 JSON 长度、tool_result 按内容文本；未知形态兜底 +10 宁可高估）
- `ShouldCompress(conv, tokenLimit)` — 判断对话上下文是否已超过 token 限额的 80% 阈值
- `CompressConversation(conv) int` — 把最旧 tool_result 内容替换为 `[tool result elided: N chars, first line: …]` 占位（保留最近 6 条完整；user/assistant 文本与工具配对结构不动——API 消息校验硬约束；幂等可重复应用）。返回省略的字符数

### 选项函数

- `WithLogger(l)` — 注入结构化事件日志器
- `WithCatalog(fn func() string)` — 注入动态主机清单文本（每次 ProcessInput 重新取值）
- `WithAudit(l AuditLogger)` — 注入审计日志（接口化：webapi 传按用户盖 `Entry.User` 戳的包装器，`*audit.Logger` 天然满足）；nil 表示不审计，typed-nil 归一化见 `AuditLogger` 契约
- `WithSession(id)` — 标记审计流里的会话标识

### 审计接口

- **`AuditLogger`** (interface) — 审计落盘的最小接口，仅 `Log(entry audit.Entry) error`。契约（禁用审计须传字面 nil）：`WithAudit` 的归一化只识别 `(*audit.Logger)(nil)` 这一种 typed-nil；包装器实现不得传自身类型的 nil 指针（会以非 nil 接口落入 Agent，调用时 panic），内层 logger 为 nil 时不得包装——调用方自行改传字面 nil

## 依赖

**内部包：** `internal/audit`、`internal/claude`、`internal/config`、`internal/consensus`、`internal/observability`、`internal/plugin`、`internal/safety`、`internal/ui`

**第三方库：** `gopkg.in/yaml.v3`（间接依赖）；标准库 `context`、`encoding/json`、`fmt`、`strings`、`time`。重构后不再直接依赖 `chzyer/readline`、`briandowns/spinner`、`mattn/go-isatty`（历史 TUI 实现已随桌面层退役）

## 设计重点

### ReAct 循环架构

`ProcessInput` 实现经典的 Reasoning-Action 循环——每一步把对话历史发送给模型，解析响应中的文本块与工具调用块，执行工具后将结果回注对话，直到 `end_turn` 或步数耗尽。

### 流式消费 + Esc 中断保留部分文本

重构后的主循环走 `MessagesStream` 拿到 `<-chan StreamEvent`，每收到一个 text delta 就同时累积进本地 `textBuf` 与推给 `ui.AssistantTextDelta`，收齐一个文本块后调 `AssistantTextEnd` 并把整段追加进 `assistantContent`。当 `ctx.Done()`（Esc 触发）时：调一次 `AssistantTextEnd` 收尾当前打开的文本块，把已累积的 `textBuf` 作为部分文本追加进 `assistantContent`，再 `conv.AddAssistantMessage(assistantContent)` 把这段部分回复写进历史——**取消不丢字**。随后为已收集未执行的 tool_use 补写 `tool_result("cancelled by user")`（取消中毒修复：无配对 tool_result 的消息序列会被下一轮 API 请求的消息校验拒绝，特定时点取消会永久报废会话）。然后打印 `[cancelled by user]` 警告并返回。

### 上下文压缩（长会话续命）

每步调 LLM 前检查 `ShouldCompress`（0.8 × 200000 token 阈值）：超限时 `CompressConversation` 把最旧 tool_result 替换为占位符（保留最近 6 条完整）。压缩发生时 `ui.Info` 提示 + `context_compressed` 事件留痕。占位替换确定性（无模型侧摘要、零额外 API 调用）；模型可按占位符明示重跑被省略的命令。

### 依赖倒置与可测试性

`ClaudeClient` 被抽象为接口（含流式方法），所有外部交互通过注入的依赖完成，测试中使用 mock 客户端和内存日志器进行完全隔离的单元测试。UI 侧通过 `ui.Sink` 接口解耦，agent 不知道渲染后端是 plain（headless）还是 webapi 的 userSink（Web 事件流）。

### 分层安全控制

工具执行前经由 `safety.Gate` 进行风险等级分类。每个 step 内所有 `risky` 级别操作先**收集成一批**，经 `ui.Sink.ConfirmBatch([]ui.ConfirmRequest{...})` **一次性**确认（一次 API 响应只弹一个窗；返回 true 全部执行 / false 全部取消），再按原顺序执行——risky 项复用这条批量决议，而非逐条弹窗。高风险命令在 `ConfirmRequest` 上标 `HighRisk=true`（批次含任一高风险则整批走"必须输入 yes"严格模式）；目标型工具的 `ConfirmRequest.Hosts` 经 `Registry.TargetNames` 展开引用，确认弹窗显示将影响的真实主机清单。`ask_user` 工具不参与批量，绕过安全门控走专用 `ui.Sink.AskUser` 路径，仍按原顺序交互。

### 系统提示词模块化组装

`New` 时拼好静态 basePrompt——多主机运维助手指令（含目标选择规则：主机名 / 标签 / all / 省略 hosts = 默认目标）、环境共识目录（Consensus Catalog）、服务器记忆使用指南（`memoryGuide()`）。每次 `ProcessInput` 开始时以 basePrompt + `catalog()`（动态主机清单目录，appcore 注入）重建系统提示词——清单可能随会话中途增删而变化；catalog 附加在尾部是为将来加 prompt cache 断点保留正确形态。

### 操作审计

注入 `WithAudit`/`WithSession` 后，五处落 `audit.Entry`：用户拒绝（safety_block）、分发失败、工具执行完成、ask_user 两路径（回答/提问失败——澄清不走安全门，Approved 恒 true，IsError 区分成败；Hosts 恒 nil，澄清面向用户而非主机）。条目含会话、工具、原始输入、实际生效主机、是否批准、结果。分发失败也落审计——能走到执行说明确认已通过，批准过的操作零记录不可接受。审计写失败降级为日志事件，不中断对话。

### 双层输出截断

单主机 / 控制面结果在 agent 层走 `claude.TruncateOutput`（4000/1500 head/tail）；多主机聚合结果不再过 agent 层截断——Registry 的 per-host 预算已约束总量，再截会砍掉中间主机的分节头（`[FAILED]` 标记随之静默消失）。

### 结构化可观测性

通过 `WithLogger` 选项注入日志器，在用户输入、步骤开始/结束、API 调用/错误、工具调用/结果、安全拦截、run 结束等关键节点发出结构化事件。
