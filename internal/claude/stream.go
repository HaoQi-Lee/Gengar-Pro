package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type EventType int

const (
	EventMessageStart EventType = iota
	EventContentBlockStart
	EventContentBlockDelta
	EventContentBlockStop
	EventMessageDelta
	EventMessageStop
	EventError
)

type BlockKind int

const (
	BlockUnknown BlockKind = iota
	BlockText
	BlockToolUse
)

// StreamEvent is the protocol-agnostic event the agent loop consumes.
// Field semantics:
//   - Type identifies the event family.
//   - Block + Index identify the content block (only meaningful for the
//     content_block_* family).
//   - Text carries text deltas (Type=ContentBlockDelta && Block=Text).
//   - ToolID/ToolName/ToolInput are populated on Block=ToolUse:
//     ContentBlockStart fills ID+Name (ToolInput is empty), ContentBlockStop
//     fills ToolInput with the assembled JSON.
//   - StopReason is populated on Type=MessageDelta from delta.stop_reason.
//   - Error is populated on Type=Error.
type StreamEvent struct {
	Type       EventType
	Block      BlockKind
	Index      int
	Text       string
	ToolID     string
	ToolName   string
	ToolInput  json.RawMessage
	StopReason string
	Error      string
}

// ErrStreamInterrupted signals the SSE stream ended before message_stop. The
// caller should rebuild whatever partial assistant text it accumulated and
// surface a "[stream interrupted]" indicator.
var ErrStreamInterrupted = errors.New("stream interrupted before message_stop")

// MessagesStream opens a streaming /v1/messages request. Returns a read-only
// channel of events and a cancel function that closes the stream early. The
// channel is closed when the stream finishes (cleanly or with error).
//
// On a clean stream, the last event is EventMessageStop and the channel is
// closed without error. On error, an EventError is sent before close, except
// for context cancellation which closes the channel with no extra event.
//
// 建流失败（网络/429/5xx）按 retryRetryable 重试 3 次（评审 ②-2）；
// 建立后的流走 streamHTTP（无整体 Timeout）+ 读空闲 watchdog——
// streamReadIdleTimeout 无帧即判死，长输出（>2 分钟合法慢流）不再被
// http.Client.Timeout 误杀。
func (c *Client) MessagesStream(ctx context.Context, system string, messages []Message, tools []Tool) (<-chan StreamEvent, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	streamCtx, cancel := context.WithCancel(ctx)

	reqBody := apiRequest{
		Model:       c.cfg.Model,
		MaxTokens:   DefaultMaxTokens,
		Temperature: c.cfg.Temperature,
		System:      system,
		Messages:    messages,
		Tools:       tools,
		Stream:      true,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		cancel()
		return nil, func() {}, fmt.Errorf("marshal request: %w", err)
	}

	// 建流（含服务端接受请求、返回 2xx）可重试；流建立后的中断不在此层
	// 重试（半途内容已消费，重放需上层重建整个 turn）。
	var httpResp *http.Response
	err = retryRetryable(streamCtx, func() error {
		req, rerr := http.NewRequestWithContext(streamCtx, "POST", c.cfg.BaseURL+"/v1/messages", bytes.NewReader(body))
		if rerr != nil {
			return fmt.Errorf("create request: %w", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.cfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("accept", "text/event-stream")

		resp, rerr := c.streamHTTP().Do(req)
		if rerr != nil {
			return fmt.Errorf("http request: %w", rerr)
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("API error %d: %s", resp.StatusCode, string(raw))
		}
		httpResp = resp
		return nil
	})
	if err != nil {
		cancel()
		return nil, func() {}, err
	}

	ch := make(chan StreamEvent, 32)
	go func() {
		defer close(ch)
		defer httpResp.Body.Close()
		if err := parseSSE(streamCtx, httpResp.Body, ch); err != nil {
			select {
			case ch <- StreamEvent{Type: EventError, Error: err.Error()}:
			case <-streamCtx.Done():
			}
		}
	}()
	return ch, cancel, nil
}

// parseSSE reads SSE frames from r, decodes the JSON payloads, and emits
// StreamEvent values on out. It returns nil on a clean message_stop, ctx.Err()
// on cancellation, ErrStreamInterrupted on premature EOF, or an error from
// JSON decoding.
//
// 读空闲看门狗（评审 ②-2）：scanner 的读取包一层 idle-deadline reader——
// streamReadIdleTimeout 无任何字节到达即返回错误判链路死亡。整请求
// Timeout 会杀 >2 分钟的合法长流，看门狗只杀「真没动静」的流。
func parseSSE(ctx context.Context, r io.Reader, out chan<- StreamEvent) error {
	scanner := bufio.NewScanner(newIdleReader(ctx, r, streamReadIdleTimeout))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	type toolBuf struct {
		id, name string
		input    strings.Builder
	}
	tools := map[int]*toolBuf{}
	blockKinds := map[int]BlockKind{}

	var dataLines []string
	flush := func() error {
		defer func() { dataLines = dataLines[:0] }()
		if len(dataLines) == 0 {
			return nil
		}
		raw := strings.Join(dataLines, "\n")
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(raw), &head); err != nil {
			return fmt.Errorf("sse decode head: %w", err)
		}
		switch head.Type {
		case "message_start":
			return send(ctx, out, StreamEvent{Type: EventMessageStart})
		case "content_block_start":
			var p struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				return err
			}
			ev := StreamEvent{Type: EventContentBlockStart, Index: p.Index}
			switch p.ContentBlock.Type {
			case "text":
				ev.Block = BlockText
			case "tool_use":
				ev.Block = BlockToolUse
				ev.ToolID = p.ContentBlock.ID
				ev.ToolName = p.ContentBlock.Name
				tools[p.Index] = &toolBuf{id: p.ContentBlock.ID, name: p.ContentBlock.Name}
			default:
				ev.Block = BlockUnknown
			}
			blockKinds[p.Index] = ev.Block
			return send(ctx, out, ev)
		case "content_block_delta":
			var p struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				return err
			}
			kind := blockKinds[p.Index]
			ev := StreamEvent{Type: EventContentBlockDelta, Index: p.Index, Block: kind}
			switch p.Delta.Type {
			case "text_delta":
				ev.Text = p.Delta.Text
			case "input_json_delta":
				if buf := tools[p.Index]; buf != nil {
					buf.input.WriteString(p.Delta.PartialJSON)
				}
				// Don't emit ContentBlockDelta for tool input fragments; the
				// agent only needs the assembled blob on Stop.
				return nil
			}
			return send(ctx, out, ev)
		case "content_block_stop":
			var p struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				return err
			}
			kind := blockKinds[p.Index]
			ev := StreamEvent{Type: EventContentBlockStop, Index: p.Index, Block: kind}
			if kind == BlockToolUse {
				if buf := tools[p.Index]; buf != nil {
					ev.ToolID = buf.id
					ev.ToolName = buf.name
					assembled := buf.input.String()
					if assembled == "" {
						assembled = "{}"
					}
					ev.ToolInput = json.RawMessage(assembled)
				}
			}
			return send(ctx, out, ev)
		case "message_delta":
			var p struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				return err
			}
			return send(ctx, out, StreamEvent{Type: EventMessageDelta, StopReason: p.Delta.StopReason})
		case "message_stop":
			return send(ctx, out, StreamEvent{Type: EventMessageStop})
		case "error":
			var p struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(raw), &p)
			return send(ctx, out, StreamEvent{Type: EventError, Error: p.Error.Message})
		case "ping":
			return nil
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		// Other fields (event:, id:, retry:) are ignored — JSON `type` is the
		// authoritative dispatch key.
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Caller is responsible for detecting clean stop by observing
	// EventMessageStop on the channel. parseSSE returns nil on EOF and lets
	// the channel close signal end-of-stream.
	return nil
}

