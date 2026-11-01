package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/claude"
	"gengar-pro/internal/config"
	"gengar-pro/internal/consensus"
	"gengar-pro/internal/executor"
	"gengar-pro/internal/observability"
	"gengar-pro/internal/plugin"
	"gengar-pro/internal/safety"
	"gengar-pro/internal/ui"
	"gengar-pro/internal/ui/plain"
)

func init() { ui.ConfirmAutoApprove = true }

// stubExecPlugin 提供与 execute_command 同名同风险等级（Risky，触发确认流）
// 的假工具，忽略注入的 executor 直接返回固定输出。Agent 层测试只关心
// 循环/确认/审计行为；真实执行已由 plugin 包自己的测试覆盖。
type stubExecPlugin struct{ output string }

func (s *stubExecPlugin) Tools() []plugin.ToolDefinition {
	return []plugin.ToolDefinition{
		{
			Name:        "execute_command",
			Description: "stub execute_command for agent loop tests",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
	}
}

func (s *stubExecPlugin) Execute(_ context.Context, _ string, _ json.RawMessage, _ executor.Executor) (plugin.ToolResult, error) {
	return plugin.ToolResult{Content: s.output}, nil
}

type mockClaudeClient struct {
	responses []claude.APIResponse
	callIndex int
}

func (m *mockClaudeClient) SendMessage(ctx context.Context, system string, messages []claude.Message, tools []claude.Tool) (*claude.APIResponse, error) {
	if m.callIndex >= len(m.responses) {
		return &claude.APIResponse{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "Done."}}}, nil
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return &resp, nil
}

// MessagesStream mirrors SendMessage's multi-turn semantics: each call
// consumes one response (by callIndex) and replays it as the equivalent
// stream event sequence the real SSE parser would emit.
func (m *mockClaudeClient) MessagesStream(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	var resp claude.APIResponse
	if m.callIndex >= len(m.responses) {
		resp = claude.APIResponse{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "Done."}}}
	} else {
		resp = m.responses[m.callIndex]
		m.callIndex++
	}
	ch := make(chan claude.StreamEvent, 32)
	go func() {
		defer close(ch)
		ch <- claude.StreamEvent{Type: claude.EventMessageStart}
		for i, b := range resp.Content {
			switch b.Type {
			case "text":
				ch <- claude.StreamEvent{Type: claude.EventContentBlockStart, Index: i, Block: claude.BlockText}
				ch <- claude.StreamEvent{Type: claude.EventContentBlockDelta, Index: i, Block: claude.BlockText, Text: b.Text}
				ch <- claude.StreamEvent{Type: claude.EventContentBlockStop, Index: i, Block: claude.BlockText}
			case "tool_use":
				ch <- claude.StreamEvent{Type: claude.EventContentBlockStart, Index: i, Block: claude.BlockToolUse, ToolID: b.ID, ToolName: b.Name}
				ch <- claude.StreamEvent{Type: claude.EventContentBlockStop, Index: i, Block: claude.BlockToolUse, ToolID: b.ID, ToolName: b.Name, ToolInput: b.Input}
			}
		}
		ch <- claude.StreamEvent{Type: claude.EventMessageDelta, StopReason: resp.StopReason}
		ch <- claude.StreamEvent{Type: claude.EventMessageStop}
	}()
	return ch, func() {}, nil
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestMemoryGuidePerHost: 记忆已按主机分域（memory 工具必带 host 参数，
// 落盘 ~/.gengar-pro/memory/<host>.json）。提示词指南必须与之一致，
// 否则模型按指南发起的调用会因缺 host 而全部报错。
func TestMemoryGuidePerHost(t *testing.T) {
	g := memoryGuide()
	if !contains(g, "~/.gengar-pro/memory/<host>.json") {
		t.Errorf("guide must point at per-host memory path, got:\n%s", g)
	}
	if !contains(g, "memory_read(host=") {
		t.Errorf("guide examples must pass host to memory tools, got:\n%s", g)
	}
}

func TestAgent_SimpleTextResponse(t *testing.T) {
	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "Nginx is running."}}},
	}}
	var buf bytes.Buffer
	term := plain.New(&buf)
	registry := plugin.NewRegistry()
	gate := safety.NewGate()
	cfg := config.Default()

	a := New(cfg, mockClient, registry, gate, term, nil)
	result := a.ProcessInput(context.Background(), "check nginx")
	if result != "Nginx is running." {
		t.Errorf("ProcessInput result = %q, want 'Nginx is running.'", result)
	}
}

func TestAgent_ToolUseThenText(t *testing.T) {
	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "● nginx.service - active (running)"})

	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "text", Text: "Let me check."},
			{Type: "tool_use", ID: "toolu_001", Name: "execute_command", Input: json.RawMessage(`{"command":"systemctl status nginx"}`)},
		}},
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "Nginx is active and running."}}},
	}}

	var buf bytes.Buffer
	term := plain.New(&buf)
	gate := safety.NewGate()
	cfg := config.Default()
	a := New(cfg, mockClient, registry, gate, term, nil)
	result := a.ProcessInput(context.Background(), "check nginx")
	if result != "Nginx is active and running." {
		t.Errorf("ProcessInput result = %q, want 'Nginx is active and running.'", result)
	}
}

func TestAgent_MaxSteps(t *testing.T) {
	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "toolu_001", Name: "execute_command", Input: json.RawMessage(`{"command":"echo test"}`)},
		}},
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "toolu_002", Name: "execute_command", Input: json.RawMessage(`{"command":"echo test2"}`)},
		}},
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "tool_use", ID: "toolu_003", Name: "execute_command", Input: json.RawMessage(`{"command":"echo test3"}`)},
		}},
	}}

	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "output"})

	var buf bytes.Buffer
	term := plain.New(&buf)
	gate := safety.NewGate()
	cfg := config.Default()
	cfg.MaxSteps = 2

	a := New(cfg, mockClient, registry, gate, term, nil)
	_ = a.ProcessInput(context.Background(), "loop test")
	output := buf.String()
	if !contains(output, "步骤上限") && !contains(output, "max steps") {
		t.Errorf("should warn about max steps, got: %s", output)
	}
}

