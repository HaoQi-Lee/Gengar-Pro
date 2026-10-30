package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"gengar-pro/internal/executor"
)

// FileManager provides file I/O tools. All tools use argv-form exec
// (or executor WriteFile for write_file), so user-supplied paths,
// patterns, and content cannot inject shell metacharacters.
// FileManager 本身无状态：执行器按调用注入，永远只作用于单台目标机。
type FileManager struct{}

func NewFileManager() *FileManager { return &FileManager{} }

func (fm *FileManager) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "read_file",
			Description: "Read the full contents of a text file.\nUse when: viewing config files; checking source code; reading a log file you know the path of.\nReturns: the file's content as a string. Prefer this over execute_command + cat.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["path"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "write_file",
			Description: "Write content to a file, creating parent directories as needed. Overwrites if the file already exists.\nUse when: creating a config file; writing a deployment script; saving generated content. Always check existing content with read_file first if overwriting matters.\nReturns: empty on success. Prefer this over execute_command + cat > / echo > redirects.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"content":{"type":"string","description":"Content to write"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["path","content"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "list_directory",
			Description: "List files and subdirectories under a path (one level, like ls -la).\nUse when: exploring an unfamiliar directory; checking what files exist before reading; verifying a deploy target.\nReturns: ls -la style listing. Prefer over execute_command + ls.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory path to list"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["path"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "find_files",
			Description: "Find files by name pattern under a directory tree (via find -maxdepth -name).\nUse when: locating config files when the exact path is unknown; surveying which .log files exist; checking for stale artifacts.\nReturns: newline-separated list of matching paths. Prefer over execute_command + find.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Root directory to search from"},"name":{"type":"string","description":"Filename glob matched against basename only (find -name semantics), e.g. *.conf"},"max_depth":{"type":"integer","description":"Maximum directory depth (default 3)","default":3},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["path","name"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "grep_files",
			Description: "Search file contents for a regex pattern (extended regex, via grep -nE).\nUse when: finding which file contains an error string; auditing config values across a directory; locating a log entry by message.\nReturns: matching lines prefixed with file:line. Set recursive=true to descend into subdirectories. For large trees, narrow path to avoid timeout. Prefer over execute_command + grep.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Extended regular expression (grep -E syntax)"},"path":{"type":"string","description":"File or directory to search"},"recursive":{"type":"boolean","description":"Descend into subdirectories (default false)","default":false},"ignore_case":{"type":"boolean","description":"Case-insensitive match (default false)","default":false},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["pattern","path"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
	}
}

func (fm *FileManager) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	switch toolName {
	case "read_file":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		return fm.exec(ctx, exec, "cat", p.Path)

	case "write_file":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		// Create parent dirs via argv, then write the file without shell.
		if _, err := exec.RunArgv(ctx, "mkdir", "-p", filepath.Dir(p.Path)); err != nil {
			return ToolResult{Content: fmt.Sprintf("Error creating parent dir: %v", err), IsError: true}, nil
		}
		if err := exec.WriteFile(ctx, p.Path, []byte(p.Content)); err != nil {
			return ToolResult{Content: fmt.Sprintf("Error writing file: %v", err), IsError: true}, nil
		}
		return ToolResult{Content: ""}, nil

	case "list_directory":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		return fm.exec(ctx, exec, "ls", "-la", p.Path)

	case "find_files":
		var p struct {
			Path     string `json:"path"`
			Name     string `json:"name"`
			MaxDepth int    `json:"max_depth"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		if p.MaxDepth <= 0 {
			p.MaxDepth = 3
		}
		return fm.exec(ctx, exec, "find", p.Path, "-maxdepth", strconv.Itoa(p.MaxDepth), "-name", p.Name)

	case "grep_files":
		var p struct {
			Pattern    string `json:"pattern"`
			Path       string `json:"path"`
			Recursive  bool   `json:"recursive"`
			IgnoreCase bool   `json:"ignore_case"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		args := []string{"-nE"}
		if p.Recursive {
			args = append(args, "-r")
		}
		if p.IgnoreCase {
			args = append(args, "-i")
		}
		args = append(args, p.Pattern, p.Path)
		return fm.exec(ctx, exec, "grep", args...)

	default:
		return ToolResult{}, fmt.Errorf("FileManager: unknown tool %q", toolName)
	}
}

func (fm *FileManager) exec(ctx context.Context, exec executor.Executor, name string, args ...string) (ToolResult, error) {
	output, err := exec.RunArgv(ctx, name, args...)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}
