package claude

import (
	"strings"
	"testing"
)

func TestTruncateOutput_Short(t *testing.T) {
	result := TruncateOutput("short text", 100, 40)
	if result != "short text" {
		t.Errorf("short text should not be truncated, got: %q", result)
	}
}

func TestTruncateOutput_Long(t *testing.T) {
	longText := strings.Repeat("a", 5000)
	result := TruncateOutput(longText, 4000, 1500)
	if len(result) >= 5000 {
		t.Error("long text should be truncated")
	}
	if !strings.Contains(result, "... truncated") {
		t.Error("truncated text should contain truncation notice")
	}
	headPart := strings.Repeat("a", 1500)
	tailPart := strings.Repeat("a", 1500)
	if !strings.HasPrefix(result, headPart) {
		t.Error("truncated text should preserve head")
	}
	if !strings.HasSuffix(result, tailPart) {
		t.Error("truncated text should preserve tail")
	}
}

func TestTruncateOutput_ExactLimit(t *testing.T) {
	text := strings.Repeat("x", 4000)
	result := TruncateOutput(text, 4000, 1500)
	if result != text {
		t.Error("text at exact limit should not be truncated")
	}
}
