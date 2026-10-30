package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"gengar-pro/internal/executor"
)

// SystemdManager 提供 systemd/journalctl 服务管理工具。全部工具用 argv
// 形式执行：模型可控的 service 名不经 shell 重新解释，`nginx; reboot`
// 只会得到 systemctl 的参数错误而非执行注入（评审 ①-4）。
type SystemdManager struct{}

func NewSystemdManager() *SystemdManager { return &SystemdManager{} }

func (sm *SystemdManager) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "systemctl_status",
			Description: "Show the current status of a systemd service (active/inactive, recent logs).\nUse when: checking if a service is up; debugging a failed start; before restarting.\nReturns: systemctl status output including active state and last journal lines. Prefer over execute_command + systemctl status.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string","description":"Service name"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["service"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "systemctl_start",
			Description: "Start a systemd service.\nUse when: bringing a service online after install; recovering from a crash.\nReturns: command result (empty on success). Prefer over execute_command + systemctl start.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string","description":"Service name"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["service"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "systemctl_stop",
			Description: "Stop a systemd service.\nUse when: taking a service offline for maintenance; freeing a bound port; before a config-changing restart.\nReturns: command result. Prefer over execute_command + systemctl stop.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string","description":"Service name"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["service"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "systemctl_restart",
			Description: "Restart a systemd service (stop + start).\nUse when: applying a config change; recovering from a stuck service.\nReturns: command result. Prefer over execute_command + systemctl restart.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string","description":"Service name"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["service"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "journalctl_logs",
			Description: "View recent systemd journal lines for a service unit.\nUse when: investigating why a service failed; collecting evidence for diagnosis.\nReturns: last N journal entries for the unit. Prefer over execute_command + journalctl.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string","description":"Service name"},"lines":{"type":"integer","description":"Number of lines","default":50},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["service"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
	}
}

func (sm *SystemdManager) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	switch toolName {
	case "systemctl_status":
		var p struct {
			Service string `json:"service"`
		}
		json.Unmarshal(input, &p)
		return sm.exec(ctx, exec, "systemctl", "status", p.Service, "--no-pager")
	case "systemctl_start":
		var p struct {
			Service string `json:"service"`
		}
		json.Unmarshal(input, &p)
		return sm.exec(ctx, exec, "systemctl", "start", p.Service)
	case "systemctl_stop":
		var p struct {
			Service string `json:"service"`
		}
		json.Unmarshal(input, &p)
		return sm.exec(ctx, exec, "systemctl", "stop", p.Service)
	case "systemctl_restart":
		var p struct {
			Service string `json:"service"`
		}
		json.Unmarshal(input, &p)
		return sm.exec(ctx, exec, "systemctl", "restart", p.Service)
	case "journalctl_logs":
		var p struct {
			Service string `json:"service"`
			Lines   int    `json:"lines"`
		}
		json.Unmarshal(input, &p)
		if p.Lines <= 0 {
			p.Lines = 50 // 负数/零一律回落默认（评审 ①-4 参数健壮性）
		}
		return sm.exec(ctx, exec, "journalctl", "-u", p.Service, "--no-pager", "-n", strconv.Itoa(p.Lines))
	default:
		return ToolResult{}, fmt.Errorf("SystemdManager: unknown tool %q", toolName)
	}
}

func (sm *SystemdManager) exec(ctx context.Context, exec executor.Executor, name string, args ...string) (ToolResult, error) {
	output, err := exec.RunArgv(ctx, name, args...)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}
