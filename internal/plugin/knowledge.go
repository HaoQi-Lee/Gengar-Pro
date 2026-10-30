package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gengar-pro/internal/consensus"
	"gengar-pro/internal/executor"
)

// KnowledgeBase plugin 暴露 query_consensus tool，供 Agent 按需查阅
// 当前 profile 的环境共识条目。零副作用（RiskLevel 0），自动执行。
type KnowledgeBase struct {
	c *consensus.Consensus
}

func NewKnowledgeBase(c *consensus.Consensus) *KnowledgeBase {
	return &KnowledgeBase{c: c}
}

func (kb *KnowledgeBase) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "query_consensus",
			Description: "Query the environment consensus knowledge base for the current target system.\nUse when: deciding which tool variant to use (e.g. apt vs yum); checking known service-specific paths/configs for this distro; recalling host-specific conventions.\nReturns: structured consensus entry matching the query, or empty if no entry exists.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"The guideline id, e.g. yum-repo, ntp-servers, backup-policy"}},"required":["topic"]}`),
			RiskLevel:   0,
		},
	}
}

// Execute 查询本地共识数据，不触达任何目标机（exec 忽略）。
func (kb *KnowledgeBase) Execute(ctx context.Context, toolName string, input json.RawMessage, _ executor.Executor) (ToolResult, error) {
	if toolName != "query_consensus" {
		return ToolResult{}, fmt.Errorf("KnowledgeBase: unknown tool %q", toolName)
	}
	// nil 防御：csns 加载失败的部署仍可能注册本插件；无此防护时
	// kb.c.IDs() 会 panic，且不进入 Registry 的 fan-out 错误恢复路径。
	if kb.c == nil {
		return ToolResult{Content: "Error: consensus not loaded on this deployment", IsError: true}, nil
	}
	var p struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(p.Topic) == "" {
		return ToolResult{
			Content: "topic is empty. Available topics: " + strings.Join(kb.c.IDs(), ", "),
			IsError: true,
		}, nil
	}
	g := kb.c.Find(p.Topic)
	if g == nil {
		return ToolResult{
			Content: fmt.Sprintf("no consensus topic %q in profile %q. Available topics: %s",
				p.Topic, kb.c.Profile, strings.Join(kb.c.IDs(), ", ")),
			IsError: true,
		}, nil
	}
	return ToolResult{
		Content: fmt.Sprintf("# %s\n\n%s", g.Title, g.Content),
	}, nil
}
