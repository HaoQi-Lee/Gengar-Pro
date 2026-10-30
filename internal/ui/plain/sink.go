// Package plain provides a non-TTY ui.Sink that writes events to an
// io.Writer in the same byte-level format as the legacy *ui.Terminal
// produced under --batch mode. It exists so the Bubble Tea sink can take
// over interactive TTY rendering without disturbing E2E or piped usage.
package plain

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gengar-pro/internal/ui"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
	colorBold   = "\033[1m"
)

type Sink struct {
	w          io.Writer
	streamBuf  strings.Builder
	lastToolOK bool
}

func New(w io.Writer) *Sink {
	return &Sink{w: w, lastToolOK: true}
}

func (s *Sink) UserSubmit(_ string) {}

func (s *Sink) Thinking(_ bool) {
	// Non-TTY path: no spinner. Output remains stable for E2E.
}

func (s *Sink) AssistantTextDelta(delta string) {
	s.streamBuf.WriteString(delta)
}

func (s *Sink) AssistantTextEnd() {
	if s.streamBuf.Len() == 0 {
		return
	}
	out := s.streamBuf.String()
	s.streamBuf.Reset()
	fmt.Fprintf(s.w, "\n%s%s%s\n", colorGreen, out, colorReset)
}

func (s *Sink) ToolCallStart(_ string, display string) {
	fmt.Fprintf(s.w, "\n%s⏺%s %s\n", colorCyan, colorReset, display)
}

func (s *Sink) ToolCallResult(_ string, body string, ok bool) {
	s.lastToolOK = ok
	prefixColor := colorGray
	if !ok {
		prefixColor = colorRed
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for i, line := range lines {
		if i == 0 {
			fmt.Fprintf(s.w, "%s⎿%s %s\n", prefixColor, colorReset, line)
		} else {
			fmt.Fprintf(s.w, "  %s\n", line)
		}
	}
}

func (s *Sink) Step(current, total int) {
	fmt.Fprintf(s.w, "\n%s--- 步骤 %d/%d ---%s\n", colorYellow, current, total, colorReset)
}

func (s *Sink) Forbidden(reason string) {
	fmt.Fprintf(s.w, "%s🚫 操作被安全策略禁止%s\n   原因: %s\n", colorRed, colorReset, reason)
}

func (s *Sink) Warning(msg string) {
	fmt.Fprintf(s.w, "%s⚠️  %s%s\n", colorYellow, msg, colorReset)
}

func (s *Sink) Error(msg string) {
	fmt.Fprintf(s.w, "%s❌ %s%s\n", colorRed, msg, colorReset)
}

func (s *Sink) Info(text string) {
	fmt.Fprintln(s.w, text)
}

func (s *Sink) ConfirmBatch(_ []ui.ConfirmRequest) bool {
	return ui.ConfirmAutoApprove // batch path always auto-approves
}

func (s *Sink) AskUser(input json.RawMessage) (string, error) {
	var ci struct {
		Question string `json:"question"`
		Header   string `json:"header"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if err := json.Unmarshal(input, &ci); err != nil {
		return "", fmt.Errorf("invalid ask_user input: %w", err)
	}
	if len(ci.Options) < 2 {
		return "", fmt.Errorf("ask_user: at least 2 options required, got %d", len(ci.Options))
	}
	answer := ci.Options[0].Label
	fmt.Fprintf(s.w, "\n❓ %s (batch: auto-selected %q)\n", ci.Header, answer)
	return answer, nil
}

func (s *Sink) Close() {}

// compile-time interface check.
var _ ui.Sink = (*Sink)(nil)
