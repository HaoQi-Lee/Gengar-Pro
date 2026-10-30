// Package observability provides structured event logging for Gengar's
// E2E testing system. Events are recorded as JSONL for post-run analysis.
//
// The package offers three Logger implementations:
//   - Noop: discards events (production default)
//   - MemoryLogger: in-memory buffer (unit testing)
//   - FileLogger: JSONL file output (E2E test runs)
package observability

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// Event represents a single structured event in the agent's execution trace.
type Event struct {
	Timestamp   string          `json:"ts"`
	Type        string          `json:"type"` // tool_call | tool_result | response | safety_block | api_error | step_start | run_end | user_input
	Step        int             `json:"step"`
	ToolName    string          `json:"tool_name,omitempty"`
	ToolID      string          `json:"tool_id,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Output      string          `json:"output,omitempty"`
	Text        string          `json:"text,omitempty"`
	StopReason  string          `json:"stop_reason,omitempty"`
	SafetyLevel string          `json:"safety_level,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Logger writes structured agent events. All implementations must be safe for concurrent use.
type Logger interface {
	Log(ev Event)
	Close() error
}

// Noop returns a logger that discards all events.
func Noop() Logger { return noopLogger{} }

type noopLogger struct{}

func (noopLogger) Log(Event)    {}
func (noopLogger) Close() error { return nil }

// MemoryLogger records all events in memory for unit testing.
type MemoryLogger struct {
	mu     sync.Mutex
	events []Event
}

func NewMemoryLogger() *MemoryLogger { return &MemoryLogger{} }

func (m *MemoryLogger) Log(ev Event) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
}

func (m *MemoryLogger) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]Event, len(m.events))
	copy(cp, m.events)
	return cp
}

func (m *MemoryLogger) Close() error { return nil }

// FileLogger writes events as JSONL (one JSON object per line).
type FileLogger struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func NewFileLogger(path string) (Logger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &FileLogger{f: f, w: bufio.NewWriter(f)}, nil
}

// Log writes an event as a JSON line. Errors during write are silently
// discarded — Log is best-effort.
//
// Each event is flushed to the OS immediately. This is deliberate: in E2E mode
// the runner kills gengar on per-case timeout (SIGKILL) and force-removes the
// container, so the deferred Close() in main never runs. Without a per-event
// flush the bufio buffer would die with the process and the JSONL would be
// empty — leaving a timed-out run as a total black box. Flushing here pushes
// each event out of the Go buffer into the kernel, so a partial log always
// survives up to the point gengar was killed.
func (l *FileLogger) Log(ev Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.w.Write(data)
	l.w.WriteByte('\n')
	l.w.Flush()
	l.mu.Unlock()
}

func (l *FileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.w.Flush(); err != nil {
		return err
	}
	return l.f.Close()
}
