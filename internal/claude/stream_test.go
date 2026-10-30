package claude

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

const sampleSSE = `event: message_start
data: {"type":"message_start","message":{"id":"m1","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"check_port","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"port\":80"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`

func TestParseSSESequence(t *testing.T) {
	r := strings.NewReader(sampleSSE)
	ch := make(chan StreamEvent, 32)
	if err := parseSSE(context.Background(), r, ch); err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	close(ch)

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) < 8 {
		t.Fatalf("expected at least 8 events, got %d", len(events))
	}
	// First content block: text "hello world".
	var fullText strings.Builder
	for _, ev := range events {
		if ev.Type == EventContentBlockDelta && ev.Block == BlockText && ev.Index == 0 {
			fullText.WriteString(ev.Text)
		}
	}
	if got := fullText.String(); got != "hello world" {
		t.Fatalf("text reassembly: want 'hello world', got %q", got)
	}
	// Tool block 1 emits ContentBlockStop with assembled input.
	var toolStopSeen bool
	for _, ev := range events {
		if ev.Type == EventContentBlockStop && ev.Block == BlockToolUse && ev.Index == 1 {
			toolStopSeen = true
			if string(ev.ToolInput) != `{"port":80}` {
				t.Fatalf("tool input reassembly want {\"port\":80}, got %s", string(ev.ToolInput))
			}
			if ev.ToolName != "check_port" || ev.ToolID != "t1" {
				t.Fatalf("tool meta lost: id=%q name=%q", ev.ToolID, ev.ToolName)
			}
		}
	}
	if !toolStopSeen {
		t.Fatalf("missing tool ContentBlockStop")
	}
	// Final stop_reason captured on MessageDelta.
	var stopReason string
	for _, ev := range events {
		if ev.Type == EventMessageDelta {
			stopReason = ev.StopReason
		}
	}
	if stopReason != "tool_use" {
		t.Fatalf("stop reason want tool_use, got %q", stopReason)
	}
}

func TestParseSSEContextCancel(t *testing.T) {
	r := strings.NewReader(sampleSSE)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before reading
	ch := make(chan StreamEvent, 4)
	err := parseSSE(ctx, r, ch)
	if err == nil {
		t.Fatalf("expected ctx err, got nil")
	}
}

// TestParseSSEIdleTimeout: 评审 ②-2 读空闲看门狗——流停滞（无字节到达）
// 超过 idle 阈值即判死，不无限挂起。用短阈值变体避免 90s 真等待：
// 直接测 idleReader 的语义。
func TestParseSSEIdleTimeout(t *testing.T) {
	r := &idleReader{ctx: context.Background(), r: blockingReader{}, idle: 50 * time.Millisecond}
	buf := make([]byte, 16)
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("expected idle timeout error, got nil")
	}
	if _, ok := err.(idleTimeoutError); !ok {
		t.Fatalf("err = %v; want idleTimeoutError", err)
	}
}

// blockingReader 永不产出数据（模拟死链）。
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {} // 阻塞；idleReader 的 timer 会先触发
}

// TestParseSSESlowButAliveNotKilled: 数据持续到达的慢流不触发看门狗——
// 每块间隔短于 idle 阈值即可（长输出合法慢）。
func TestParseSSESlowButAliveNotKilled(t *testing.T) {
	slow := &slowReader{chunks: 5}
	ch := make(chan StreamEvent, 32)
	err := parseSSE(context.Background(), slow, ch)
	if err != nil {
		t.Fatalf("slow-but-alive stream must not be killed: %v", err)
	}
}

// slowReader 每 10ms 吐一段 SSE 块（远小于 90s idle 阈值）。
type slowReader struct {
	chunks int
	pos    int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= s.chunks {
		return 0, io.EOF
	}
	frame := "data: {\"type\":\"ping\"}\n\n"
	s.pos++
	time.Sleep(10 * time.Millisecond)
	return copy(p, frame), nil
}
