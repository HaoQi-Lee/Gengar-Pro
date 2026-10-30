package plugin

import (
	"context"
	"encoding/json"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/safety"
)

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	RiskLevel   safety.Level    `json:"-"`
	// Targeting 标记目标型工具：输入带 hosts 参数，由 Registry 解析并
	// 按主机 fan-out；插件每次收到的是单主机的 exec。控制面工具为 false。
	Targeting bool `json:"-"`
}

type ToolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
	// Hosts 记录本次调用实际生效的目标主机名（单机 fan-out 也填写），
	// 供 Agent 层审计与 UI 归属展示。03-T3 起由 Registry 填写。
	Hosts []string `json:"-"`
}

type Plugin interface {
	Tools() []ToolDefinition
	// Execute 在单台目标机上执行工具。控制面工具忽略 exec（传 nil 不触发执行）。
	Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error)
}
