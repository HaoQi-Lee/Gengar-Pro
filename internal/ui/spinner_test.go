package ui

import (
	"encoding/json"
	"testing"
)

func TestFormatToolDisplayBashCommand(t *testing.T) {
	got := FormatToolDisplay("execute_command", json.RawMessage(`{"command":"ls -la"}`))
	want := "Bash(ls -la)"
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestFormatToolDisplayReadFile(t *testing.T) {
	got := FormatToolDisplay("read_file", json.RawMessage(`{"path":"/etc/hosts"}`))
	want := "Read(/etc/hosts)"
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestFormatToolDisplayFallbackTruncates(t *testing.T) {
	long := json.RawMessage(`{"x":"` + repeat("a", 200) + `"}`)
	got := FormatToolDisplay("unknown", long)
	if len(got) > 75 {
		t.Fatalf("expected truncation, got len %d: %q", len(got), got)
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
