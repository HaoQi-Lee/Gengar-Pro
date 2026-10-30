package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestShellExecutor_Tools(t *testing.T) {
	se := NewShellExecutor()
	tools := se.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() count = %d, want 1", len(tools))
	}
	if tools[0].Name != "execute_command" {
		t.Errorf("Tool name = %q, want execute_command", tools[0].Name)
	}
	if !tools[0].Targeting {
		t.Errorf("execute_command Targeting = false, want true")
	}
}

func TestShellExecuteSuccess(t *testing.T) {
	se := NewShellExecutor()
	fake := &fakeExec{shellFn: func(_ context.Context, cmd string) ([]byte, error) {
		return []byte("ok: " + cmd), nil
	}}
	res, err := se.Execute(context.Background(), "execute_command",
		json.RawMessage(`{"command":"echo hi"}`), fake)
	if err != nil || res.IsError || res.Content != "ok: echo hi" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestShellExecuteCommandError(t *testing.T) {
	se := NewShellExecutor()
	fake := &fakeExec{shellFn: func(_ context.Context, cmd string) ([]byte, error) {
		return []byte("boom"), errors.New("exit 1")
	}}
	res, _ := se.Execute(context.Background(), "execute_command",
		json.RawMessage(`{"command":"bad"}`), fake)
	if !res.IsError {
		t.Fatal("IsError must be true on command failure")
	}
	if res.Content == "" {
		t.Error("failure output should be preserved in Content")
	}
}

func TestShellExecuteUnknownTool(t *testing.T) {
	se := NewShellExecutor()
	_, err := se.Execute(context.Background(), "unknown_tool",
		json.RawMessage(`{}`), &fakeExec{})
	if err == nil {
		t.Error("Execute should return error for unknown tool name")
	}
}

// TestShellExecuteNilExecutor：目标型工具没拿到执行器不允许静默成功。
// 03-T3 起 Registry 会在 fan-out 前拒绝无 resolver 的目标型调用；在那之前
// 本测试锁定 nil exec 不会装作成功（nil 接口调用必然 panic）。
func TestShellExecuteNilExecutor(t *testing.T) {
	se := NewShellExecutor()
	defer func() {
		if recover() == nil {
			t.Fatal("nil executor must not silently succeed")
		}
	}()
	_, _ = se.Execute(context.Background(), "execute_command",
		json.RawMessage(`{"command":"x"}`), nil)
}
