package plugin

import (
	"encoding/json"
	"testing"
)

func TestToolDefinitionMarshal(t *testing.T) {
	td := ToolDefinition{
		Name:        "execute_command",
		Description: "Execute a shell command",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
	}
	data, err := json.Marshal(td)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)
	if parsed["name"] != "execute_command" {
		t.Errorf("name = %v, want execute_command", parsed["name"])
	}
	if _, ok := parsed["risk_level"]; ok {
		t.Error("risk_level should not appear in JSON output")
	}
}

func TestToolResultJSON(t *testing.T) {
	tr := ToolResult{Content: "output text", IsError: false}
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if string(data) != `{"content":"output text","is_error":false}` {
		t.Errorf("unexpected: %s", data)
	}
}
