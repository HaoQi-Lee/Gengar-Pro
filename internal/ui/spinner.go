package ui

import (
	"encoding/json"
	"fmt"
)

// FormatToolDisplay produces a Claude-Code-style display name for a tool
// call, e.g. "Bash(ls -la)" for execute_command. Falls back to the raw
// tool name with a truncated JSON snippet for unknown tools.
func FormatToolDisplay(name string, input json.RawMessage) string {
	switch name {
	case "execute_command":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &p); err == nil && p.Command != "" {
			return fmt.Sprintf("Bash(%s)", p.Command)
		}
	case "read_file":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &p); err == nil && p.Path != "" {
			return fmt.Sprintf("Read(%s)", p.Path)
		}
	case "write_file":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &p); err == nil && p.Path != "" {
			return fmt.Sprintf("Write(%s)", p.Path)
		}
	case "edit_file":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &p); err == nil && p.Path != "" {
			return fmt.Sprintf("Edit(%s)", p.Path)
		}
	}
	// Fallback: <name>(<truncated input>)
	snippet := string(input)
	if len(snippet) > 60 {
		snippet = snippet[:57] + "..."
	}
	return fmt.Sprintf("%s(%s)", name, snippet)
}
