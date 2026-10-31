package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"gengar-pro/internal/executor"
)

type mockPlugin struct {
	tools []ToolDefinition
	calls []string
}

func (m *mockPlugin) Tools() []ToolDefinition { return m.tools }

func (m *mockPlugin) Execute(_ context.Context, toolName string, _ json.RawMessage, _ executor.Executor) (ToolResult, error) {
	m.calls = append(m.calls, toolName)
	return ToolResult{Content: "mock result for " + toolName}, nil
}

func TestRegistry_RegisterAndTools(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockPlugin{tools: []ToolDefinition{{Name: "tool_a", Description: "Tool A", RiskLevel: 0}}})
	reg.Register(&mockPlugin{tools: []ToolDefinition{
		{Name: "tool_b", Description: "Tool B", RiskLevel: 1},
		{Name: "tool_c", Description: "Tool C", RiskLevel: 0},
	}})
	if len(reg.Tools()) != 3 {
		t.Fatalf("Tools() count = %d, want 3", len(reg.Tools()))
	}
}

func TestRegistry_Execute(t *testing.T) {
	reg := NewRegistry()
	mp := &mockPlugin{tools: []ToolDefinition{{Name: "tool_a", Description: "Tool A"}}}
	reg.Register(mp)
	result, err := reg.Execute(context.Background(), "tool_a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Content != "mock result for tool_a" {
		t.Errorf("Content = %q, want mock result", result.Content)
	}
	if len(mp.calls) != 1 || mp.calls[0] != "tool_a" {
		t.Errorf("plugin calls = %v, want [tool_a]", mp.calls)
	}
}

func TestRegistry_ExecuteUnknown(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Execute(context.Background(), "nonexistent", json.RawMessage(`{}`))
	if err == nil {
		t.Error("Execute should return error for unknown tool")
	}
}

func TestRegistry_RiskLevel(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockPlugin{tools: []ToolDefinition{
		{Name: "safe_tool", RiskLevel: 0},
		{Name: "risky_tool", RiskLevel: 1},
	}})
	if level := reg.RiskLevel("safe_tool"); level != 0 {
		t.Errorf("RiskLevel(safe_tool) = %d, want 0", level)
	}
	if level := reg.RiskLevel("risky_tool"); level != 1 {
		t.Errorf("RiskLevel(risky_tool) = %d, want 1", level)
	}
}

func TestTargetingToolSchemasDeclareHosts(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewShellExecutor())
	reg.Register(NewFileManager())
	reg.Register(NewDockerManager())
	reg.Register(NewSystemdManager())
	reg.Register(NewPackageManager())
	reg.Register(NewDiagnosticKit())
	reg.Register(NewNetworkPlugin())
	// 控制面反向断言：这些工具必须不带 Targeting/hosts（nil 构造安全，
	// Tools() 返回静态定义不解引用 nil 字段）。
	reg.Register(NewMemoryManager(nil))
	reg.Register(NewKnowledgeBase(nil))
	reg.Register(NewClarifyPlugin())
	targeting := 0
	for _, td := range reg.Tools() {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(td.InputSchema, &schema); err != nil {
			t.Fatalf("%s: bad schema json: %v", td.Name, err)
		}
		_, hasHosts := schema.Properties["hosts"]
		if td.Targeting != hasHosts {
			t.Errorf("%s: Targeting=%v but hosts in schema=%v", td.Name, td.Targeting, hasHosts)
		}
		if td.Targeting {
			targeting++
			// hosts 不得进 required
			var full struct {
				Required []string `json:"required"`
			}
			_ = json.Unmarshal(td.InputSchema, &full)
			for _, r := range full.Required {
				if r == "hosts" {
					t.Errorf("%s: hosts must not be required", td.Name)
				}
			}
		}
	}
	if targeting != 30 {
		t.Fatalf("expected 30 targeting tools, got %d", targeting)
	}
}

// fakeResolver 实现 TargetResolver：refs 为空时返回 defaultTgts。
type fakeResolver struct {
	targets     []Target
	unresolved  []string
	defaultTgts []Target
}

func (f *fakeResolver) Names(refs []string) ([]string, []string) {
	tgts := f.targets
	if len(refs) == 0 {
		tgts = f.defaultTgts
	}
	var names []string
	for _, t := range tgts {
		names = append(names, t.Name)
	}
	return names, f.unresolved
}

