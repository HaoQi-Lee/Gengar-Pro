package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/memory"
)

// MemoryManager plugin 暴露 memory_read / memory_write / memory_delete 三个
// tool，让 Agent 按主机持久化和读取服务器基本信息与历史操作记录。
//
// 与 KnowledgeBase 不同，Memory 是运行时动态写入的外部资源，OpenStore
// 失败不应阻止 Gengar 启动，由调用方（appcore）做好降级处理。
type MemoryManager struct {
	store *memory.Store
}

func NewMemoryManager(s *memory.Store) *MemoryManager {
	return &MemoryManager{store: s}
}

func (mm *MemoryManager) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "memory_read",
			Description: "Read persistent memory by category (system_info | history | all) for a host from ~/.gengar-pro/memory/<host>.json across sessions.\nUse when: recalling values stored earlier (host inventory, user preferences); avoiding redundant probes the user already answered.\nReturns: the stored category's contents as JSON, or empty if nothing is stored.",
			InputSchema: json.RawMessage(
				`{"type":"object","properties":{"host":{"type":"string","description":"Target host name (required)"},"category":{"type":"string","description":"system_info | history | all"}},"required":["host","category"]}`),
			RiskLevel: 0,
		},
		{
			Name:        "memory_write",
			Description: "Persist data under a category (system_info | history) for a host in ~/.gengar-pro/memory/<host>.json so it survives across sessions.\nUse when: caching expensive probe results (OS version, installed services); recording user-provided context worth keeping.\nReturns: empty on success.",
			InputSchema: json.RawMessage(
				`{"type":"object","properties":{"host":{"type":"string","description":"Target host name (required)"},"category":{"type":"string","description":"system_info | history"},"data":{"description":"Object for system_info, array for history"}},"required":["host","category","data"]}`),
			RiskLevel: 0,
		},
		{
			Name:        "memory_delete",
			Description: "Remove a category (system_info | history | all) for a host from ~/.gengar-pro/memory/<host>.json.\nUse when: a stored category is now stale or wrong; cleaning up after a host changes.\nReturns: empty on success.",
			InputSchema: json.RawMessage(
				`{"type":"object","properties":{"host":{"type":"string","description":"Target host name (required)"},"category":{"type":"string","description":"system_info | history | all"}},"required":["host","category"]}`),
			RiskLevel: 1,
		},
	}
}

// Execute 读写本地持久化记忆，不触达任何目标机（exec 忽略）。
func (mm *MemoryManager) Execute(ctx context.Context, toolName string, input json.RawMessage, _ executor.Executor) (ToolResult, error) {
	switch toolName {
	case "memory_read":
		return mm.handleRead(input)
	case "memory_write":
		return mm.handleWrite(input)
	case "memory_delete":
		return mm.handleDelete(input)
	default:
		return ToolResult{}, fmt.Errorf("MemoryManager: unknown tool %q", toolName)
	}
}

// parseHost 提取并校验 host 参数，返回该主机的 Memory。
func (mm *MemoryManager) parseHost(input json.RawMessage, dst any) (*memory.Memory, error) {
	if err := json.Unmarshal(input, dst); err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	var ho struct {
		Host string `json:"host"`
	}
	_ = json.Unmarshal(input, &ho)
	if ho.Host == "" {
		return nil, fmt.Errorf("host is required (managed host name)")
	}
	return mm.store.Get(ho.Host), nil
}

func (mm *MemoryManager) handleRead(input json.RawMessage) (ToolResult, error) {
	var p struct {
		Host     string `json:"host"`
		Category string `json:"category"`
	}
	m, err := mm.parseHost(input, &p)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if p.Category == "" {
		return ToolResult{
			Content: "category is required (system_info | history | all)",
			IsError: true,
		}, nil
	}

	result, err := m.Read(p.Category)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}

	pretty, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("marshal result: %v", err), IsError: true}, nil
	}
	return ToolResult{Content: string(pretty)}, nil
}

func (mm *MemoryManager) handleWrite(input json.RawMessage) (ToolResult, error) {
	// 用 RawMessage 接 data，保留任意 JSON 结构（object 或 array）
	var p struct {
		Host     string          `json:"host"`
		Category string          `json:"category"`
		Data     json.RawMessage `json:"data"`
	}
	m, err := mm.parseHost(input, &p)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if p.Category == "" {
		return ToolResult{
			Content: "category is required (system_info | history)",
			IsError: true,
		}, nil
	}
	if len(p.Data) == 0 {
		return ToolResult{Content: "data is required", IsError: true}, nil
	}

	var data interface{}
	if err := json.Unmarshal(p.Data, &data); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid data JSON: %v", err), IsError: true}, nil
	}

	if err := m.Write(p.Category, data); err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := m.Save(); err != nil {
		return ToolResult{Content: fmt.Sprintf("save failed: %v", err), IsError: true}, nil
	}
	return ToolResult{Content: fmt.Sprintf("ok: %s saved for host %s", p.Category, p.Host)}, nil
}

func (mm *MemoryManager) handleDelete(input json.RawMessage) (ToolResult, error) {
	var p struct {
		Host     string `json:"host"`
		Category string `json:"category"`
	}
	m, err := mm.parseHost(input, &p)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if p.Category == "" {
		return ToolResult{
			Content: "category is required (system_info | history | all)",
			IsError: true,
		}, nil
	}

	if err := m.Delete(p.Category); err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// "all" 分支 Delete 已删除落盘文件，不再 Save
	if p.Category != "all" {
		if err := m.Save(); err != nil {
			return ToolResult{Content: fmt.Sprintf("save failed: %v", err), IsError: true}, nil
		}
	}
	return ToolResult{Content: fmt.Sprintf("ok: %s deleted for host %s", p.Category, p.Host)}, nil
}
