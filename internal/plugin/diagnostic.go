package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gengar-pro/internal/executor"
)

// DiagnosticKit provides read-only system diagnostic tools. All tools
// use argv-form exec, so user-supplied names/paths cannot inject shell
// metacharacters (which the previous bash -c form was vulnerable to).
// DiagnosticKit 无状态：执行器按调用注入，永远只作用于单台目标机。
type DiagnosticKit struct{}

func NewDiagnosticKit() *DiagnosticKit { return &DiagnosticKit{} }

func (dk *DiagnosticKit) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "check_port",
			Description: "Check which process is listening on a TCP port (via ss -tlnp).\nUse when: diagnosing port conflicts; verifying a service is bound; before starting a server that needs a port.\nReturns: process name, PID, bind address for the port, or empty if nothing listens. Prefer over execute_command + ss/netstat/lsof.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"port":{"type":"integer","description":"Port number"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["port"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "check_process",
			Description: "Find a running process by name (via pgrep -af).\nUse when: confirming a daemon is up; checking before kill; troubleshooting \"service running?\" questions.\nReturns: matching process lines (PID and full command line), or empty if not found. Prefer over execute_command + ps.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Process name to search for"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["name"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "check_disk",
			Description: "Show disk usage for a path or root filesystem (via df -h).\nUse when: investigating \"disk full\" errors; checking before large downloads; pre-deploy sanity.\nReturns: df -h table (filesystem, size, used, avail, use%, mount). Prefer over execute_command + df.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to check","default":"/"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "check_memory",
			Description: "Show system memory and swap usage (via free -h).\nUse when: investigating OOM; capacity planning; checking before starting a memory-hungry service.\nReturns: free -h table (total, used, free, shared, buff/cache, available). Prefer over execute_command + free.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "check_network",
			Description: "List listening TCP ports and the processes bound to them (via ss -tlnp).\nUse when: surveying which services are exposed; investigating connectivity issues from the server side.\nReturns: ss -tlnp output. Prefer over execute_command + ss/netstat.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "tail_log",
			Description: "Show the last N lines of a log file (via tail).\nUse when: investigating recent errors; verifying a deploy produced expected entries.\nReturns: last N lines of the file (default 50). Prefer over execute_command + tail.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Log file path"},"lines":{"type":"integer","description":"Number of lines","default":50},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["path"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "system_info",
			Description: "Show a compact summary of OS and host info: kernel, hostname, uptime/load, and /etc/os-release distribution name.\nUse when: starting work on an unfamiliar host; the user asks \"what kind of machine is this\"; before choosing distro-specific commands.\nReturns: a multi-section text block, one section per source. Prefer over execute_command + uname/uptime/cat /etc/os-release.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
	}
}

func (dk *DiagnosticKit) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	switch toolName {
	case "check_port":
		var p struct {
			Port int `json:"port"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		return dk.exec(ctx, exec, "ss", "-tlnp", "sport", "=", ":"+strconv.Itoa(p.Port))

	case "check_process":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		return dk.exec(ctx, exec, "pgrep", "-af", p.Name)

	case "check_disk":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		if p.Path == "" {
			p.Path = "/"
		}
		return dk.exec(ctx, exec, "df", "-h", p.Path)

	case "check_memory":
		return dk.exec(ctx, exec, "free", "-h")

	case "check_network":
		return dk.exec(ctx, exec, "ss", "-tlnp")

	case "tail_log":
		var p struct {
			Path  string `json:"path"`
			Lines int    `json:"lines"`
		}
		if err := json.Unmarshal(input, &p); err != nil {
			return ToolResult{}, fmt.Errorf("parse input: %w", err)
		}
		if p.Lines <= 0 {
			p.Lines = 50
		}
		return dk.exec(ctx, exec, "tail", "-n", strconv.Itoa(p.Lines), p.Path)

	case "system_info":
		return dk.systemInfo(ctx, exec)

	default:
		return ToolResult{}, fmt.Errorf("DiagnosticKit: unknown tool %q", toolName)
	}
}

// systemInfo runs four read-only probes and concatenates their output.
// Partial failures are surfaced as a "[failed: ...]" marker so a missing
// /etc/os-release doesn't void the entire result.
func (dk *DiagnosticKit) systemInfo(ctx context.Context, exec executor.Executor) (ToolResult, error) {
	type probe struct {
		label string
		name  string
		args  []string
	}
	probes := []probe{
		{"kernel", "uname", []string{"-a"}},
		{"hostname", "hostname", nil},
		{"uptime", "uptime", nil},
		{"os-release", "cat", []string{"/etc/os-release"}},
	}
	var sb strings.Builder
	for _, pr := range probes {
		sb.WriteString("=== ")
		sb.WriteString(pr.label)
		sb.WriteString(" ===\n")
		out, err := exec.RunArgv(ctx, pr.name, pr.args...)
		if err != nil {
			sb.WriteString("[failed: ")
			sb.WriteString(err.Error())
			sb.WriteString("]\n")
		}
		sb.Write(out)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			sb.WriteByte('\n')
		}
	}
	return ToolResult{Content: sb.String()}, nil
}

func (dk *DiagnosticKit) exec(ctx context.Context, exec executor.Executor, name string, args ...string) (ToolResult, error) {
	output, err := exec.RunArgv(ctx, name, args...)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}
