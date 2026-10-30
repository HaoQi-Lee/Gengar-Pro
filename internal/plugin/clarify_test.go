package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"gengar-pro/internal/safety"
)

func TestClarifyPlugin_Tools(t *testing.T) {
	p := NewClarifyPlugin()
	tools := p.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() count = %d, want 1", len(tools))
	}
	td := tools[0]
	if td.Name != "ask_user" {
		t.Errorf("Tool name = %q, want ask_user", td.Name)
	}
	if td.RiskLevel != safety.LevelSafe {
		t.Errorf("RiskLevel = %v, want LevelSafe", td.RiskLevel)
	}
	if td.Targeting {
		t.Errorf("ask_user Targeting = true, want false (control-plane)")
	}
	// Validate InputSchema is valid JSON
	var schema map[string]interface{}
	if err := json.Unmarshal(td.InputSchema, &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestClarifyPlugin_Execute_ReturnsError(t *testing.T) {
	p := NewClarifyPlugin()
	_, err := p.Execute(context.Background(), "ask_user", json.RawMessage(`{"question":"test?","options":[{"label":"A","description":"a"}]}`), nil)
	if err == nil {
		t.Error("Execute should return error (handled by agent loop)")
	}
}
