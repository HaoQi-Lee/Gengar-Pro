package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedisPluginToolsRegistered(t *testing.T) {
	rp := NewRedisPlugin()
	tools := rp.Tools()
	if len(tools) != 3 {
		t.Fatalf("len(tools) = %d, want 3", len(tools))
	}
	names := map[string]bool{}
	for _, td := range tools {
		names[td.Name] = true
	}
	for _, want := range []string{"redis_discover", "redis_cli", "redis_rdb"} {
		if !names[want] {
			t.Errorf("tool %q missing", want)
		}
	}
	for _, td := range tools {
		if !td.Targeting {
			t.Errorf("tool %q should be targeting", td.Name)
		}
	}
	risk := map[string]int{}
	for _, td := range tools {
		risk[td.Name] = int(td.RiskLevel)
	}
	if risk["redis_cli"] != 1 || risk["redis_discover"] != 0 || risk["redis_rdb"] != 0 {
		t.Errorf("risk levels = %v (cli 应 Risky，其余 Safe)", risk)
	}
}

func TestRedisCliArgv(t *testing.T) {
	var gotName string
	var gotArgs []string
	exec := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("OK\n"), nil
	}}
	res, err := NewRedisPlugin().Execute(context.Background(), "redis_cli",
		json.RawMessage(`{"args":["GET","foo"],"port":6380,"password":"s3cret"}`), exec)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if res.Content != "OK\n" {
		t.Errorf("Content = %q", res.Content)
	}
	want := []string{"-h", "127.0.0.1", "-p", "6380", "-a", "s3cret", "GET", "foo"}
	if gotName != "redis-cli" || strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %s %v, want redis-cli %v", gotName, gotArgs, want)
	}
}

func TestRedisCliDefaultPortNoPassword(t *testing.T) {
	var gotArgs []string
	exec := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("3\n"), nil
	}}
	res, _ := NewRedisPlugin().Execute(context.Background(), "redis_cli",
		json.RawMessage(`{"args":["DBSIZE"]}`), exec)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if strings.Join(gotArgs, " ") != "-h 127.0.0.1 -p 6379 DBSIZE" {
		t.Errorf("argv = %v", gotArgs)
	}
}

func TestRedisCliEmptyArgs(t *testing.T) {
	res, _ := NewRedisPlugin().Execute(context.Background(), "redis_cli",
		json.RawMessage(`{"args":[]}`), &fakeExec{})
	if !res.IsError {
		t.Error("empty args should be an error result")
	}
}

func TestRedisCliExecErrorPassthrough(t *testing.T) {
	exec := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("WRONGPASS invalid username-password pair"),
			context.DeadlineExceeded
	}}
	res, _ := NewRedisPlugin().Execute(context.Background(), "redis_cli",
		json.RawMessage(`{"args":["PING"]}`), exec)
	if !res.IsError {
		t.Error("exec error should be IsError")
	}
	if !strings.Contains(res.Content, "WRONGPASS") {
		t.Errorf("Content should contain original output: %q", res.Content)
	}
}
