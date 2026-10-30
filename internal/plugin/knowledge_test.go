package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gengar-pro/internal/consensus"
)

func newTestKB() *KnowledgeBase {
	c := &consensus.Consensus{
		Profile: "dev",
		Guidelines: []consensus.Guideline{
			{ID: "yum-repo", Title: "YUM 源配置", Summary: "开发环境 YUM 源地址", Content: "base url 是 http://mirrors.dev/..."},
			{ID: "backup-policy", Title: "备份策略", Summary: "修改前备份", Content: "备份到 /var/backups/gengar"},
		},
	}
	return NewKnowledgeBase(c)
}

func TestKnowledgeBase_ToolsShape(t *testing.T) {
	kb := newTestKB()
	tools := kb.Tools()
	if len(tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(tools))
	}
	td := tools[0]
	if td.Name != "query_consensus" {
		t.Errorf("Name = %q, want query_consensus", td.Name)
	}
	if td.RiskLevel != 0 {
		t.Errorf("RiskLevel = %d, want 0", td.RiskLevel)
	}
	if td.Targeting {
		t.Errorf("query_consensus Targeting = true, want false (control-plane)")
	}
	if !strings.Contains(string(td.InputSchema), "topic") {
		t.Errorf("InputSchema missing 'topic': %s", td.InputSchema)
	}
}

func TestKnowledgeBase_Hit(t *testing.T) {
	kb := newTestKB()
	res, err := kb.Execute(context.Background(), "query_consensus", json.RawMessage(`{"topic":"yum-repo"}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError = true, want false; content=%q", res.Content)
	}
	if !strings.Contains(res.Content, "YUM 源配置") {
		t.Errorf("Content missing title: %q", res.Content)
	}
	if !strings.Contains(res.Content, "http://mirrors.dev/") {
		t.Errorf("Content missing body: %q", res.Content)
	}
}

func TestKnowledgeBase_Miss(t *testing.T) {
	kb := newTestKB()
	res, err := kb.Execute(context.Background(), "query_consensus", json.RawMessage(`{"topic":"nosuch"}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false, want true for unknown topic")
	}
	// 错误内容应列出可用 id，方便 Agent 重试
	for _, want := range []string{"yum-repo", "backup-policy"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("Miss content missing available id %q: %s", want, res.Content)
		}
	}
}

func TestKnowledgeBase_EmptyTopic(t *testing.T) {
	kb := newTestKB()
	res, err := kb.Execute(context.Background(), "query_consensus", json.RawMessage(`{"topic":""}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Errorf("empty topic should be error")
	}
}

func TestKnowledgeBase_NilConsensus(t *testing.T) {
	res, err := NewKnowledgeBase(nil).Execute(context.Background(), "query_consensus", json.RawMessage(`{"topic":"x"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestKnowledgeBase_UnknownTool(t *testing.T) {
	kb := newTestKB()
	_, err := kb.Execute(context.Background(), "not_a_tool", json.RawMessage(`{}`), nil)
	if err == nil {
		t.Errorf("expected error for unknown tool name")
	}
}
