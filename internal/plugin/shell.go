package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"gengar-pro/internal/executor"
)

// ShellExecutor 提供 execute_command 兜底工具，命令经目标机 shell 执行。
type ShellExecutor struct{}

func NewShellExecutor() *ShellExecutor { return &ShellExecutor{} }

// Tools returns the execute_command tool definition.
//
// The Do-NOT-use list in the description intentionally references tools
// (find_files, grep_files, system_info, ping, dns_lookup, http_check)
// that are introduced in Step 3-5 of the tool-enhancement plan. Each step
// is internally consistent against the FINAL tool set, not the
// step-in-progress set. If you git-bisect into this range and see the LLM
// retry with an unknown_tool error, that's expected.
func (se *ShellExecutor) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "execute_command",
			Description: "Execute an arbitrary shell command via the target host's shell. This is a FALLBACK for cases not covered by any specialized tool.\nDo NOT use this for: port checks (use check_port); process checks (use check_process); disk/memory/network status (use check_disk/check_memory/check_network); log tails (use tail_log); file read/write/list/search (use read_file/write_file/list_directory/find_files/grep_files); Docker (use docker_*); systemd (use systemctl_*); package install/remove/search (use package_*); ping/dns/http (use ping/dns_lookup/http_check); system info (use system_info); Redis operations (use redis_discover/redis_cli/redis_rdb).\nUse when: no specialized tool fits the task.\nReturns: combined stdout+stderr of the command.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The shell command to execute"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["command"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
	}
}

func (se *ShellExecutor) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	if toolName != "execute_command" {
		return ToolResult{}, fmt.Errorf("ShellExecutor: unknown tool %q", toolName)
	}
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{}, fmt.Errorf("parse input: %w", err)
	}
	output, err := exec.RunShell(ctx, params.Command)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}