func TestAgent_New_NoConsensus(t *testing.T) {
	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "OK"}}},
	}}
	var buf bytes.Buffer
	term := plain.New(&buf)
	registry := plugin.NewRegistry()
	gate := safety.NewGate()
	cfg := config.Default()

	a := New(cfg, mockClient, registry, gate, term, nil)
	// When consensus is nil, [REDACTED] should be the default without catalog
	if !contains(a.conv.SystemPrompt(), "多主机 Linux 运维指挥台") {
		t.Errorf("Expected default [REDACTED] when consensus is nil")
	}
	if contains(a.conv.SystemPrompt(), "环境共识目录") {
		t.Errorf("Should not contain catalog when consensus is nil")
	}
}

func TestAgent_New_WithConsensus(t *testing.T) {
	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "OK"}}},
	}}
	var buf bytes.Buffer
	term := plain.New(&buf)
	registry := plugin.NewRegistry()
	gate := safety.NewGate()
	cfg := config.Default()

	c := &consensus.Consensus{
		Profile: "dev",
		Guidelines: []consensus.Guideline{
			{ID: "yum-repo", Title: "YUM 源配置", Summary: "dev yum 源", Content: "YUM 源地址: http://dev-mirror.local/centos/7/"},
		},
	}

	a := New(cfg, mockClient, registry, gate, term, c)

	prompt := a.conv.SystemPrompt()
	if !contains(prompt, "环境共识目录 (profile: dev)") {
		t.Errorf("Expected system prompt to contain catalog header, got: %s", prompt)
	}
	if !contains(prompt, "yum-repo: dev yum 源") {
		t.Errorf("Expected system prompt to contain guideline entry, got: %s", prompt)
	}
}

func TestWithLoggerOption(t *testing.T) {
	memLog := observability.NewMemoryLogger()
	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "done"}}},
	}}
	var buf bytes.Buffer
	term := plain.New(&buf)
	registry := plugin.NewRegistry()
	gate := safety.NewGate()
	cfg := config.Default()

	a := New(cfg, mockClient, registry, gate, term, nil,
		WithLogger(memLog),
	)

	a.ProcessInput(context.Background(), "hello")

	events := memLog.Events()
	hasUserInput := false
	hasStepStart := false
	hasResponse := false
	hasRunEnd := false
	for _, ev := range events {
		switch ev.Type {
		case "user_input":
			hasUserInput = true
		case "step_start":
			hasStepStart = true
		case "response":
			hasResponse = true
		case "run_end":
			hasRunEnd = true
		}
	}

	if !hasUserInput {
		t.Error("expected user_input event")
	}
	if !hasStepStart {
		t.Error("expected step_start event")
	}
	if !hasResponse {
		t.Error("expected response event")
	}
	if !hasRunEnd {
		t.Error("expected run_end event")
	}
}

func TestLoggerCapturesToolCall(t *testing.T) {
	memLog := observability.NewMemoryLogger()
	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "hi"})

	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{
			Content: []claude.ContentBlock{
				{Type: "tool_use", ID: "t1", Name: "execute_command", Input: json.RawMessage(`{"command":"echo hi"}`)},
			},
			StopReason: "tool_use",
		},
		{
			Content:    []claude.ContentBlock{{Type: "text", Text: "all done"}},
			StopReason: "end_turn",
		},
	}}
	var buf bytes.Buffer
	term := plain.New(&buf)
	gate := safety.NewGate()
	cfg := config.Default()

	a := New(cfg, mockClient, registry, gate, term, nil,
		WithLogger(memLog),
	)

	a.ProcessInput(context.Background(), "run echo")

	events := memLog.Events()
	hasToolCall := false
	hasToolResult := false
	for _, ev := range events {
		if ev.Type == "tool_call" && ev.ToolName == "execute_command" && ev.ToolID == "t1" {
			hasToolCall = true
		}
		if ev.Type == "tool_result" && ev.ToolID == "t1" {
			hasToolResult = true
		}
	}
	if !hasToolCall {
		t.Error("expected tool_call event for execute_command")
	}
	if !hasToolResult {
		t.Error("expected tool_result event for t1")
	}
}

func TestLoggerCapturesAPIError(t *testing.T) {
	memLog := observability.NewMemoryLogger()
	var buf bytes.Buffer
	term := plain.New(&buf)
	registry := plugin.NewRegistry()
	gate := safety.NewGate()
	cfg := config.Default()

	a := New(cfg, &errorClient{err: fmt.Errorf("API down")}, registry, gate, term, nil,
		WithLogger(memLog),
	)

	a.ProcessInput(context.Background(), "test")

	events := memLog.Events()
	hasAPIError := false
	for _, ev := range events {
		if ev.Type == "api_error" && strings.Contains(ev.Error, "API down") {
			hasAPIError = true
		}
	}
	if !hasAPIError {
		t.Error("expected api_error event")
	}
}

type errorClient struct {
	err error
}

func (e *errorClient) SendMessage(ctx context.Context, system string, messages []claude.Message, tools []claude.Tool) (*claude.APIResponse, error) {
	return nil, e.err
}

func (e *errorClient) MessagesStream(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	return nil, func() {}, e.err
}

