package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDiagnosticKit_Tools(t *testing.T) {
	dk := NewDiagnosticKit()
	if len(dk.Tools()) != 7 {
		t.Fatalf("Tools() count = %d, want 7", len(dk.Tools()))
	}
	got := map[string]bool{}
	for _, tool := range dk.Tools() {
		got[tool.Name] = true
		if tool.RiskLevel != 0 {
			t.Errorf("%s RiskLevel = %d, want 0", tool.Name, tool.RiskLevel)
		}
		if !tool.Targeting {
			t.Errorf("%s Targeting = false, want true", tool.Name)
		}
	}
	for _, want := range []string{"check_port", "check_process", "check_disk", "check_memory", "check_network", "tail_log", "system_info"} {
		if !got[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
}

func TestDiagnosticKit_CheckPort(t *testing.T) {
	dk := NewDiagnosticKit()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("LISTEN 0 128 0.0.0.0:80 *:*"), nil
	}}
	result, err := dk.Execute(context.Background(), "check_port", json.RawMessage(`{"port":80}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "ss" {
		t.Errorf("expected ss, got %q", capturedName)
	}
	if !argvContains(capturedArgs, "-tlnp") {
		t.Errorf("expected -tlnp as literal arg, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "sport") {
		t.Errorf("expected ss filter keyword 'sport' as literal arg, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, ":80") {
		t.Errorf("expected port filter ':80' as literal arg, got %v", capturedArgs)
	}
}

func TestDiagnosticKit_CheckProcessInjectionSafe(t *testing.T) {
	// 关键：process name 含 shell 元字符必须作为 literal argv 传递
	dk := NewDiagnosticKit()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := dk.Execute(context.Background(), "check_process", json.RawMessage(`{"name":"nginx; rm -rf /"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !argvContains(capturedArgs, "nginx; rm -rf /") {
		t.Errorf("malicious name should be a single literal arg, got %v", capturedArgs)
	}
}

func TestDiagnosticKit_CheckDisk(t *testing.T) {
	dk := NewDiagnosticKit()
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("/dev/sda1  50G  20G  30G  40% /"), nil
	}}
	result, err := dk.Execute(context.Background(), "check_disk", json.RawMessage(`{}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
}

func TestDiagnosticKit_CheckMemory(t *testing.T) {
	dk := NewDiagnosticKit()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("Mem:  8G  2G  6G"), nil
	}}
	_, err := dk.Execute(context.Background(), "check_memory", json.RawMessage(`{}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedName != "free" {
		t.Errorf("expected free, got %q", capturedName)
	}
	if len(capturedArgs) == 0 || capturedArgs[0] != "-h" {
		t.Errorf("expected -h, got %v", capturedArgs)
	}
}

func TestDiagnosticKit_TailLog(t *testing.T) {
	dk := NewDiagnosticKit()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("last log line"), nil
	}}
	result, err := dk.Execute(context.Background(), "tail_log", json.RawMessage(`{"path":"/var/log/syslog","lines":20}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "tail" {
		t.Errorf("expected tail, got %q", capturedName)
	}
	if !hasArgvPair(capturedArgs, "-n", "20") {
		t.Errorf("expected '-n' '20' pair in args, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "/var/log/syslog") {
		t.Errorf("expected log path as literal arg, got %v", capturedArgs)
	}
}

func TestDiagnosticKit_SystemInfo(t *testing.T) {
	dk := NewDiagnosticKit()
	calls := []string{}
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		switch name {
		case "uname":
			return []byte("Linux host 5.10.0\n"), nil
		case "hostname":
			return []byte("my-host\n"), nil
		case "uptime":
			return []byte(" 10:00:00 up 1 day,  2:30,  1 user,  load average: 0.10, 0.05, 0.01\n"), nil
		case "cat":
			return []byte(`PRETTY_NAME="CentOS Linux 7"`), nil
		}
		return []byte(""), nil
	}}
	result, err := dk.Execute(context.Background(), "system_info", json.RawMessage(`{}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	for _, want := range []string{"=== kernel ===", "=== hostname ===", "=== uptime ===", "=== os-release ==="} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("system_info output missing section header %q; got: %s", want, result.Content)
		}
	}
	for _, want := range []string{"Linux host", "my-host", "load average", "CentOS Linux 7"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("system_info output missing %q; got: %s", want, result.Content)
		}
	}
	for _, want := range []string{"uname", "hostname", "uptime", "cat"} {
		seen := false
		for _, c := range calls {
			if c == want {
				seen = true
			}
		}
		if !seen {
			t.Errorf("system_info should invoke %s, calls = %v", want, calls)
		}
	}
}

func TestDiagnosticKit_SystemInfoResilient(t *testing.T) {
	// 即使部分子命令失败，system_info 也应返回成功 + 标注失败的部分
	dk := NewDiagnosticKit()
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "cat" {
			return []byte("os-release missing"), errors.New("exit status 1")
		}
		return []byte(name + " ok\n"), nil
	}}
	result, err := dk.Execute(context.Background(), "system_info", json.RawMessage(`{}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("system_info should tolerate partial failures, content = %s", result.Content)
	}
	if !strings.Contains(result.Content, "uname ok") {
		t.Errorf("expected uname output preserved, got: %s", result.Content)
	}
}

func TestDiagnosticKit_UnknownTool(t *testing.T) {
	dk := NewDiagnosticKit()
	_, err := dk.Execute(context.Background(), "check_unknown", json.RawMessage(`{}`), &fakeExec{})
	if err == nil {
		t.Error("should return error for unknown tool")
	}
}
