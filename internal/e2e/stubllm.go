//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// stubllm 是本地 SSE 桩服务器：扮演 Anthropic Messages API，按脚本回放
// 工具调用序列。桩不做任何对话理解——第 N 个 /v1/messages 请求对应脚本
// 第 N 回合；工具结果回传（下一请求）自然推进脚本。
//
// 帧格式逐字节对照 internal/claude/stream_test.go 的 sampleSSE：帧用有序
// struct 序列化（键序与 sampleSSE 完全一致），event:/data: 双行 + 空行。
// parseSSE 只认 data: 行的 JSON type 字段，但桩仍按上游真实 wire 格式发。
//
// 仅支持流式（agent 循环走 MessagesStream）。非流式请求打到桩只会收到
// SSE 体并因 JSON 反序列化失败而报错——桩不支持该路径，这是未定义的
// 失败形态而非可依赖的信号，需要覆盖非流式时须另行实现。
type StubLLM struct {
	Server *httptest.Server

	mu       sync.Mutex
	turn     int
	script   []Turn
	requests []json.RawMessage
	apiKeys  []string
	msgSeq   int
}

// Turn 是脚本的一个回合：ToolCalls 非空 → 发 tool_use 块
// （stop_reason=tool_use）；否则发文本块（stop_reason=end_turn）。
// Text 是可选的工具回合文本前导（text 块排在 tool 块前，同 sampleSSE）。
// StopReason 非空时覆盖默认停机原因——复刻异常收束形态（如 max_tokens
// 截断：文本发完但 stop_reason 不是 end_turn），覆盖 agent 侧对停机
// 原因的分支处理。
type Turn struct {
	ToolCalls  []ToolCall
	FinalText  string
	Text       string
	StopReason string
}

// ToolCall 描述一次工具调用。Input 为 nil 时发空对象 {}。
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// deltaChunk 是 input_json_delta / text_delta 的分片粒度（字节）。
// 故意取小值（sampleSSE 把 12 字节的 {"port":80} 拆成两帧；真实 API
// 常见 20-100），保证短输入也跨多个 delta 帧，让 parseSSE 的重组路径
// 在 E2E 下被真实走到。
const deltaChunk = 8

// startStubLLM 启动桩并注册 t.Cleanup 关闭。
func startStubLLM(t *testing.T, script []Turn) *StubLLM {
	t.Helper()
	s := &StubLLM{script: script}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.handleMessages)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

// URL 返回桩服务地址（作为 headless 的 ANTHROPIC_BASE_URL）。
func (s *StubLLM) URL() string { return s.Server.URL }

// RequestCount 返回已处理的请求数。
func (s *StubLLM) RequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Request 返回第 i 个请求的原始 body（0 起）。
func (s *StubLLM) Request(i int) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.requests) {
		return nil
	}
	return s.requests[i]
}

// APIKey 返回第 i 个请求携带的 x-api-key（校验客户端鉴权头接对了）。
func (s *StubLLM) APIKey(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.apiKeys) {
		return ""
	}
	return s.apiKeys[i]
}