func TestAgent_AskUserIntercept(t *testing.T) {
	registry := plugin.NewRegistry()
	registry.Register(plugin.NewClarifyPlugin())

	mockClient := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "tool_use", Content: []claude.ContentBlock{
			{Type: "text", Text: "需要澄清一下。"},
			{Type: "tool_use", ID: "toolu_clarify_001", Name: "ask_user", Input: json.RawMessage(`{"question":"你说的清理日志是指哪种操作？","header":"日志清理","options":[{"label":"压缩归档","description":"压缩"},{"label":"删除旧日志","description":"删除"}]}`)},
		}},
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "好的，我来压缩归档。"}}},
	}}

	var buf bytes.Buffer
	term := plain.New(&buf)
	gate := safety.NewGate()
	cfg := config.Default()
	memLog := observability.NewMemoryLogger()
	a := New(cfg, mockClient, registry, gate, term, nil, WithLogger(memLog))

	ui.ConfirmAutoApprove = true
	defer func() { ui.ConfirmAutoApprove = false }()

	result := a.ProcessInput(context.Background(), "清理日志")
	if result != "好的，我来压缩归档。" {
		t.Errorf("ProcessInput result = %q, want final text", result)
	}

	// Verify the intercept path was taken: tool_result for toolu_clarify_001
	// should contain the auto-selected answer (first option), NOT from registry.Execute.
	var foundClarifyResult bool
	for _, ev := range memLog.Events() {
		if ev.Type == "tool_result" && ev.ToolID == "toolu_clarify_001" {
			foundClarifyResult = true
			if ev.Output != "压缩归档" {
				t.Errorf("ask_user tool_result output = %q, want %q (first option auto-selected)", ev.Output, "压缩归档")
			}
		}
	}
	if !foundClarifyResult {
		t.Error("expected tool_result event for toolu_clarify_001")
	}
}

func TestAgent_AskUserInToolList(t *testing.T) {
	registry := plugin.NewRegistry()
	registry.Register(plugin.NewClarifyPlugin())
	registry.Register(&stubExecPlugin{output: "ok"})

	tools := registry.Tools()
	found := false
	for _, td := range tools {
		if td.Name == "ask_user" {
			found = true
			break
		}
	}
	if !found {
		t.Error("ask_user should be in registry tools")
	}
}

// fakeSink is the smallest ui.Sink that records calls so we can assert the
// agent drives the right events. P0 contract test — passes after the type
// migration to ui.Sink with no behavioural changes.
type fakeSink struct {
	userSubmit        []string
	thinkingOn        int
	thinkingOff       int
	textDeltas        []string
	textEnds          int
	toolStarts        []string
	toolResults       []toolResultCapture
	steps             [][2]int
	forbidden         []string
	warnings          []string
	errors            []string
	infos             []string
	confirmBatchReqs  [][]ui.ConfirmRequest // 每次 ConfirmBatch 调用的入参
	confirmBatchReply bool
	askReply          string
	askErr            error
}

type toolResultCapture struct {
	ID, Body string
	OK       bool
}

func (f *fakeSink) UserSubmit(text string) { f.userSubmit = append(f.userSubmit, text) }
func (f *fakeSink) Thinking(start bool) {
	if start {
		f.thinkingOn++
	} else {
		f.thinkingOff++
	}
}
func (f *fakeSink) AssistantTextDelta(delta string) {
	f.textDeltas = append(f.textDeltas, delta)
}
func (f *fakeSink) AssistantTextEnd() { f.textEnds++ }
func (f *fakeSink) ToolCallStart(id, display string) {
	_ = id
	f.toolStarts = append(f.toolStarts, display)
}
func (f *fakeSink) ToolCallResult(id, body string, ok bool) {
	f.toolResults = append(f.toolResults, toolResultCapture{id, body, ok})
}
func (f *fakeSink) Step(c, t int)           { f.steps = append(f.steps, [2]int{c, t}) }
func (f *fakeSink) Forbidden(reason string) { f.forbidden = append(f.forbidden, reason) }
func (f *fakeSink) Warning(msg string)      { f.warnings = append(f.warnings, msg) }
func (f *fakeSink) Error(msg string)        { f.errors = append(f.errors, msg) }
func (f *fakeSink) Info(text string)        { f.infos = append(f.infos, text) }
func (f *fakeSink) ConfirmBatch(reqs []ui.ConfirmRequest) bool {
	f.confirmBatchReqs = append(f.confirmBatchReqs, reqs)
	return f.confirmBatchReply
}
func (f *fakeSink) AskUser(_ json.RawMessage) (string, error) { return f.askReply, f.askErr }
func (f *fakeSink) Close()                                    {}

func TestAgentDrivesSinkOnEndTurn(t *testing.T) {
	sink := &fakeSink{}
	client := &mockClaudeClient{responses: []claude.APIResponse{
		{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "ok"}}},
	}}
	a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil)
	a.ProcessInput(context.Background(), "hi")

	if len(sink.textDeltas) != 1 || sink.textDeltas[0] != "ok" {
		t.Fatalf("expected one text delta 'ok', got %v", sink.textDeltas)
	}
	if sink.textEnds != 1 {
		t.Fatalf("expected one AssistantTextEnd, got %d", sink.textEnds)
	}
	if sink.thinkingOn != 1 || sink.thinkingOff != 1 {
		t.Fatalf("expected one Thinking on/off pair, got %d/%d", sink.thinkingOn, sink.thinkingOff)
	}
	if len(sink.steps) != 1 || sink.steps[0] != [2]int{1, 1} {
		t.Fatalf("expected one Step(1,1), got %v", sink.steps)
	}
}

// fakeStreamingClient drives the agent purely through MessagesStream events.
type fakeStreamingClient struct {
	events  []claude.StreamEvent
	sendErr error
}

