package plain_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gengar-pro/internal/ui"
	"gengar-pro/internal/ui/plain"
)

// driveEvents replays the same Sink calls against any implementation.
func driveEvents(s ui.Sink) {
	s.Step(1, 5)
	s.Thinking(true)
	s.Thinking(false)
	s.AssistantTextDelta("hello ")
	s.AssistantTextDelta("world")
	s.AssistantTextEnd()
	s.ToolCallStart("t1", "Bash(ls)")
	s.ToolCallResult("t1", "a\nb\nc", true)
	s.ToolCallStart("t2", "Bash(rm)")
	s.ToolCallResult("t2", "permission denied", false)
	s.Warning("hint")
	s.Error("boom")
	s.Forbidden("blacklisted")
}

func TestPlainSinkRendersEvents(t *testing.T) {
	var buf bytes.Buffer
	driveEvents(plain.New(&buf))
	out := buf.String()

	for _, want := range []string{
		"步骤 1/5",
		"hello world",
		"⏺",
		"⎿",
		"⚠️",
		"❌",
		"🚫",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestPlainSinkAskUserBatchAutoSelects(t *testing.T) {
	ui.ConfirmAutoApprove = true
	defer func() { ui.ConfirmAutoApprove = false }()

	var buf bytes.Buffer
	s := plain.New(&buf)
	payload := json.RawMessage(`{"question":"q","header":"h","options":[{"label":"first","description":"d"},{"label":"second","description":"d"}]}`)
	ans, err := s.AskUser(payload)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ans != "first" {
		t.Fatalf("want first, got %q", ans)
	}
	if !strings.Contains(buf.String(), "auto-selected") {
		t.Fatalf("expected batch marker in output, got %q", buf.String())
	}
}

func TestPlainSinkConfirmHonorsAutoApprove(t *testing.T) {
	ui.ConfirmAutoApprove = true
	defer func() { ui.ConfirmAutoApprove = false }()
	s := plain.New(&bytes.Buffer{})
	if !s.ConfirmBatch([]ui.ConfirmRequest{{ToolName: "x", Summary: "y", HighRisk: true}}) {
		t.Fatal("batch mode must auto-approve HighRisk")
	}
}

// TestPlainSinkInfoWritesLine: slash-command output routed through Info must
// land verbatim (one line, trailing newline) so --batch output stays
// byte-stable when ctx.Out is a NewLineWriter backed by plain.Sink.
func TestPlainSinkInfoWritesLine(t *testing.T) {
	var buf bytes.Buffer
	plain.New(&buf).Info("hello world")
	if buf.String() != "hello world\n" {
		t.Errorf("Info output = %q, want %q", buf.String(), "hello world\n")
	}
}
