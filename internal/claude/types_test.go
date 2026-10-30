package claude

import (
	"encoding/json"
	"testing"
)

func TestMessageMarshal(t *testing.T) {
	msg := Message{
		Role: "user",
		Content: []ContentBlock{
			{Type: "text", Text: "Hello"},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if string(data) != `{"role":"user","content":[{"type":"text","text":"Hello"}]}` {
		t.Errorf("unexpected marshal result: %s", data)
	}
}

func TestToolUseContentBlock(t *testing.T) {
	raw := json.RawMessage(`{"command":"ls"}`)
	block := ContentBlock{
		Type:  "tool_use",
		ID:    "toolu_123",
		Name:  "execute_command",
		Input: raw,
	}
	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)
	if parsed["type"] != "tool_use" {
		t.Errorf("type = %v, want tool_use", parsed["type"])
	}
	if parsed["name"] != "execute_command" {
		t.Errorf("name = %v, want execute_command", parsed["name"])
	}
}

func TestToolResultMarshal(t *testing.T) {
	msg := Message{
		Role: "user",
		Content: []ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: "toolu_123",
				Content:   "command output here",
			},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)
	content := parsed["content"].([]interface{})[0].(map[string]interface{})
	if content["tool_use_id"] != "toolu_123" {
		t.Errorf("tool_use_id = %v, want toolu_123", content["tool_use_id"])
	}
}

func TestAPIResponseParse(t *testing.T) {
	raw := `{
		"id": "msg_001",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "text", "text": "Let me check"},
			{"type": "tool_use", "id": "toolu_001", "name": "execute_command", "input": {"command": "ls"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 100, "output_tokens": 50}
	}`
	var resp APIResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("Content length = %d, want 2", len(resp.Content))
	}
	if resp.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want text", resp.Content[0].Type)
	}
	if resp.Content[1].Type != "tool_use" {
		t.Errorf("Content[1].Type = %q, want tool_use", resp.Content[1].Type)
	}
	if resp.Content[1].Name != "execute_command" {
		t.Errorf("Content[1].Name = %q, want execute_command", resp.Content[1].Name)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", resp.Usage.InputTokens)
	}
}