func (f *fakeStreamingClient) SendMessage(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (*claude.APIResponse, error) {
	return nil, errors.New("not used")
}

func (f *fakeStreamingClient) MessagesStream(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	if f.sendErr != nil {
		return nil, func() {}, f.sendErr
	}
	ch := make(chan claude.StreamEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch, func() {}, nil
}

func TestAgentStreamingTextDeltas(t *testing.T) {
	sink := &fakeSink{}
	client := &fakeStreamingClient{events: []claude.StreamEvent{
		{Type: claude.EventMessageStart},
		{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockText},
		{Type: claude.EventContentBlockDelta, Index: 0, Block: claude.BlockText, Text: "hel"},
		{Type: claude.EventContentBlockDelta, Index: 0, Block: claude.BlockText, Text: "lo"},
		{Type: claude.EventContentBlockStop, Index: 0, Block: claude.BlockText},
		{Type: claude.EventMessageDelta, StopReason: "end_turn"},
		{Type: claude.EventMessageStop},
	}}
	a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil)
	a.ProcessInput(context.Background(), "hi")

	if got := strings.Join(sink.textDeltas, ""); got != "hello" {
		t.Fatalf("delta concat want 'hello', got %q", got)
	}
	if sink.textEnds != 1 {
		t.Fatalf("expected one AssistantTextEnd, got %d", sink.textEnds)
	}
}

// max_tokens 停机不允许静默：报告类长输出被掐断时必须在 UI 里留痕，
// 否则用户只看到「内容莫名缺尾」（曾以 4096 上限复现）。
func TestAgentWarnsOnMaxTokensStop(t *testing.T) {
	sink := &fakeSink{}
	client := &fakeStreamingClient{events: []claude.StreamEvent{
		{Type: claude.EventMessageStart},
		{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockText},
		{Type: claude.EventContentBlockDelta, Index: 0, Block: claude.BlockText, Text: "报告前半"},
		{Type: claude.EventContentBlockStop, Index: 0, Block: claude.BlockText},
		{Type: claude.EventMessageDelta, StopReason: "max_tokens"},
		{Type: claude.EventMessageStop},
	}}
	a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil)
	a.ProcessInput(context.Background(), "hi")

	if len(sink.warnings) != 1 || !strings.Contains(sink.warnings[0], "截断") {
		t.Fatalf("expected one truncation warning, got %v", sink.warnings)
	}
}

// slowStreamClient emits one text delta then blocks until ctx is cancelled,
// simulating a long stream the user interrupts with Esc.
type slowStreamClient struct {
	first string
}

