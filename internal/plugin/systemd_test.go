package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSystemdManager_Tools(t *testing.T) {
	sm := NewSystemdManager()
	tools := sm.Tools()
	if len(tools) != 5 {
		t.Fatalf("Tools() count = %d, want 5", len(tools))
	}
	for _, td := range tools {
		if !td.Targeting {
			t.Errorf("%s Targeting = false, want true", td.Name)
		}
	}
}

func TestSystemdStatusRunsOnExecutor(t *testing.T) {
	sm := NewSystemdManager()
	var gotName string
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("● nginx.service - active (running)"), nil
	}}
	res, err := sm.Execute(context.Background(), "systemctl_status", json.RawMessage(`{"service":"nginx"}`), fake)
	if err != nil || res.IsError || gotName != "systemctl" || strings.Join(gotArgs, " ") != "status nginx --no-pager" {
		t.Fatalf("cmd=%s %v res=%+v err=%v", gotName, gotArgs, res, err)
	}
}

func TestSystemdManager_Restart(t *testing.T) {
	sm := NewSystemdManager()
	var gotName string
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte(""), nil
	}}
	_, err := sm.Execute(context.Background(), "systemctl_restart", json.RawMessage(`{"service":"nginx"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotName != "systemctl" || strings.Join(gotArgs, " ") != "restart nginx" {
		t.Errorf("cmd = %s %q, want 'systemctl restart nginx'", gotName, strings.Join(gotArgs, " "))
	}
}

func TestSystemdManager_Journalctl(t *testing.T) {
	sm := NewSystemdManager()
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("May 28 10:00:01 server nginx[123]: started"), nil
	}}
	result, err := sm.Execute(context.Background(), "journalctl_logs", json.RawMessage(`{"service":"nginx","lines":50}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if join := strings.Join(gotArgs, " "); join != "-u nginx --no-pager -n 50" {
		t.Errorf("args = %q, want '-u nginx --no-pager -n 50'", join)
	}
}

func TestSystemdManager_JournalctlNegativeLinesClamped(t *testing.T) {
	// 评审 ①-4 参数健壮性：负数 lines 回落默认 50
	sm := NewSystemdManager()
	var gotArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(""), nil
	}}
	_, _ = sm.Execute(context.Background(), "journalctl_logs", json.RawMessage(`{"service":"nginx","lines":-1}`), fake)
	if join := strings.Join(gotArgs, " "); join != "-u nginx --no-pager -n 50" {
		t.Errorf("args = %q, want '-u nginx --no-pager -n 50' (negative clamped)", join)
	}
}

// TestSystemdManager_InjectionBecomesArgvError: 评审 ①-4 注入回归——
// service 字段夹带 `; reboot` 旧实现 Sprintf 拼 shell 免确认执行任意命令；
// argv 化后它只是 systemctl 的一个字面参数。
func TestSystemdManager_InjectionBecomesArgvError(t *testing.T) {
	sm := NewSystemdManager()
	var shellCalled bool
	var gotArgs []string
	fake := &fakeExec{
		argvFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			gotArgs = args
			return []byte(""), nil
		},
		shellFn: func(context.Context, string) ([]byte, error) {
			shellCalled = true
			return []byte(""), nil
		},
	}
	_, _ = sm.Execute(context.Background(), "systemctl_status", json.RawMessage(`{"service":"nginx; reboot"}`), fake)
	if shellCalled {
		t.Fatal("systemctl_status must never go through RunShell")
	}
	found := false
	for _, a := range gotArgs {
		if a == "nginx; reboot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected service must appear verbatim as one argv: %v", gotArgs)
	}
}

func TestSystemdManager_UnknownTool(t *testing.T) {
	sm := NewSystemdManager()
	_, err := sm.Execute(context.Background(), "systemctl_unknown", json.RawMessage(`{}`), &fakeExec{})
	if err == nil {
		t.Error("should return error for unknown tool")
	}
}
