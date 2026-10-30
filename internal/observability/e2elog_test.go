package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNoopLoggerDoesNothing(t *testing.T) {
	l := Noop()
	l.Log(Event{Type: "test"})
	if err := l.Close(); err != nil {
		t.Fatalf("Noop Close should not error, got: %v", err)
	}
}

func TestMemoryLoggerRecordsEvents(t *testing.T) {
	l := NewMemoryLogger()
	l.Log(Event{Type: "step_start", Step: 1})
	l.Log(Event{Type: "tool_call", Step: 1, ToolName: "execute_command"})
	l.Log(Event{Type: "response", Step: 1, Text: "hello"})

	events := l.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Type != "step_start" {
		t.Errorf("event[0].Type = %q, want step_start", events[0].Type)
	}
	if events[1].ToolName != "execute_command" {
		t.Errorf("event[1].ToolName = %q, want execute_command", events[1].ToolName)
	}
	if events[2].Text != "hello" {
		t.Errorf("event[2].Text = %q, want hello", events[2].Text)
	}
}

func TestFileLoggerWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	l.Log(Event{Type: "step_start", Step: 1})
	l.Log(Event{Type: "tool_call", Step: 1, ToolName: "pkg", ToolID: "t1"})
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var ev1, ev2 Event
	if err := json.Unmarshal([]byte(lines[0]), &ev1); err != nil {
		t.Fatalf("parse line 1: %v", err)
	}
	if ev1.Type != "step_start" || ev1.Step != 1 {
		t.Errorf("line 1: got type=%q step=%d, want step_start/1", ev1.Type, ev1.Step)
	}

	if err := json.Unmarshal([]byte(lines[1]), &ev2); err != nil {
		t.Fatalf("parse line 2: %v", err)
	}
	if ev2.ToolName != "pkg" || ev2.ToolID != "t1" {
		t.Errorf("line 2: got tool_name=%q tool_id=%q", ev2.ToolName, ev2.ToolID)
	}
}

// TestFileLoggerFlushesWithoutClose verifies that events reach disk as soon as
// they are logged, without waiting for Close(). This matters for E2E runs: when
// the runner times out it kills gengar (SIGKILL) / force-removes the container,
// so Close() never runs. If Log() only buffered, the JSONL would be 0 bytes and
// the run would be a black box. Each Log() must flush the bufio buffer to the OS.
func TestFileLoggerFlushesWithoutClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "durable.jsonl")

	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	// Close only for handle cleanup at teardown; all reads/assertions below
	// happen first, simulating a process killed mid-run before Close() runs.
	defer l.Close()

	l.Log(Event{Type: "step_start", Step: 1})
	l.Log(Event{Type: "tool_call", Step: 1, ToolName: "ping", ToolID: "t1"})

	// Read the file straight off disk, exactly as the runner's `cat` does when
	// collecting partial logs after a timeout.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile without Close: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines flushed to disk without Close(), got %d (data=%q)", len(lines), string(data))
	}

	var ev Event
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil {
		t.Fatalf("parse flushed line: %v", err)
	}
	if ev.ToolName != "ping" {
		t.Errorf("flushed event tool_name=%q, want ping", ev.ToolName)
	}
}

func TestFileLoggerThreadSafety(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.jsonl")

	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(step int) {
			defer wg.Done()
			l.Log(Event{Type: "step_start", Step: step})
		}(i)
	}
	wg.Wait()
	l.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 100 {
		t.Errorf("expected 100 lines, got %d", len(lines))
	}
}
