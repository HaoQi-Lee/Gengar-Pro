//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"gengar-pro/internal/audit"
)

func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "case.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCase(t *testing.T) {
	path := writeTempYAML(t, `
name: E02-fanout
description: 双机 fan-out
llm: stub
hosts: 2
tags:
  0: [web]
  1: [web]
script:
  - tool_calls:
      - {id: t1, name: execute_command, input: {command: "echo hi", hosts: [all]}}
  - final_text: 两台完成
inputs:
  - 在所有机器上执行 echo hi
assertions:
  - {type: tool_called, spec: {tool: execute_command, min_count: 1}}
  - {type: audit_hosts, spec: {tool: execute_command, count: 2}}
  - {type: response_contains, spec: {matches: hi}}
`)
	c, err := loadCase(path)
	if err != nil {
		t.Fatalf("loadCase: %v", err)
	}
	if c.Name != "E02-fanout" || c.LLM != "stub" || c.Hosts != 2 {
		t.Fatalf("header: %+v", c)
	}
	if got := c.Tags[0]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("tags[0]: %v", c.Tags[0])
	}
	if len(c.Script) != 2 {
		t.Fatalf("script turns: %d", len(c.Script))
	}
	tc := c.Script[0].ToolCalls[0]
	if tc.ID != "t1" || tc.Name != "execute_command" || tc.Input["command"] != "echo hi" {
		t.Fatalf("tool call: %+v", tc)
	}
	if hosts, ok := tc.Input["hosts"].([]any); !ok || hosts[0] != "all" {
		t.Fatalf("tool input hosts: %#v", tc.Input["hosts"])
	}
	if c.Script[1].FinalText != "两台完成" {
		t.Fatalf("final text: %q", c.Script[1].FinalText)
	}
	if len(c.Inputs) != 1 || len(c.Assertions) != 3 {
		t.Fatalf("inputs/assertions: %d/%d", len(c.Inputs), len(c.Assertions))
	}
	if c.Assertions[1].Spec.Count != 2 {
		t.Fatalf("audit_hosts count: %+v", c.Assertions[1].Spec)
	}
	// runs 缺省归一为单轮（顶层 script/inputs）；auto_yes 缺省 nil = true。
	if len(c.Runs) != 1 || len(c.Runs[0].Script) != 2 || len(c.Runs[0].Inputs) != 1 {
		t.Fatalf("normalized runs: %+v", c.Runs)
	}
	if c.AutoYes != nil {
		t.Fatalf("auto_yes absent must stay nil, got %v", *c.AutoYes)
	}
}

// TestLoadCaseRunsAndAutoYes：多轮（跨会话）形态与 --yes 开关直读。
func TestLoadCaseRunsAndAutoYes(t *testing.T) {
	path := writeTempYAML(t, `
name: E05
llm: stub
hosts: 1
auto_yes: false
runs:
  - script:
      - tool_calls: [{id: w1, name: memory_write, input: {host: e2e-host-1, category: system_info, data: {anchor: e2e-anchor-7c4d91}}}]
      - final_text: 已写入
    inputs: [记住锚点]
  - script:
      - tool_calls: [{id: r1, name: memory_read, input: {host: e2e-host-1, category: system_info}}]
      - final_text: 已读回
    inputs: [读取记忆]
assertions:
  - {type: memory_contains, spec: {host: e2e-host-1, matches: e2e-anchor-7c4d91}}
`)
	c, err := loadCase(path)
	if err != nil {
		t.Fatalf("loadCase: %v", err)
	}
	if len(c.Runs) != 2 {
		t.Fatalf("runs: %d", len(c.Runs))
	}
	if w := c.Runs[0].Script[0].ToolCalls[0]; w.Name != "memory_write" || len(c.Runs[0].Inputs) != 1 {
		t.Fatalf("run 1: %+v", w)
	}
	if r := c.Runs[1].Script[0].ToolCalls[0]; r.Name != "memory_read" {
		t.Fatalf("run 2: %+v", r)
	}
	if c.AutoYes == nil || *c.AutoYes {
		t.Fatalf("auto_yes: false want explicit false, got %v", c.AutoYes)
	}
}

func TestLoadCaseDefaults(t *testing.T) {
	path := writeTempYAML(t, `
name: R01
llm: real
hosts: 1
inputs: [报告系统信息]
assertions: [{type: tool_called, spec: {tool: system_info}}]
`)
	c, err := loadCase(path)
	if err != nil {
		t.Fatalf("loadCase: %v", err)
	}
	// llm=real 无 script；min_count 缺省 1。
	if len(c.Runs) != 1 || len(c.Runs[0].Script) != 0 {
		t.Fatalf("real case must not need script")
	}
	if c.Assertions[0].Spec.MinCount != 1 {
		t.Fatalf("min_count default want 1, got %d", c.Assertions[0].Spec.MinCount)
	}
}