func (f *fakeResolver) Resolve(refs []string) ([]Target, []string, error) {
	if len(refs) == 0 {
		return f.defaultTgts, f.unresolved, nil
	}
	return f.targets, f.unresolved, nil
}

// echoPlugin 原样返回主机绑定执行器给出的内容，用来观察 fan-out 行为。
type echoPlugin struct{}

func (echoPlugin) Tools() []ToolDefinition {
	return []ToolDefinition{{Name: "echo_tool", InputSchema: json.RawMessage(`{"type":"object"}`), Targeting: true}}
}

func (echoPlugin) Execute(_ context.Context, _ string, _ json.RawMessage, exec executor.Executor) (ToolResult, error) {
	out, err := exec.RunShell(context.Background(), "whoami")
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: string(out)}, nil
}

// hostExec 是按主机名回显的执行器。
type hostExec struct{ name string }

func (h hostExec) RunShell(_ context.Context, cmd string) ([]byte, error) {
	return []byte(h.name), nil
}
func (h hostExec) RunArgv(_ context.Context, name string, _ ...string) ([]byte, error) {
	return []byte(h.name), nil
}
func (h hostExec) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }
func (h hostExec) FetchFile(_ context.Context, _, _ string) error        { return nil }

type failExec struct{}

func (failExec) RunShell(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("connection refused")
}
func (failExec) RunArgv(_ context.Context, name string, _ ...string) ([]byte, error) {
	return nil, errors.New("connection refused")
}
func (failExec) WriteFile(_ context.Context, _ string, _ []byte) error {
	return errors.New("connection refused")
}
func (failExec) FetchFile(_ context.Context, _, _ string) error {
	return errors.New("connection refused")
}

// countingExec 包装 hostExec 计数调用，用于断言「不该执行就没执行」
// （本路径 0 目标 0 goroutine，非并发，普通计数即可）。
type countingExec struct {
	hostExec
	calls int
}

func (c *countingExec) RunShell(ctx context.Context, cmd string) ([]byte, error) {
	c.calls++
	return c.hostExec.RunShell(ctx, cmd)
}

// panicPlugin 对回显为 "boom" 的主机 panic，其余主机正常——验证
// fan-out 把单主机 panic 降级为 [FAILED] 分节而非杀死进程。
type panicPlugin struct{}

func (panicPlugin) Tools() []ToolDefinition {
	return []ToolDefinition{{Name: "panic_tool", InputSchema: json.RawMessage(`{"type":"object"}`), Targeting: true}}
}

func (panicPlugin) Execute(_ context.Context, _ string, _ json.RawMessage, exec executor.Executor) (ToolResult, error) {
	out, _ := exec.RunShell(context.Background(), "whoami")
	if string(out) == "boom" {
		panic("host exploded")
	}
	return ToolResult{Content: string(out)}, nil
}

