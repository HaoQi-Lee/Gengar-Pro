package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDockerManager_Tools(t *testing.T) {
	dm := NewDockerManager()
	tools := dm.Tools()
	if len(tools) != 5 {
		t.Fatalf("Tools() count = %d, want 5", len(tools))
	}
	for _, td := range tools {
		if !td.Targeting {
			t.Errorf("%s Targeting = false, want true", td.Name)
		}
	}
}

func TestDockerPsRunsOnExecutor(t *testing.T) {
	dm := NewDockerManager()
	var gotName string
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("CONTAINER ID  IMAGE"), nil
	}}
	res, err := dm.Execute(context.Background(), "docker_ps", json.RawMessage(`{}`), fake)
	if err != nil || res.IsError || gotName != "docker" || strings.Join(gotArgs, " ") != "ps" {
		t.Fatalf("cmd=%s %v res=%+v err=%v", gotName, gotArgs, res, err)
	}
}

func TestDockerManager_DockerLogs(t *testing.T) {
	dm := NewDockerManager()
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("log line 1\nlog line 2"), nil
	}}
	result, err := dm.Execute(context.Background(), "docker_logs", json.RawMessage(`{"container":"nginx","lines":50}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if join := strings.Join(gotArgs, " "); join != "logs --tail 50 -- nginx" {
		t.Errorf("args = %q, want 'logs --tail 50 -- nginx'", join)
	}
}

func TestDockerManager_DockerLogsNegativeLinesClamped(t *testing.T) {
	// 评审 ①-4 参数健壮性：负数 lines 回落默认 100，不得把负数传给 cli
	dm := NewDockerManager()
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(""), nil
	}}
	_, _ = dm.Execute(context.Background(), "docker_logs", json.RawMessage(`{"container":"nginx","lines":-5}`), fake)
	if join := strings.Join(gotArgs, " "); join != "logs --tail 100 -- nginx" {
		t.Errorf("args = %q, want 'logs --tail 100 -- nginx' (negative clamped)", join)
	}
}

func TestDockerManager_ComposeUp(t *testing.T) {
	dm := NewDockerManager()
	var gotName string
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("Creating network... done"), nil
	}}
	result, err := dm.Execute(context.Background(), "docker_compose_up", json.RawMessage(`{"path":"/opt/app"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotName != "docker" || strings.Join(gotArgs, " ") != "compose -f /opt/app up -d" {
		t.Errorf("cmd = %s %q, want 'docker compose -f /opt/app up -d'", gotName, strings.Join(gotArgs, " "))
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
}

func TestDockerManager_ComposeUpNoPath(t *testing.T) {
	// path 空 = compose 在 cwd 找默认文件（与旧 `docker-compose up -d` 语义一致）
	dm := NewDockerManager()
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(""), nil
	}}
	_, _ = dm.Execute(context.Background(), "docker_compose_up", json.RawMessage(`{}`), fake)
	if join := strings.Join(gotArgs, " "); join != "compose up -d" {
		t.Errorf("args = %q, want 'compose up -d'", join)
	}
}

// TestDockerManager_InjectionBecomesArgvError: 评审 ①-4 注入回归——
// container 字段夹带 `; reboot` 旧实现会拼进 shell 执行任意命令；argv 化
// 后它只是一个字面参数，注入内容不再被任何 shell 解释。
func TestDockerManager_InjectionBecomesArgvError(t *testing.T) {
	dm := NewDockerManager()
	var gotName string
	var gotArgs []string
	var shellCalled bool
	fake := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotName, gotArgs = name, args
			return []byte(""), nil
		},
		shellFn: func(context.Context, string) ([]byte, error) {
			shellCalled = true // argv 化后本路径不得再触发任何 shell
			return []byte(""), nil
		},
	}
	_, _ = dm.Execute(context.Background(), "docker_logs", json.RawMessage(`{"container":"nginx; reboot","lines":10}`), fake)
	if shellCalled {
		t.Fatal("docker_logs must never go through RunShell")
	}
	if gotName != "docker" {
		t.Fatalf("name = %q, want docker", gotName)
	}
	// 注入串必须原样作为最后一个参数出现，而不是被拆开
	found := false
	for _, a := range gotArgs {
		if a == "nginx; reboot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected container must appear verbatim as one argv: %v", gotArgs)
	}
}

func TestDockerManager_UnknownTool(t *testing.T) {
	dm := NewDockerManager()
	_, err := dm.Execute(context.Background(), "docker_unknown", json.RawMessage(`{}`), &fakeExec{})
	if err == nil {
		t.Error("should return error for unknown tool")
	}
}
