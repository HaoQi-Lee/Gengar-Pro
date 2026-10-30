package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendMessage_TextResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %s, want test-key", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %s, want 2023-06-01", r.Header.Get("anthropic-version"))
		}
		resp := APIResponse{
			ID: "msg_001", Type: "message", Role: "assistant", StopReason: "end_turn",
			Content: []ContentBlock{{Type: "text", Text: "Nginx is running normally."}},
			Usage:   Usage{InputTokens: 50, OutputTokens: 20},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: server.URL, Model: "claude-sonnet-4-20250514"})
	resp, err := client.SendMessage(nil, "You are a helpful assistant.",
		[]Message{{Role: "user", Content: "Check nginx status"}}, nil)
	if err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if resp.Content[0].Text != "Nginx is running normally." {
		t.Errorf("Text = %q, want 'Nginx is running normally.'", resp.Content[0].Text)
	}
}

func TestSendMessage_ToolUseResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req apiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Tools) == 0 {
			t.Error("request should include tools")
		}
		resp := APIResponse{
			ID: "msg_002", Type: "message", Role: "assistant", StopReason: "tool_use",
			Content: []ContentBlock{
				{Type: "text", Text: "Let me check."},
				{Type: "tool_use", ID: "toolu_001", Name: "execute_command", Input: json.RawMessage(`{"command":"systemctl status nginx"}`)},
			},
			Usage: Usage{InputTokens: 100, OutputTokens: 30},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: server.URL, Model: "claude-sonnet-4-20250514"})
	tools := []Tool{
		{Name: "execute_command", Description: "Execute a shell command",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)},
	}
	resp, err := client.SendMessage(nil, "You are a helpful assistant.",
		[]Message{{Role: "user", Content: "Check nginx"}}, tools)
	if err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
	if resp.Content[1].Name != "execute_command" {
		t.Errorf("Tool name = %q, want execute_command", resp.Content[1].Name)
	}
}

func TestSendMessage_RetryOnError(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := APIResponse{ID: "msg_003", StopReason: "end_turn",
			Content: []ContentBlock{{Type: "text", Text: "OK"}}}
		json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: server.URL, Model: "claude-sonnet-4-20250514"})
	resp, err := client.SendMessage(nil, "",
		[]Message{{Role: "user", Content: "test"}}, nil)
	if err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}
	if resp.Content[0].Text != "OK" {
		t.Errorf("Text = %q, want OK", resp.Content[0].Text)
	}
	if callCount != 3 {
		t.Errorf("callCount = %d, want 3", callCount)
	}
}

// TestSendMessage_NoRetryOnClientError: 评审 ②-2——400/401 是客户端错误，
// 重试无意义：一次即返回。
func TestSendMessage_NoRetryOnClientError(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusUnauthorized)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(ClientConfig{APIKey: "bad-key", BaseURL: server.URL, Model: "m"})
	_, err := client.SendMessage(context.Background(), "", []Message{{Role: "user", Content: "x"}}, nil)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if callCount != 1 {
		t.Errorf("callCount = %d; want 1 (4xx must not retry)", callCount)
	}
}

// TestSendMessage_RetryOn429: 429 限流可重试，恢复后成功。
func TestSendMessage_RetryOn429(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(APIResponse{StopReason: "end_turn",
			Content: []ContentBlock{{Type: "text", Text: "ok"}}})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(ClientConfig{APIKey: "k", BaseURL: server.URL, Model: "m"})
	resp, err := client.SendMessage(context.Background(), "", []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("429 retry: %v", err)
	}
	if callCount != 2 || resp.Content[0].Text != "ok" {
		t.Fatalf("callCount=%d resp=%+v", callCount, resp)
	}
}

func TestTemperatureInRequest(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	zero := 0.0
	c := NewClient(ClientConfig{
		APIKey:      "test-key",
		BaseURL:     srv.URL,
		Model:       "test-model",
		Temperature: &zero,
	})

	ctx := context.Background()
	_, err := c.SendMessage(ctx, "system", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	temp, ok := req["temperature"]
	if !ok {
		t.Fatal("request body missing 'temperature' field")
	}
	if temp != 0.0 {
		t.Errorf("temperature = %v, want 0", temp)
	}
}

func TestNilTemperatureOmitted(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	ctx := context.Background()
	_, err := c.SendMessage(ctx, "system", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if _, ok := req["temperature"]; ok {
		t.Error("temperature should be omitted when nil, but was present")
	}
}