func TestFanOutSingleTargetPassthrough(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{targets: []Target{{Name: "web-01", Exec: hostExec{"web-01"}}}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["web-01"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Content != "web-01" || len(res.Hosts) != 1 || res.Hosts[0] != "web-01" {
		t.Fatalf("single target must pass through verbatim: %+v", res)
	}
}

func TestFanOutMultiHostAggregates(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{targets: []Target{
		{Name: "web-01", Exec: hostExec{"web-01"}},
		{Name: "web-02", Exec: hostExec{"web-02"}},
	}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["web"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"===== web-01 =====", "web-01", "===== web-02 =====", "web-02"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("aggregate missing %q in:\n%s", want, res.Content)
		}
	}
	if res.IsError || len(res.Hosts) != 2 {
		t.Fatalf("res=%+v", res)
	}
}

func TestFanOutPartialFailureIsNotError(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{targets: []Target{
		{Name: "ok", Exec: hostExec{"ok"}},
		{Name: "bad", Exec: failExec{}},
	}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["ok","bad"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.IsError {
		t.Fatal("partial failure must not set IsError")
	}
	if !strings.Contains(res.Content, "===== bad [FAILED] =====") {
		t.Fatalf("failed host must be marked:\n%s", res.Content)
	}
}

func TestFanOutAllFailureIsError(t *testing.T) {
	// 两个 failExec 才走多目标分支的 failed == len(results) 路径；
	// 单台只会覆盖单目标透传分支。
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{targets: []Target{
		{Name: "bad1", Exec: failExec{}},
		{Name: "bad2", Exec: failExec{}},
	}})
	res, _ := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["bad1","bad2"]}`))
	if !res.IsError {
		t.Fatal("all-failed fan-out must set IsError")
	}
	for _, name := range []string{"bad1", "bad2"} {
		if !strings.Contains(res.Content, "===== "+name+" [FAILED] =====") {
			t.Fatalf("missing failed section for %s:\n%s", name, res.Content)
		}
	}
}

func TestFanOutPluginPanicBecomesFailedSection(t *testing.T) {
	reg := NewRegistry()
	reg.Register(panicPlugin{})
	reg.SetResolver(&fakeResolver{targets: []Target{
		{Name: "ok", Exec: hostExec{"ok"}},
		{Name: "boom", Exec: hostExec{"boom"}},
	}})
	res, err := reg.Execute(context.Background(), "panic_tool", json.RawMessage(`{"hosts":["ok","boom"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.IsError {
		t.Fatal("single-host panic must not fail the whole call")
	}
	if !strings.Contains(res.Content, "===== boom [FAILED] =====") || !strings.Contains(res.Content, "plugin panic:") {
		t.Fatalf("panicked host must become a failed section:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "===== ok =====") {
		t.Fatalf("healthy host section missing:\n%s", res.Content)
	}
}

func TestFanOutMalformedHostsRejected(t *testing.T) {
	// hosts 是字符串而非数组时 unmarshal 失败、Hosts 为 nil——若静默
	// 吞掉，nil refs 会改道 resolver 默认目标（钉住机器），等于把本想
	// 发给别处的命令发到钉住的机器上。必须显式拒绝且不触达任何主机。
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	pinned := &countingExec{hostExec: hostExec{"pinned-1"}}
	reg.SetResolver(&fakeResolver{defaultTgts: []Target{{Name: "pinned-1", Exec: pinned}}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":"web-01"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid hosts") {
		t.Fatalf("malformed hosts must be rejected: %+v\n%s", res, res.Content)
	}
	if pinned.calls != 0 {
		t.Fatalf("malformed hosts must not fall through to default targets; pinned called %d times", pinned.calls)
	}
}

func TestFanOutUnresolvedRefsReported(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{targets: []Target{{Name: "ok", Exec: hostExec{"ok"}}}, unresolved: []string{"ghost"}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["ghost","ok"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(res.Content, "ghost") || !strings.Contains(res.Content, "未解析") {
		t.Fatalf("unresolved refs must surface to the model:\n%s", res.Content)
	}
}

func TestFanOutDefaultTargetsWhenHostsOmitted(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{defaultTgts: []Target{{Name: "pinned-1", Exec: hostExec{"pinned-1"}}}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{}`))
	if err != nil || len(res.Hosts) != 1 || res.Hosts[0] != "pinned-1" {
		t.Fatalf("omitted hosts must use resolver default: %+v err=%v", res, err)
	}
}

func TestFanOutTargetingWithoutResolverRejected(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	_, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["x"]}`))
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("expected resolver error, got %v", err)
	}
}

func TestFanOutNoTargetsResolved(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["x"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no target hosts resolved (no default targets set)") {
		t.Fatalf("res=%+v", res)
	}
}

func TestFanOutNoTargetsAllUnresolvedNamesRef(t *testing.T) {
	// 引用全错与「无默认目标」要区分开：全错时必须点名未解析引用。
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{unresolved: []string{"ghost"}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["ghost"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "unresolved refs: ghost") {
		t.Fatalf("res=%+v\n%s", res, res.Content)
	}
}

func TestTargetNamesExpandsForConfirm(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{targets: []Target{
		{Name: "web-01", Exec: hostExec{"web-01"}},
		{Name: "web-02", Exec: hostExec{"web-02"}},
	}})
	names := reg.TargetNames("echo_tool", json.RawMessage(`{"hosts":["web"]}`))
	if len(names) != 2 || names[0] != "web-01" || names[1] != "web-02" {
		t.Fatalf("names=%v", names)
	}
}

func TestTargetNamesBestEffortNil(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	if got := reg.TargetNames("echo_tool", json.RawMessage(`{}`)); got != nil {
		t.Fatalf("no resolver should yield nil, got %v", got)
	}
	if got := reg.TargetNames("nonexistent", json.RawMessage(`{}`)); got != nil {
		t.Fatalf("unknown tool should yield nil, got %v", got)
	}
}

func TestIsTargeting(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	if !reg.IsTargeting("echo_tool") || reg.IsTargeting("nonexistent") {
		t.Fatal("IsTargeting mismatch")
	}
}

func TestTargetNamesMalformedHostsYieldsNil(t *testing.T) {
	// hosts 为字符串而非数组时 unmarshal 失败——必须返回 nil 而非
	// 落进 Names(nil) 的默认目标：Execute 对同样输入显式拒绝，弹窗
	// 不能显示默认（钉住）主机的幽灵清单。
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	reg.SetResolver(&fakeResolver{defaultTgts: []Target{{Name: "pinned-1", Exec: hostExec{"pinned-1"}}}})
	if got := reg.TargetNames("echo_tool", json.RawMessage(`{"hosts":"web"}`)); got != nil {
		t.Fatalf("malformed hosts must yield nil, not default targets: %v", got)
	}
}

// TestAggregateTruncatesPerHost: 多目标时每主机输出按预算截断，防聚合
// 输出撑爆模型上下文；分节头保留、截断有标注。
func TestAggregateTruncatesPerHost(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	long := strings.Repeat("x", 8000)
	reg.SetResolver(&fakeResolver{targets: []Target{
		{Name: "h1", Exec: hostExec{long}},
		{Name: "h2", Exec: hostExec{long}},
	}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["h1","h2"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(res.Content) >= 16000 {
		t.Fatalf("aggregate must truncate: len=%d", len(res.Content))
	}
	for _, want := range []string{"===== h1 =====", "===== h2 =====", "[host output truncated,"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing %q", want)
		}
	}
	// 每主机上限 = max(500, 10000/2) = 5000：保留头 5000 字符。
	if !strings.Contains(res.Content, strings.Repeat("x", 5000)) {
		t.Fatal("head-biased truncation must keep the first 5000 chars")
	}
	if strings.Contains(res.Content, strings.Repeat("x", 5001)) {
		t.Fatal("per-host body must be capped at 5000 chars")
	}
	if !strings.Contains(res.Content, "3000 chars omitted") {
		t.Fatalf("omission note must state omitted count:\n%.200s", res.Content)
	}
}

// TestAggregateSingleTargetNotTruncated: 单目标保持透传语义，超长输出
// 也不截断（截断只在多目标聚合路径生效）。
func TestAggregateSingleTargetNotTruncated(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	long := strings.Repeat("y", 20000)
	reg.SetResolver(&fakeResolver{targets: []Target{{Name: "only", Exec: hostExec{long}}}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["only"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Content != long {
		t.Fatalf("single target must pass through verbatim, len=%d", len(res.Content))
	}
}

// TestAggregateTruncatesAtRuneBoundary: 截断点必须落在 rune 边界——
// 中文运维输出按字节切开会产生乱码（cap=5000 字节回退到 4998=1666 个
// 三字节汉字）。
func TestAggregateTruncatesAtRuneBoundary(t *testing.T) {
	reg := NewRegistry()
	reg.Register(echoPlugin{})
	long := strings.Repeat("中", 6000) // 18000 字节
	reg.SetResolver(&fakeResolver{targets: []Target{
		{Name: "h1", Exec: hostExec{long}},
		{Name: "h2", Exec: hostExec{long}},
	}})
	res, err := reg.Execute(context.Background(), "echo_tool", json.RawMessage(`{"hosts":["h1","h2"]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatal("truncation must not split a multi-byte rune")
	}
	// 5000 字节上限回退 2 字节 → 每主机保留 1666 个完整汉字。
	if !strings.Contains(res.Content, strings.Repeat("中", 1666)) {
		t.Fatalf("expected 1666 intact runes per host:\n%.120s", res.Content)
	}
	if strings.Contains(res.Content, strings.Repeat("中", 1667)) {
		t.Fatal("per-host cap (5000 bytes) must still hold after rune backoff")
	}
}