func TestLoadCaseRejectsBadLLM(t *testing.T) {
	path := writeTempYAML(t, "name: x\nllm: banana\nhosts: 1\n")
	if _, err := loadCase(path); err == nil {
		t.Fatal("want error for llm=banana")
	}
}

// TestLoadCaseRejectsStubRunWithoutScript：llm=stub 时每轮都必须有脚本。
func TestLoadCaseRejectsStubRunWithoutScript(t *testing.T) {
	path := writeTempYAML(t, `
name: x
llm: stub
hosts: 1
script:
  - final_text: ok
runs:
  - inputs: [a]
assertions: []
`)
	if _, err := loadCase(path); err == nil {
		t.Fatal("want error for empty run script under llm=stub")
	}
}

// TestLoadCaseRejectsBadPattern：坏正则与缺字段必须在加载期失败（fail
// fast），不能等到跑完一轮才死在断言上。
func TestLoadCaseRejectsBadPattern(t *testing.T) {
	bad := []string{
		"name: x\nllm: stub\nhosts: 1\nscript: [{final_text: ok}]\n" +
			"assertions: [{type: response_contains, spec: {matches: \"a(b\"}}]\n",
		"name: x\nllm: stub\nhosts: 1\nscript: [{final_text: ok}]\n" +
			"assertions: [{type: response_not_contains, spec: {matches: \"[z-a]\"}}]\n",
		"name: x\nllm: stub\nhosts: 1\nscript: [{final_text: ok}]\n" +
			"assertions: [{type: memory_contains, spec: {matches: anchor}}]\n",           // 缺 host
		"name: x\nllm: stub\nhosts: 1\nscript: [{final_text: ok}]\n" +
			"assertions: [{type: audit_host_names, spec: {tool: execute_command}}]\n", // 缺 names
	}
	for i, yaml := range bad {
		if _, err := loadCase(writeTempYAML(t, yaml)); err == nil {
			t.Fatalf("case %d: want load error", i)
		}
	}
}

func entries(es ...audit.Entry) []audit.Entry { return es }

func TestEvalAssertion(t *testing.T) {
	stdout := "结果：web-1 ok\nweb-2 ok\n"
	aud := entries(
		audit.Entry{Tool: "execute_command", Hosts: []string{"e2e-host-1", "e2e-host-2"}, Approved: true},
		audit.Entry{Tool: "system_info", Hosts: []string{"e2e-host-1"}, Approved: true, IsError: true},
		audit.Entry{Tool: "write_file", Hosts: []string{"e2e-host-1"}, Approved: false, IsError: true},
	)
	cases := []struct {
		name    string
		a       assertionSpec
		stdout  string
		entries []audit.Entry
		wantErr bool
	}{
		{"tool_called 命中", assertionSpec{Type: "tool_called", Spec: specBody{Tool: "execute_command"}}, stdout, aud, false},
		{"tool_called 缺失", assertionSpec{Type: "tool_called", Spec: specBody{Tool: "read_file"}}, stdout, aud, true},
		{"tool_called min_count 不满足", assertionSpec{Type: "tool_called", Spec: specBody{Tool: "execute_command", MinCount: 2}}, stdout, aud, true},
		{"tool_not_called 未调用过", assertionSpec{Type: "tool_not_called", Spec: specBody{Tool: "rm"}}, stdout, aud, false},
		{"tool_not_called 调用过", assertionSpec{Type: "tool_not_called", Spec: specBody{Tool: "system_info"}}, stdout, aud, true},
		{"response_contains 子串", assertionSpec{Type: "response_contains", Spec: specBody{Matches: "web-1 ok"}}, stdout, aud, false},
		{"response_contains 正则", assertionSpec{Type: "response_contains", Spec: specBody{Matches: `web-\d ok`}}, stdout, aud, false},
		{"response_contains 未命中", assertionSpec{Type: "response_contains", Spec: specBody{Matches: "web-3"}}, stdout, aud, true},
		{"response_not_contains 干净", assertionSpec{Type: "response_not_contains", Spec: specBody{Matches: "forbidden"}}, stdout, aud, false},
		{"response_not_contains 出现", assertionSpec{Type: "response_not_contains", Spec: specBody{Matches: "web-2"}}, stdout, aud, true},
		{"audit_hosts 数量匹配", assertionSpec{Type: "audit_hosts", Spec: specBody{Tool: "execute_command", Count: 2}}, stdout, aud, false},
		{"audit_hosts 数量不符", assertionSpec{Type: "audit_hosts", Spec: specBody{Tool: "execute_command", Count: 1}}, stdout, aud, true},
		{"audit_hosts 工具无记录", assertionSpec{Type: "audit_hosts", Spec: specBody{Tool: "ping", Count: 2}}, stdout, aud, true},
		{"audit_hosts 空审计", assertionSpec{Type: "audit_hosts", Spec: specBody{Tool: "execute_command", Count: 2}}, stdout, nil, true},
		{"audit_host_names 集合相等", assertionSpec{Type: "audit_host_names", Spec: specBody{Tool: "execute_command", Names: []string{"e2e-host-1", "e2e-host-2"}}}, stdout, aud, false},
		{"audit_host_names 序无关", assertionSpec{Type: "audit_host_names", Spec: specBody{Tool: "execute_command", Names: []string{"e2e-host-2", "e2e-host-1"}}}, stdout, aud, false},
		{"audit_host_names 多出主机", assertionSpec{Type: "audit_host_names", Spec: specBody{Tool: "execute_command", Names: []string{"e2e-host-1"}}}, stdout, aud, true},
		{"audit_host_names 夹带外来名", assertionSpec{Type: "audit_host_names", Spec: specBody{Tool: "execute_command", Names: []string{"e2e-host-1", "e2e-host-2", "ghost"}}}, stdout, aud, true},
		{"audit_host_names 重复不算集合相等", assertionSpec{Type: "audit_host_names", Spec: specBody{Tool: "execute_command", Names: []string{"e2e-host-1", "e2e-host-1"}}}, stdout, aud, true},
		{"audit_host_names 工具无记录", assertionSpec{Type: "audit_host_names", Spec: specBody{Tool: "ping", Names: []string{"e2e-host-1"}}}, stdout, aud, true},
		{"audit_host_names 空审计", assertionSpec{Type: "audit_host_names", Spec: specBody{Tool: "execute_command", Names: []string{"e2e-host-1"}}}, stdout, nil, true},
		{"audit_rejected 命中", assertionSpec{Type: "audit_rejected", Spec: specBody{Tool: "write_file"}}, stdout, aud, false},
		{"audit_rejected 全部已批准", assertionSpec{Type: "audit_rejected", Spec: specBody{Tool: "execute_command"}}, stdout, aud, true},
		{"audit_rejected 空审计", assertionSpec{Type: "audit_rejected", Spec: specBody{Tool: "execute_command"}}, stdout, nil, true},
		{"未知断言类型", assertionSpec{Type: "banana"}, stdout, aud, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evalAssertion(tc.a, tc.stdout, tc.entries, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("evalAssertion(%+v) err=%v wantErr=%v", tc.a, err, tc.wantErr)
			}
		})
	}
}

