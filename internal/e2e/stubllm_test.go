//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"gengar-pro/internal/claude"
)

// streamOnce 用真实 internal/claude 流式客户端打一次 /v1/messages，
// 收集全部 StreamEvent 返回。
func streamOnce(t *testing.T, client *claude.Client, msgs []claude.Message) []claude.StreamEvent {
	t.Helper()
	ch, cancel, err := client.MessagesStream(context.Background(), "e2e system prompt", msgs, nil)
	if err != nil {
		t.Fatalf("MessagesStream: %v", err)
	}
	defer cancel()
	var events []claude.StreamEvent
	for ev := range ch {
		events = append(events, ev)
		if ev.Type == claude.EventError {
			t.Fatalf("unexpected stream error: %s", ev.Error)
		}
	}
	return events
}

// TestStubLLMDrivesRealClient —— 最强验证：真实 internal/claude 流式客户端
// 打桩。脚本两回合：T1 工具调用（stop_reason=tool_use），T2 终文本
// （end_turn）。断言客户端侧事件序列 / 工具输入重组 / 停止原因，
// 以及桩侧请求体录制（第二请求必须带回工具结果）。
func TestStubLLMDrivesRealClient(t *testing.T) {
	toolInput := map[string]any{"command": "echo hi", "hosts": []string{"all"}}
	stub := startStubLLM(t, []Turn{
		{ToolCalls: []ToolCall{{ID: "t1", Name: "execute_command", Input: toolInput}}},
		{FinalText: "两台主机执行完成 hi"},
	})
	client := claude.NewClient(claude.ClientConfig{
		BaseURL: stub.URL(), APIKey: "e2e-key", Model: "stub-model",
	})

	history := []claude.Message{{Role: "user", Content: "在所有机器上执行 echo hi"}}

	// ---- 回合 1：工具调用 ----
	events := streamOnce(t, client, history)
	if len(events) == 0 || events[0].Type != claude.EventMessageStart {
		t.Fatalf("first event want MessageStart, got %+v", events)
	}
	var toolStop *claude.StreamEvent
	var stopReason string
	sawMessageStop := false
	for i := range events {
		ev := events[i]
		switch ev.Type {
		case claude.EventContentBlockStop:
			if ev.Block == claude.BlockToolUse {
				toolStop = &events[i]
			}
		case claude.EventMessageDelta:
			stopReason = ev.StopReason
		case claude.EventMessageStop:
			sawMessageStop = true
		}
	}
	if toolStop == nil {
		t.Fatalf("no tool ContentBlockStop in %+v", events)
	}
	if toolStop.ToolID != "t1" || toolStop.ToolName != "execute_command" {
		t.Fatalf("tool meta: id=%q name=%q", toolStop.ToolID, toolStop.ToolName)
	}
	var gotInput map[string]any
	if err := json.Unmarshal(toolStop.ToolInput, &gotInput); err != nil {
		t.Fatalf("reassembled tool input not JSON: %v (%s)", err, toolStop.ToolInput)
	}
	if gotInput["command"] != "echo hi" {
		t.Fatalf("tool input command: %v", gotInput)
	}
	if hosts, ok := gotInput["hosts"].([]any); !ok || len(hosts) != 1 || hosts[0] != "all" {
		t.Fatalf("tool input hosts: %v", gotInput["hosts"])
	}
	if stopReason != "tool_use" {
		t.Fatalf("turn 1 stop reason want tool_use, got %q", stopReason)
	}
	if !sawMessageStop {
		t.Fatalf("missing MessageStop")
	}

	// ---- 回合 2：带工具结果历史 → 终文本 ----
	rawInput, _ := json.Marshal(toolInput)
	history = append(history, claude.Message{Role: "assistant", Content: []claude.ContentBlock{{
		Type: "tool_use", ID: "t1", Name: "execute_command", Input: rawInput,
	}}})
	history = append(history, claude.Message{Role: "user", Content: []claude.ContentBlock{{
		Type: "tool_result", ToolUseID: "t1", Content: "web-1: hi\nweb-2: hi",
	}}})

	events = streamOnce(t, client, history)
	var text strings.Builder
	stopReason = ""
	for _, ev := range events {
		switch {
		case ev.Type == claude.EventContentBlockDelta && ev.Block == claude.BlockText:
			text.WriteString(ev.Text)
		case ev.Type == claude.EventMessageDelta:
			stopReason = ev.StopReason
		}
	}
	if got := text.String(); got != "两台主机执行完成 hi" {
		t.Fatalf("final text reassembly: got %q", got)
	}
	if stopReason != "end_turn" {
		t.Fatalf("turn 2 stop reason want end_turn, got %q", stopReason)
	}

	// ---- 桩侧录制：第二请求必须带回工具结果 ----
	if got := stub.RequestCount(); got != 2 {
		t.Fatalf("request count want 2, got %d", got)
	}
	var req2 struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(stub.Request(1), &req2); err != nil {
		t.Fatalf("request 2 body: %v", err)
	}
	var sawToolResult bool
	for _, m := range req2.Messages {
		var blocks []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
		}
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue // 纯文本 content，跳过
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID == "t1" && strings.Contains(b.Content, "web-1: hi") {
				sawToolResult = true
			}
		}
	}
	if !sawToolResult {
		t.Fatalf("request 2 does not round-trip tool result: %s", stub.Request(1))
	}
}