// send writes ev to out unless ctx is done. Returns ctx.Err() to abort the
// parse on cancel.
func send(ctx context.Context, out chan<- StreamEvent, ev StreamEvent) error {
	select {
	case out <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// idleTimeoutError 标记读空闲超时（区别于普通网络错误，便于调用方归因）。
type idleTimeoutError struct{ after time.Duration }

func (e idleTimeoutError) Error() string {
	return fmt.Sprintf("stream idle for %s with no data — link presumed dead", e.after)
}

// newIdleReader 包装 r：单次 Read 超过 idle 无数据返回 idleTimeoutError。
// 靠 goroutine + timer 实现（net.Conn 的 SetReadDeadline 只对 Conn 生效，
// http.Response.Body 不一定暴露 Conn）。ctx 取消时唤醒阻塞的 Read。
func newIdleReader(ctx context.Context, r io.Reader, idle time.Duration) io.Reader {
	return &idleReader{ctx: ctx, r: r, idle: idle}
}

type idleReader struct {
	ctx  context.Context
	r    io.Reader
	idle time.Duration
}

func (ir *idleReader) Read(p []byte) (int, error) {
	if ir.ctx.Err() != nil {
		return 0, ir.ctx.Err()
	}
	type res struct {
		n   int
		err error
	}
	done := make(chan res, 1)
	go func() {
		n, err := ir.r.Read(p)
		done <- res{n, err}
	}()
	timer := time.NewTimer(ir.idle)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.n, r.err
	case <-timer.C:
		return 0, idleTimeoutError{after: ir.idle}
	case <-ir.ctx.Done():
		return 0, ir.ctx.Err()
	}
}