// TestEvalMemoryContains：memory_contains 直查 dataDir/memory/<host>.json
// 原始文件（E05 跨会话落盘的真实证据，桩的 final_text 只是脚本回放）。
func TestEvalMemoryContains(t *testing.T) {
	dataDir := t.TempDir()
	memDir := filepath.Join(dataDir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(memDir, "e2e-host-1.json")
	body := `{"system_info":{"updated_at":"2026-10-22T00:00:00Z","data":{"anchor":"e2e-anchor-7c4d91"}}}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		host    string
		matches string
		wantErr bool
	}{
		{"命中锚点", "e2e-host-1", "e2e-anchor-7c4d91", false},
		{"正则命中", "e2e-host-1", `e2e-anchor-[0-9a-f]+`, false},
		{"内容不含", "e2e-host-1", "no-such-anchor", true},
		{"文件不存在（write 未落盘）", "e2e-host-2", "anchor", true},
		{"主机名清洗防路径逃逸", "../evil", "anchor", true}, // 清洗后读 ../_evil.json，不存在 → 报错而非逃逸
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := assertionSpec{Type: "memory_contains", Spec: specBody{Host: tc.host, Matches: tc.matches}}
			err := evalAssertion(a, "", nil, dataDir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("evalAssertion memory_contains err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestScriptToTurns —— YAML script → 桩 Turn 的转换（含默认 id 补齐）。
func TestScriptToTurns(t *testing.T) {
	turns := scriptToTurns([]scriptTurn{
		{ToolCalls: []scriptToolCall{{Name: "system_info", Input: map[string]any{}}}},
		{FinalText: "done"},
		{FinalText: "被掐断的半份报告", StopReason: "max_tokens"},
	})
	if len(turns) != 3 {
		t.Fatalf("turns: %d", len(turns))
	}
	if len(turns[0].ToolCalls) != 1 || turns[0].ToolCalls[0].ID == "" {
		t.Fatalf("tool id not defaulted: %+v", turns[0])
	}
	if turns[1].FinalText != "done" || turns[1].StopReason != "" {
		t.Fatalf("final: %+v", turns[1])
	}
	if turns[2].StopReason != "max_tokens" {
		t.Fatalf("stop_reason not passed through: %+v", turns[2])
	}
}