// TestStubLLMStopReasonOverride —— 用例指定的停机原因（max_tokens 截断
// 形态）必须原样出现在 message_delta：文本块照发、停机原因非 end_turn。
// E06 依赖此形态复刻「报告被上限掐断」的异常收束。
func TestStubLLMStopReasonOverride(t *testing.T) {
	stub := startStubLLM(t, []Turn{
		{FinalText: "半份报告", StopReason: "max_tokens"},
	})
	client := claude.NewClient(claude.ClientConfig{
		BaseURL: stub.URL(), APIKey: "e2e-key", Model: "stub-model",
	})
	events := streamOnce(t, client, []claude.Message{{Role: "user", Content: "hi"}})
	var text strings.Builder
	stopReason := ""
	for _, ev := range events {
		switch {
		case ev.Type == claude.EventContentBlockDelta && ev.Block == claude.BlockText:
			text.WriteString(ev.Text)
		case ev.Type == claude.EventMessageDelta:
			stopReason = ev.StopReason
		}
	}
	if got := text.String(); got != "半份报告" {
		t.Fatalf("text want 半份报告, got %q", got)
	}
	if stopReason != "max_tokens" {
		t.Fatalf("stop reason want max_tokens, got %q", stopReason)
	}
}

// TestStubLLMFrameShapesTextual —— 原始 HTTP 读回帧文本，逐串对照
// stream_test.go sampleSSE 的帧形态（结构逐字节兼容：event:/data: 前缀、
// JSON 键序、input_json_delta 分片、stop_reason 位置）。
func TestStubLLMFrameShapesTextual(t *testing.T) {
	stub := startStubLLM(t, []Turn{
		{ToolCalls: []ToolCall{{ID: "t1", Name: "check_port", Input: map[string]any{"port": 80}}}},
	})
	req, err := http.NewRequest(http.MethodPost, stub.URL()+"/v1/messages",
		strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "e2e-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	for _, want := range []string{
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_e2e_1","model":"stub-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"\n\n",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"check_port","input":{}}}`,
		`"type":"input_json_delta","partial_json":`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("frame body missing %q\n--- body ---\n%s", want, s)
		}
	}
	if n := strings.Count(s, `"type":"input_json_delta"`); n < 2 {
		t.Fatalf("input_json_delta want >=2 chunks (port:80 split), got %d\n%s", n, s)
	}
}

// TestStubLLMRejectsNonMessages —— 桩只服务 POST /v1/messages。
func TestStubLLMRejectsNonMessages(t *testing.T) {
	stub := startStubLLM(t, []Turn{{FinalText: "x"}})
	resp, err := http.Get(stub.URL() + "/v1/messages")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/messages want 405, got %d", resp.StatusCode)
	}
	resp2, err := http.Post(stub.URL()+"/v1/count_tokens", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /v1/count_tokens want 404, got %d", resp2.StatusCode)
	}
}

// TestStubLLMScriptExhausted —— 脚本耗尽后按协议 error 事件报错（客户端
// 收到 EventError），而非静默空响应。
func TestStubLLMScriptExhausted(t *testing.T) {
	stub := startStubLLM(t, []Turn{{FinalText: "one"}})
	client := claude.NewClient(claude.ClientConfig{BaseURL: stub.URL(), APIKey: "e2e-key", Model: "stub-model"})
	msgs := []claude.Message{{Role: "user", Content: "q"}}

	if evs := streamOnce(t, client, msgs); len(evs) == 0 {
		t.Fatal("turn 1 produced no events")
	}

	ch, cancel, err := client.MessagesStream(context.Background(), "s", msgs, nil)
	if err != nil {
		t.Fatalf("MessagesStream: %v", err)
	}
	defer cancel()
	var errText string
	for ev := range ch {
		if ev.Type == claude.EventError {
			errText = ev.Error
		}
	}
	if !strings.Contains(errText, "script exhausted") {
		t.Fatalf("want script-exhausted error event, got %q", errText)
	}
}