func (s *slowStreamClient) SendMessage(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (*claude.APIResponse, error) {
	return nil, errors.New("unused")
}

func (s *slowStreamClient) MessagesStream(ctx context.Context, _ string, _ []claude.Message, _ []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	ch := make(chan claude.StreamEvent, 4)
	go func() {
		defer close(ch)
		ch <- claude.StreamEvent{Type: claude.EventMessageStart}
		ch <- claude.StreamEvent{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockText}
		ch <- claude.StreamEvent{Type: claude.EventContentBlockDelta, Index: 0, Block: claude.BlockText, Text: s.first}
		<-ctx.Done()
	}()
	return ch, func() {}, nil
}

func TestAgentCancelStreamPreservesPartial(t *testing.T) {
	sink := &fakeSink{}
	client := &slowStreamClient{first: "partial"}
	a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	a.ProcessInput(ctx, "hi")

	if got := strings.Join(sink.textDeltas, ""); !strings.Contains(got, "partial") {
		t.Fatalf("partial text lost on cancel: %q", got)
	}
	found := false
	for _, msg := range sink.warnings {
		if strings.Contains(msg, "cancelled by user") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected '[cancelled by user]' warning, got %v", sink.warnings)
	}
}

// scriptedThinkingClient 按「先 thinking 块 → checkpoint → 再 text 块」的脚本
// 发流。events 为无缓冲 channel：第 N+1 个事件 send 返回即证明第 N 个事件已被
// agent 处理完毕，checkpoint 时刻因此可确定性观察 spinner 状态。
type scriptedThinkingClient struct {
	events     chan claude.StreamEvent
	checkpoint chan struct{} // producer → test：thinking 块已被 agent 处理
	release    chan struct{} // test → producer：检查完毕，继续发 text 块
}

func (c *scriptedThinkingClient) SendMessage(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (*claude.APIResponse, error) {
	return nil, errors.New("not used")
}

func (c *scriptedThinkingClient) MessagesStream(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	return c.events, func() {}, nil
}

// TestAgentInvisibleBlockKeepsSpinner: GLM 的 thinking 块在 stream.go 中归为
// BlockUnknown，其 delta 不渲染。spinner 必须保持到可见内容（text/tool_use）
// 出现，否则长推理期间界面无任何反馈，观感如同卡死。
func TestAgentInvisibleBlockKeepsSpinner(t *testing.T) {
	sink := &fakeSink{}
	client := &scriptedThinkingClient{
		events:     make(chan claude.StreamEvent),
		checkpoint: make(chan struct{}),
		release:    make(chan struct{}),
	}
	a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.ProcessInput(context.Background(), "hi")
	}()

	go func() {
		defer close(client.events)
		client.events <- claude.StreamEvent{Type: claude.EventMessageStart}
		client.events <- claude.StreamEvent{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockUnknown}
		client.events <- claude.StreamEvent{Type: claude.EventContentBlockDelta, Index: 0, Block: claude.BlockUnknown, Text: "模型内部推理"}
		// 该 send 返回 ⇒ 上面的 content_block_start(thinking) 必已处理完毕
		client.events <- claude.StreamEvent{Type: claude.EventContentBlockStop, Index: 0, Block: claude.BlockUnknown}
		close(client.checkpoint)
		<-client.release
		client.events <- claude.StreamEvent{Type: claude.EventContentBlockStart, Index: 1, Block: claude.BlockText}
		client.events <- claude.StreamEvent{Type: claude.EventContentBlockDelta, Index: 1, Block: claude.BlockText, Text: "ok"}
		client.events <- claude.StreamEvent{Type: claude.EventContentBlockStop, Index: 1, Block: claude.BlockText}
		client.events <- claude.StreamEvent{Type: claude.EventMessageDelta, StopReason: "end_turn"}
		client.events <- claude.StreamEvent{Type: claude.EventMessageStop}
	}()

	<-client.checkpoint
	if sink.thinkingOff != 0 {
		close(client.release)
		t.Fatalf("spinner 在 thinking(不可见)块期间被撤下 (thinkingOff=%d)，长推理时界面零反馈", sink.thinkingOff)
	}
	close(client.release)
	<-done

	if sink.thinkingOff != 1 {
		t.Fatalf("流结束后 spinner 应恰好关闭一次, got %d", sink.thinkingOff)
	}
}

// silentStreamClient 发出 message_start 后保持 channel 打开（不 close），
// 模拟 TTFB 期间用户按 Esc：agent 只能走 ctx.Done 分支，无 select 竞争。
type silentStreamClient struct{}

func (s *silentStreamClient) SendMessage(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (*claude.APIResponse, error) {
	return nil, errors.New("not used")
}

func (s *silentStreamClient) MessagesStream(ctx context.Context, _ string, _ []claude.Message, _ []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	ch := make(chan claude.StreamEvent, 4)
	go func() {
		ch <- claude.StreamEvent{Type: claude.EventMessageStart}
		<-ctx.Done()
		// 故意不 close：保证 agent 的 select 只能命中 ctx.Done
	}()
	return ch, func() {}, nil
}

// toolUseThenBlockClient 发一个 tool_use 块后阻塞（模拟模型请求了工具
// 但流尚未结束时用户按 Esc）——取消时 tool_use 已进 assistantContent
// 但没有 tool_result，不补写会让下轮 API 请求被消息校验拒绝（②-3）。
type toolUseThenBlockClient struct{}

func (s *toolUseThenBlockClient) SendMessage(_ context.Context, _ string, _ []claude.Message, _ []claude.Tool) (*claude.APIResponse, error) {
	return nil, errors.New("not used")
}

func (s *toolUseThenBlockClient) MessagesStream(ctx context.Context, _ string, _ []claude.Message, _ []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	ch := make(chan claude.StreamEvent, 8)
	go func() {
		defer close(ch)
		ch <- claude.StreamEvent{Type: claude.EventMessageStart}
		ch <- claude.StreamEvent{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockToolUse, ToolID: "t1", ToolName: "check_disk"}
		ch <- claude.StreamEvent{Type: claude.EventContentBlockStop, Index: 0, Block: claude.BlockToolUse, ToolID: "t1", ToolName: "check_disk", ToolInput: json.RawMessage(`{"path":"/"}`)}
		<-ctx.Done()
	}()
	return ch, func() {}, nil
}

// TestAgentCancelBeforeFirstBlockStopsSpinner: 首个内容块到达前取消，
// spinner 必须停止，否则会永远转动。
func TestAgentCancelBeforeFirstBlockStopsSpinner(t *testing.T) {
	sink := &fakeSink{}
	client := &silentStreamClient{}
	a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	a.ProcessInput(ctx, "hi")

	if sink.thinkingOff != 1 {
		t.Fatalf("Esc 取消后 spinner 未停止 (thinkingOff=%d)，会永远转动", sink.thinkingOff)
	}
	found := false
	for _, msg := range sink.warnings {
		if strings.Contains(msg, "cancelled by user") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected '[cancelled by user]' warning, got %v", sink.warnings)
	}
}

// TestAgentCancelWritesToolResultForPendingTools: 评审 ②-3 取消中毒——
// 已收集未执行的 tool_use 必须补写 tool_result("cancelled by user")，
// 保持 tool_use ↔ tool_result 配对，会话可继续。
func TestAgentCancelWritesToolResultForPendingTools(t *testing.T) {
	sink := &fakeSink{}
	client := &toolUseThenBlockClient{}
	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "ok"})
	a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	a.ProcessInput(ctx, "hi")

	// 会话末尾必须是 tool_result 消息且配对 t1
	msgs := a.conv.Messages()
	last := msgs[len(msgs)-1]
	blocks, ok := last.Content.([]claude.ContentBlock)
	if !ok || len(blocks) == 0 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "t1" {
		t.Fatalf("last message must be tool_result for t1, got %+v", last)
	}
	if s, _ := blocks[0].Content.(string); !strings.Contains(s, "cancelled by user") {
		t.Fatalf("tool_result content = %q; want 'cancelled by user'", s)
	}
	// 会话继续可用：不因取消而报废（配对完整是 API 校验的硬约束）
	if a.conv == nil {
		t.Fatal("conversation destroyed")
	}
}

