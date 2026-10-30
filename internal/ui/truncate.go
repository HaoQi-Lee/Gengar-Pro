package ui

import "encoding/json"

// TruncateMiddle shortens s to at most max runes by keeping the head and tail
// and joining them with a single "…" when truncation is needed. If s already
// fits within max it is returned unchanged; max <= 0 returns the empty string;
// max == 1 returns "…".
//
// Counting is by rune, which is sufficient for shell commands (essentially
// ASCII); CJK paths may be off by a display column in extreme cases — an
// accepted trade-off.
func TruncateMiddle(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	head := (max - 1) / 2
	tail := max - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// FormatConfirmSummary returns a clean, single-line summary of a tool call for
// display in the confirm modal (replacing the raw JSON the UI used to show):
//
//   - execute_command → the bare command
//   - read_file/write_file/edit_file → the bare path
//   - anything else → FormatToolDisplay's wrapped form (e.g. "Name(keyarg)")
//
// The result is what the modal truncates with TruncateMiddle when it overflows.
func FormatConfirmSummary(name string, input json.RawMessage) string {
	switch name {
	case "execute_command":
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &p) == nil && p.Command != "" {
			return p.Command
		}
	case "read_file", "write_file", "edit_file":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(input, &p) == nil && p.Path != "" {
			return p.Path
		}
	}
	return FormatToolDisplay(name, input)
}