func (s *StubLLM) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "stub: POST only", http.StatusMethodNotAllowed)
		return
	}
	// 鉴权校验必须发生在占用回合之前：被拒绝的请求不得推进脚本、不得
	// 进入录制，否则一次 401 重试会把后续合法请求错位到错误的回合。
	if r.Header.Get("x-api-key") == "" {
		http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"missing x-api-key"}}`, http.StatusUnauthorized)
		return
	}

	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	turn := s.turn
	s.turn++
	s.requests = append(s.requests, json.RawMessage(raw))
	s.apiKeys = append(s.apiKeys, r.Header.Get("x-api-key"))
	s.msgSeq++
	msgID := fmt.Sprintf("msg_e2e_%d", s.msgSeq)
	s.mu.Unlock()

	var turnPlan Turn
	if turn < len(s.script) {
		turnPlan = s.script[turn]
	} else {
		// 脚本耗尽：按协议 error 事件告知（客户端 parseSSE → EventError），
		// 而非静默空流——测试里这是 Agent 请求数超出脚本的明确信号。
		writeFrame(w, "error", frameError{
			Type:  "error",
			Error: frameErrorBody{Type: "api_error", Message: fmt.Sprintf("stub script exhausted: request %d beyond %d turns", turn+1, len(s.script))},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	writeFrame(w, "message_start", frameMessageStart{
		Type: "message_start",
		Message: frameStartMessage{
			ID: msgID, Model: "stub-model",
			Content: []any{},
			Usage:   frameUsage{InputTokens: 1},
		},
	})

	index := 0
	if turnPlan.Text != "" {
		writeTextBlock(w, index, turnPlan.Text)
		index++
	}
	for _, tc := range turnPlan.ToolCalls {
		writeToolBlock(w, index, tc)
		index++
	}
	if len(turnPlan.ToolCalls) == 0 {
		writeTextBlock(w, index, turnPlan.FinalText)
	}

	stopReason := "end_turn"
	if len(turnPlan.ToolCalls) > 0 {
		stopReason = "tool_use"
	}
	if turnPlan.StopReason != "" {
		stopReason = turnPlan.StopReason // 用例指定的异常停机（max_tokens 等）
	}
	writeFrame(w, "message_delta", frameMessageDelta{
		Type:  "message_delta",
		Delta: frameStopDelta{StopReason: stopReason},
		Usage: frameOutUsage{OutputTokens: 12},
	})
	writeFrame(w, "message_stop", frameMessageStop{Type: "message_stop"})
}

// ---- 帧 payload 类型：字段顺序即 JSON 键序，严格对照 sampleSSE ----

type frameUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type frameStartMessage struct {
	ID      string     `json:"id"`
	Model   string     `json:"model"`
	Content []any      `json:"content"`
	Usage   frameUsage `json:"usage"`
}

type frameMessageStart struct {
	Type    string            `json:"type"`
	Message frameStartMessage `json:"message"`
}

type frameTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type frameTextStart struct {
	Type         string         `json:"type"`
	Index        int            `json:"index"`
	ContentBlock frameTextBlock `json:"content_block"`
}

type frameTextDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type frameToolBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type frameToolStart struct {
	Type         string         `json:"type"`
	Index        int            `json:"index"`
	ContentBlock frameToolBlock `json:"content_block"`
}

type frameToolDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

type frameBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type frameStopDelta struct {
	StopReason string `json:"stop_reason"`
}

// frameOutUsage 是 message_delta 的 usage 形态：sampleSSE 里只有
// output_tokens（无 input_tokens 键）。
type frameOutUsage struct {
	OutputTokens int `json:"output_tokens"`
}

type frameMessageDelta struct {
	Type  string         `json:"type"`
	Delta frameStopDelta `json:"delta"`
	Usage frameOutUsage  `json:"usage"`
}

type frameMessageStop struct {
	Type string `json:"type"`
}

type frameErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type frameError struct {
	Type  string         `json:"type"`
	Error frameErrorBody `json:"error"`
}

// ---- 块发射 ----

// writeTextBlock 发一个完整 text 块：start → 分片 delta → stop。
func writeTextBlock(w http.ResponseWriter, index int, text string) {
	writeFrame(w, "content_block_start", frameTextStart{
		Type: "content_block_start", Index: index,
		ContentBlock: frameTextBlock{Type: "text", Text: ""},
	})
	for _, chunk := range chunkString(text) {
		var d frameTextDelta
		d.Type = "content_block_delta"
		d.Index = index
		d.Delta.Type = "text_delta"
		d.Delta.Text = chunk
		writeFrame(w, "content_block_delta", d)
	}
	writeFrame(w, "content_block_stop", frameBlockStop{Type: "content_block_stop", Index: index})
}

// writeToolBlock 发一个完整 tool_use 块：start（含 id/name/空 input，
// 同 sampleSSE）→ input_json_delta 分片 → stop（parseSSE 在 stop 时交出
// 重组后的完整输入）。
func writeToolBlock(w http.ResponseWriter, index int, tc ToolCall) {
	writeFrame(w, "content_block_start", frameToolStart{
		Type: "content_block_start", Index: index,
		ContentBlock: frameToolBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: map[string]any{}},
	})
	input := "{}"
	if tc.Input != nil {
		if raw, err := json.Marshal(tc.Input); err == nil {
			input = string(raw)
		}
	}
	for _, chunk := range chunkString(input) {
		var d frameToolDelta
		d.Type = "content_block_delta"
		d.Index = index
		d.Delta.Type = "input_json_delta"
		d.Delta.PartialJSON = chunk
		writeFrame(w, "content_block_delta", d)
	}
	writeFrame(w, "content_block_stop", frameBlockStop{Type: "content_block_stop", Index: index})
}

// writeFrame 输出单帧 SSE：event:/data: 双行 + 空行分隔。
func writeFrame(w http.ResponseWriter, event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// chunkString 把 s 按 deltaChunk 字节分片，但只在 UTF-8 符文边界切——
// 字节切会把中文文本劈成半个符文，json.Marshal 会把无效字节替换成
// U+FFFD，破坏 parseSSE 侧的文本重组。空串返回单个空分片（保持每块
// 至少一个 delta 帧的 wire 形态）。
func chunkString(s string) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	start := 0
	for i, r := range s {
		if i > start && i-start+runLen(r) > deltaChunk {
			out = append(out, s[start:i])
			start = i
		}
	}
	return append(out, s[start:])
}

func runLen(r rune) int {
	switch {
	case r < 0x80:
		return 1
	case r < 0x800:
		return 2
	case r < 0x10000:
		return 3
	default:
		return 4
	}
}
