package ui

import "encoding/json"

// ConfirmAutoApprove is set by gengar-headless's -yes flag; Sinks use it to
// bypass interactive Confirm/AskUser modals.
var ConfirmAutoApprove bool

// ConfirmRequest carries the data the UI needs to render a risk-confirmation
// modal. HighRisk drives the stricter "must type yes" affordance.
type ConfirmRequest struct {
	ToolName string
	Summary  string // 干净的一行摘要（替代原始 JSON 的 Input）
	HighRisk bool
	// Hosts 是该调用将影响的主机名（目标引用已展开）。GUI 据此渲染
	// 「命令 × 机器」清单；文本 Sink 可忽略。尽力而为、可能为 nil
	// （非目标型 / 无 resolver / 解析失败）。
	Hosts []string
}

// Sink is the surface that agent.Agent uses to drive the UI. Two
// implementations exist: plain.Sink (non-TTY stdout, byte-stable) and
// webapi's userSink (per-user WS event stream). The interface is
// intentionally render-agnostic — each method describes an event in the
// conversation, not a rendering call.
type Sink interface {
	// UserSubmit is called when the user submits a line of input. Plain
	// implementations may print it back; interactive ones echo it as a
	// user line.
	UserSubmit(text string)

	// Thinking toggles the "waiting on first model output" indicator.
	Thinking(start bool)

	// AssistantTextDelta accepts a streaming text chunk. Implementations may
	// buffer until AssistantTextEnd (plain sink does this to keep batch
	// output stable byte-for-byte).
	AssistantTextDelta(delta string)
	AssistantTextEnd()

	// ToolCallStart renders the "⏺ <display>" header and starts a tool
	// spinner where applicable.
	ToolCallStart(id, display string)

	// ToolCallResult renders the "⎿ <body>" block and stops the tool spinner.
	// ok=false marks the upcoming block as a failure (red ⎿, never folded).
	ToolCallResult(id, body string, ok bool)

	// Step is a legacy hook for "--- 步骤 N/M ---". webapi's userSink
	// forwards it; plain.Sink keeps it to avoid changing batch output.
	Step(current, total int)

	// Forbidden / Warning / Error are surfaced as system rows.
	Forbidden(reason string)
	Warning(msg string)
	Error(msg string)

	// Info surfaces a neutral, unstyled system row (e.g. slash-command output
	// such as /help). Unlike Warning/Error it carries no icon and preserves the
	// caller's formatting verbatim, so column-aligned text stays aligned.
	Info(text string)

	// ConfirmBatch blocks until the user accepts or rejects a batch of risky
	// tool calls from a single API response. Returns one decision for the
	// whole batch (true = execute all, false = cancel all).
	ConfirmBatch(reqs []ConfirmRequest) bool

	// AskUser blocks until the user chooses a structured answer for the
	// ask_user tool.
	AskUser(input json.RawMessage) (string, error)

	// Close releases any UI-owned resources.
	Close()
}