// TestAgentBatchConfirmsOncePerStep: 一次流式响应里的 2 条 risky 命令，只触发
// 一次 ConfirmBatch（入参含两条），而不是逐条确认。拒绝时两条都被记为 rejected。
func TestAgentBatchConfirmsOncePerStep(t *testing.T) {
	// 需要一个能执行 execute_command 的 registry（批准后才会调用 Execute）。
	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "ok\n"})

	// 两条 risky execute_command，同属一个流式响应。
	client := &fakeStreamingClient{events: []claude.StreamEvent{
		{Type: claude.EventMessageStart},
		{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockToolUse},
		{Type: claude.EventContentBlockStop, Index: 0, Block: claude.BlockToolUse,
			ToolID: "t1", ToolName: "execute_command",
			ToolInput: json.RawMessage(`{"command":"systemctl restart nginx"}`)},
		{Type: claude.EventContentBlockStart, Index: 1, Block: claude.BlockToolUse},
		{Type: claude.EventContentBlockStop, Index: 1, Block: claude.BlockToolUse,
			ToolID: "t2", ToolName: "execute_command",
			ToolInput: json.RawMessage(`{"command":"rm -rf /tmp/old-backup"}`)},
		{Type: claude.EventMessageDelta, StopReason: "tool_use"},
		{Type: claude.EventMessageStop},
	}}

	t.Run("approved", func(t *testing.T) {
		sink := &fakeSink{confirmBatchReply: true}
		a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil)
		a.ProcessInput(context.Background(), "do both")

		if len(sink.confirmBatchReqs) != 1 {
			t.Fatalf("want exactly 1 ConfirmBatch call, got %d", len(sink.confirmBatchReqs))
		}
		if len(sink.confirmBatchReqs[0]) != 2 {
			t.Fatalf("want batch of 2 commands, got %d", len(sink.confirmBatchReqs[0]))
		}
		// 批准 → 两条都执行（两条 tool result）。
		if len(sink.toolResults) != 2 {
			t.Fatalf("want 2 tool results after approval, got %d", len(sink.toolResults))
		}
	})

	t.Run("denied", func(t *testing.T) {
		sink := &fakeSink{confirmBatchReply: false}
		a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil)
		a.ProcessInput(context.Background(), "do both")

		if len(sink.confirmBatchReqs) != 1 {
			t.Fatalf("want exactly 1 ConfirmBatch call, got %d", len(sink.confirmBatchReqs))
		}
		// 拒绝 → 不应执行任何工具。
		if len(sink.toolResults) != 0 {
			t.Fatalf("want 0 tool results when denied, got %d: %v", len(sink.toolResults), sink.toolResults)
		}
	})
}

// systemRecordingClient 记录每次 MessagesStream 收到的 system 提示词，
// 其余行为复用 mockClaudeClient。
type systemRecordingClient struct {
	mockClaudeClient
	systems []string
}

func (c *systemRecordingClient) MessagesStream(ctx context.Context, system string, msgs []claude.Message, tools []claude.Tool) (<-chan claude.StreamEvent, func(), error) {
	c.systems = append(c.systems, system)
	return c.mockClaudeClient.MessagesStream(ctx, system, msgs, tools)
}

// staticResolver 把任意 refs 展开为固定目标（agent 层测试用；plugin 包
// 内部的 fakeResolver 未导出）。Exec 留 nil：stubExecPlugin 忽略执行器。
type staticResolver struct {
	targets []plugin.Target
}

func (s *staticResolver) Names(_ []string) ([]string, []string) {
	names := make([]string, len(s.targets))
	for i, t := range s.targets {
		names[i] = t.Name
	}
	return names, nil
}

func (s *staticResolver) Resolve(_ []string) ([]plugin.Target, []string, error) {
	return s.targets, nil, nil
}

// TestSystemPromptRefreshPerInput: 主机清单可能随会话中途增删，Agent 必须
// 在每次 ProcessInput 开始时用最新 catalog 重建系统提示词——第二次调用
// 发出的 system 要包含新清单且不含旧清单。
func TestSystemPromptRefreshPerInput(t *testing.T) {
	catalogText := "主机清单: web-01"
	calls := 0
	client := &systemRecordingClient{mockClaudeClient: mockClaudeClient{
		responses: []claude.APIResponse{
			{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "ok"}}},
			{StopReason: "end_turn", Content: []claude.ContentBlock{{Type: "text", Text: "ok"}}},
		},
	}}
	a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), &fakeSink{}, nil,
		WithCatalog(func() string {
			calls++
			return catalogText
		}),
	)

	a.ProcessInput(context.Background(), "first")
	if len(client.systems) != 1 {
		t.Fatalf("want 1 stream call after first input, got %d", len(client.systems))
	}
	if !contains(client.systems[0], "主机清单: web-01") || !contains(client.systems[0], "多主机 Linux 运维指挥台") {
		t.Fatalf("first system prompt must embed catalog + base prompt, got:\n%s", client.systems[0])
	}

	catalogText = "主机清单: web-01, db-01"
	a.ProcessInput(context.Background(), "second")
	if len(client.systems) != 2 {
		t.Fatalf("want 2 stream calls total, got %d", len(client.systems))
	}
	if !contains(client.systems[1], "db-01") {
		t.Fatalf("second input must see refreshed catalog, got:\n%s", client.systems[1])
	}
	if calls < 2 {
		t.Fatalf("catalog must be re-read per ProcessInput, called %d times", calls)
	}
}

// riskyToolEvents 构造一条 execute_command（Risky，走确认流）的流事件。
func riskyToolEvents(id, input string) []claude.StreamEvent {
	return []claude.StreamEvent{
		{Type: claude.EventMessageStart},
		{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockToolUse},
		{Type: claude.EventContentBlockStop, Index: 0, Block: claude.BlockToolUse,
			ToolID: id, ToolName: "execute_command", ToolInput: json.RawMessage(input)},
		{Type: claude.EventMessageDelta, StopReason: "tool_use"},
		{Type: claude.EventMessageStop},
	}
}

// TestConfirmRequestCarriesHosts: 确认弹窗必须展示目标引用展开后的主机名
// （TargetNames），批准后命令真正执行。
func TestConfirmRequestCarriesHosts(t *testing.T) {
	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "ok"})
	registry.SetResolver(&staticResolver{targets: []plugin.Target{
		{Name: "web-01"}, {Name: "web-02"},
	}})

	client := &fakeStreamingClient{events: riskyToolEvents("t1", `{"command":"systemctl restart nginx","hosts":["web"]}`)}
	sink := &fakeSink{confirmBatchReply: true}
	a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil)
	a.ProcessInput(context.Background(), "restart nginx")

	if len(sink.confirmBatchReqs) != 1 || len(sink.confirmBatchReqs[0]) != 1 {
		t.Fatalf("want 1 ConfirmBatch call with 1 request, got %+v", sink.confirmBatchReqs)
	}
	got := sink.confirmBatchReqs[0][0].Hosts
	if len(got) != 2 || got[0] != "web-01" || got[1] != "web-02" {
		t.Fatalf("ConfirmRequest.Hosts = %v, want [web-01 web-02]", got)
	}
	if len(sink.toolResults) != 1 {
		t.Fatalf("approved call must execute, got %d tool results", len(sink.toolResults))
	}
}

// TestAuditEntryWritten: 工具执行与拒绝都要落审计 JSONL，条目含 tool、
// 展开后的 hosts、批准与否。
func TestAuditEntryWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := audit.Open(path)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	defer logger.Close()

	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "restarted"})
	registry.SetResolver(&staticResolver{targets: []plugin.Target{
		{Name: "web-01"}, {Name: "web-02"},
	}})

	t.Run("approved", func(t *testing.T) {
		client := &fakeStreamingClient{events: riskyToolEvents("t1", `{"command":"systemctl restart nginx","hosts":["web"]}`)}
		sink := &fakeSink{confirmBatchReply: true}
		a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil,
			WithAudit(logger), WithSession("sess-1"))
		a.ProcessInput(context.Background(), "restart nginx")

		e := readSingleAuditEntry(t, path, "execute_command")
		if e.Session != "sess-1" || !e.Approved || e.IsError {
			t.Fatalf("entry fields wrong: %+v", e)
		}
		if len(e.Hosts) != 2 || e.Hosts[0] != "web-01" || e.Hosts[1] != "web-02" {
			t.Fatalf("audit hosts = %v, want [web-01 web-02]", e.Hosts)
		}
		if !contains(e.Output, "restarted") {
			t.Fatalf("audit output = %q", e.Output)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		client := &fakeStreamingClient{events: riskyToolEvents("t2", `{"command":"rm -rf /tmp/old","hosts":["web"]}`)}
		sink := &fakeSink{confirmBatchReply: false}
		a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil,
			WithAudit(logger), WithSession("sess-1"))
		a.ProcessInput(context.Background(), "cleanup")

		e := readSingleAuditEntry(t, path, "execute_command")
		if e.Approved || !e.IsError || e.Output != "user rejected" {
			t.Fatalf("rejected entry fields wrong: %+v", e)
		}
		if len(e.Hosts) != 2 {
			t.Fatalf("rejected entry must still record hosts, got %v", e.Hosts)
		}
	})

	t.Run("dispatch error", func(t *testing.T) {
		// 有工具、无 resolver：已批准的调用在 Registry.Execute 返回 error
		// （分发失败）——必须落审计，不能零记录。
		brokenRegistry := plugin.NewRegistry()
		brokenRegistry.Register(&stubExecPlugin{output: "never runs"})
		client := &fakeStreamingClient{events: riskyToolEvents("t3", `{"command":"uptime"}`)}
		sink := &fakeSink{confirmBatchReply: true}
		a := New(&config.Config{MaxSteps: 1}, client, brokenRegistry, safety.NewGate(), sink, nil,
			WithAudit(logger), WithSession("sess-1"))
		a.ProcessInput(context.Background(), "uptime")

		e := readSingleAuditEntry(t, path, "execute_command")
		if !e.Approved || !e.IsError || !contains(e.Output, "dispatch error") {
			t.Fatalf("dispatch error entry wrong: %+v", e)
		}
		if !contains(e.Output, "resolver") {
			t.Fatalf("dispatch error entry should carry the cause: %+v", e)
		}
	})
}

// TestAuditAskUserEntry: ask_user 分支曾提前 continue 完全绕过审计——
// 正常回答与 AskUser 报错两条路径都必须落条目（ask_user 面向用户而非
// 主机，无 hosts 概念，Hosts 留空）。ask_user 不走 registry 分发，
// 空 registry 即可驱动。
func TestAuditAskUserEntry(t *testing.T) {
	askEvents := func(id string) []claude.StreamEvent {
		return []claude.StreamEvent{
			{Type: claude.EventMessageStart},
			{Type: claude.EventContentBlockStart, Index: 0, Block: claude.BlockToolUse},
			{Type: claude.EventContentBlockStop, Index: 0, Block: claude.BlockToolUse,
				ToolID: id, ToolName: "ask_user",
				ToolInput: json.RawMessage(`{"question":"清理哪种日志？"}`)},
			{Type: claude.EventMessageDelta, StopReason: "tool_use"},
			{Type: claude.EventMessageStop},
		}
	}

	t.Run("answered", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		logger, err := audit.Open(path)
		if err != nil {
			t.Fatalf("audit.Open: %v", err)
		}
		defer logger.Close()

		client := &fakeStreamingClient{events: askEvents("ask-1")}
		sink := &fakeSink{askReply: "压缩归档"}
		a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil,
			WithAudit(logger), WithSession("sess-ask"))
		a.ProcessInput(context.Background(), "清理日志")

		e := readSingleAuditEntry(t, path, "ask_user")
		if e.Session != "sess-ask" || !e.Approved || e.IsError {
			t.Fatalf("ask_user entry fields wrong: %+v", e)
		}
		if !contains(string(e.Input), "清理哪种日志？") {
			t.Fatalf("ask_user input = %s; want question json", e.Input)
		}
		if e.Output != "压缩归档" {
			t.Fatalf("ask_user output = %q; want answer", e.Output)
		}
	})

	t.Run("ask error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		logger, err := audit.Open(path)
		if err != nil {
			t.Fatalf("audit.Open: %v", err)
		}
		defer logger.Close()

		client := &fakeStreamingClient{events: askEvents("ask-2")}
		sink := &fakeSink{askErr: errors.New("user dismissed")}
		a := New(&config.Config{MaxSteps: 1}, client, plugin.NewRegistry(), safety.NewGate(), sink, nil,
			WithAudit(logger), WithSession("sess-ask"))
		a.ProcessInput(context.Background(), "清理日志")

		e := readSingleAuditEntry(t, path, "ask_user")
		if !e.IsError {
			t.Fatalf("ask_user error entry must set IsError: %+v", e)
		}
		if !contains(e.Output, "ask error") || !contains(e.Output, "user dismissed") {
			t.Fatalf("ask_user error output = %q; want error summary", e.Output)
		}
	})
}

// TestAuditWriteFailureLogged: 审计写入失败不得静默吞掉，必须以
// audit_error 事件落到 observability 日志。
func TestAuditWriteFailureLogged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := audit.Open(path)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	// 先关闭底层文件，后续 Log 必然失败（write → closed file）。
	if err := logger.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: "ok"})
	registry.SetResolver(&staticResolver{targets: []plugin.Target{{Name: "web-01"}}})
	client := &fakeStreamingClient{events: riskyToolEvents("t1", `{"command":"uptime","hosts":["web-01"]}`)}
	sink := &fakeSink{confirmBatchReply: true}
	memLog := observability.NewMemoryLogger()
	a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil,
		WithAudit(logger), WithLogger(memLog))
	a.ProcessInput(context.Background(), "uptime")

	hasAuditError := false
	for _, ev := range memLog.Events() {
		if ev.Type == "audit_error" {
			hasAuditError = true
		}
	}
	if !hasAuditError {
		t.Fatal("audit write failure must surface as audit_error event")
	}
	// 工具本身照常执行：审计故障不阻塞主流程。
	if len(sink.toolResults) != 1 {
		t.Fatalf("tool must still execute despite audit failure, got %d results", len(sink.toolResults))
	}
}

// TestWithAuditNilMeansNoAudit: audit.Open 失败时调用方（gengar-headless/
// gengar-server）把 *audit.Logger 置 nil 后仍传参。接口化后 typed-nil 会
// 装箱为非 nil 接口，若不归一化，工具执行时的 nil 检查失效、在 nil
// receiver 上调 Log 直接 panic——「审计未启用」语义必须不因接口化破坏。
func TestWithAuditNilMeansNoAudit(t *testing.T) {
	a := &Agent{}
	WithAudit(nil)(a)
	if a.auditLog != nil {
		t.Fatal("untyped nil must disable audit")
	}
	WithAudit((*audit.Logger)(nil))(a)
	if a.auditLog != nil {
		t.Fatal("typed-nil *audit.Logger must disable audit, not wrap into a non-nil interface")
	}
}

// TestMultiHostAggregateSurvivesAgentTruncation: 20 主机 × 长输出经
// Registry per-host 截断后已在预算内；agent 层不得再对多主机聚合结果做
// head/tail 截断——那会把中间主机的分节头（含 [FAILED] 标记）静默砍掉。
// fakeSink.toolResults 捕获的 body 与传给 conv.AddToolResult 的 content
// 同源，可据此断言模型实际收到的内容。
func TestMultiHostAggregateSurvivesAgentTruncation(t *testing.T) {
	registry := plugin.NewRegistry()
	registry.Register(&stubExecPlugin{output: strings.Repeat("x", 8000)})
	targets := make([]plugin.Target, 20)
	for i := range targets {
		targets[i] = plugin.Target{Name: fmt.Sprintf("h%02d", i)}
	}
	registry.SetResolver(&staticResolver{targets: targets})

	client := &fakeStreamingClient{events: riskyToolEvents("t1", `{"command":"journalctl -u nginx","hosts":["all"]}`)}
	sink := &fakeSink{confirmBatchReply: true}
	a := New(&config.Config{MaxSteps: 1}, client, registry, safety.NewGate(), sink, nil)
	a.ProcessInput(context.Background(), "check nginx logs everywhere")

	if len(sink.toolResults) != 1 {
		t.Fatalf("want 1 tool result, got %d", len(sink.toolResults))
	}
	body := sink.toolResults[0].Body
	for i := 0; i < 20; i++ {
		header := fmt.Sprintf("===== h%02d =====", i)
		if !contains(body, header) {
			t.Fatalf("agent must not truncate away section %q; body len=%d", header, len(body))
		}
	}
	if len(body) >= 20*8000 {
		t.Fatalf("registry per-host truncation should bound the aggregate, len=%d", len(body))
	}
	// 回归锚点：旧行为（agent 层再过 4000/1500）只能留下 ~3000 字符。
	if len(body) < 4000 {
		t.Fatalf("body too short (%d) — did registry truncation over-shrink?", len(body))
	}
}

// readSingleAuditEntry 读取审计 JSONL（轮转分片优先，旧固定名兼容），
// 返回含指定 tool 的最后一条（并断言存在）。
func readSingleAuditEntry(t *testing.T, path, tool string) audit.Entry {
	t.Helper()
	var raw []byte
	if shards, _ := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + "-*.jsonl"); len(shards) > 0 {
		for _, sh := range shards {
			b, err := os.ReadFile(sh)
			if err != nil {
				continue
			}
			raw = append(raw, b...)
			if len(raw) > 0 && raw[len(raw)-1] != '\n' {
				raw = append(raw, '\n')
			}
		}
	} else {
		var err error
		raw, err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("read audit: %v", err)
		}
	}
	var entry audit.Entry
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e audit.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad jsonl line %q: %v", line, err)
		}
		if e.Tool == tool {
			entry = e
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry for %q in:\n%s", tool, raw)
	}
	return entry
}
