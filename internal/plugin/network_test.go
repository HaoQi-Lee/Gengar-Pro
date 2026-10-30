package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestNetworkPlugin_Tools(t *testing.T) {
	np := NewNetworkPlugin()
	tools := np.Tools()
	if len(tools) != 3 {
		t.Fatalf("Tools() count = %d, want 3", len(tools))
	}
	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.Name] = true
		if tool.RiskLevel != 0 {
			t.Errorf("tool %s RiskLevel = %d, want 0 (Safe)", tool.Name, tool.RiskLevel)
		}
		if !tool.Targeting {
			t.Errorf("tool %s Targeting = false, want true", tool.Name)
		}
	}
	for _, want := range []string{"ping", "dns_lookup", "http_check"} {
		if !got[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
}

func TestNetworkPlugin_Ping(t *testing.T) {
	np := NewNetworkPlugin()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("64 bytes from 1.1.1.1: icmp_seq=1 ttl=58 time=1.20 ms"), nil
	}}
	result, err := np.Execute(context.Background(), "ping", json.RawMessage(`{"host":"1.1.1.1","count":2}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "ping" {
		t.Errorf("captured name = %q, want ping", capturedName)
	}
	// args should include "-c", "2", and the host as separate argv entries
	if !hasArgvPair(capturedArgs, "-c", "2") {
		t.Errorf("expected '-c' '2' pair in args, got %v", capturedArgs)
	}
	if capturedArgs[len(capturedArgs)-1] != "1.1.1.1" {
		t.Errorf("last arg = %q, want 1.1.1.1", capturedArgs[len(capturedArgs)-1])
	}
}

func TestNetworkPlugin_PingDefaultCount(t *testing.T) {
	np := NewNetworkPlugin()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := np.Execute(context.Background(), "ping", json.RawMessage(`{"host":"example.com"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !hasArgvPair(capturedArgs, "-c", "4") {
		t.Errorf("default count should produce '-c' '4' pair, got %v", capturedArgs)
	}
}

func TestNetworkPlugin_PingInjectionSafe(t *testing.T) {
	// 关键：host 包含 shell 元字符也不能被解释
	np := NewNetworkPlugin()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := np.Execute(context.Background(), "ping", json.RawMessage(`{"host":"evil.com; rm -rf /","count":1}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// 整个恶意字符串必须作为单个 argv 元素出现，不能拆分
	found := false
	for _, a := range capturedArgs {
		if a == "evil.com; rm -rf /" {
			found = true
		}
	}
	if !found {
		t.Errorf("malicious host should be passed as single argv element verbatim; got %v", capturedArgs)
	}
}

func TestNetworkPlugin_DnsLookup(t *testing.T) {
	np := NewNetworkPlugin()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("1.2.3.4\n"), nil
	}}
	result, err := np.Execute(context.Background(), "dns_lookup", json.RawMessage(`{"host":"example.com","type":"A"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "dig" {
		t.Errorf("captured name = %q, want dig", capturedName)
	}
	if !argvContains(capturedArgs, "+short") {
		t.Errorf("expected '+short' as a literal arg, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "example.com") {
		t.Errorf("expected host as a literal arg, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "A") {
		t.Errorf("expected record type 'A' as a literal arg, got %v", capturedArgs)
	}
}

func TestNetworkPlugin_DnsLookupDefaultType(t *testing.T) {
	np := NewNetworkPlugin()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := np.Execute(context.Background(), "dns_lookup", json.RawMessage(`{"host":"example.com"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !argvContains(capturedArgs, "A") {
		t.Errorf("default type should appear as literal 'A' arg, got %v", capturedArgs)
	}
}

func TestNetworkPlugin_HttpCheck(t *testing.T) {
	np := NewNetworkPlugin()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("200 0.123\n"), nil
	}}
	result, err := np.Execute(context.Background(), "http_check", json.RawMessage(`{"url":"https://example.com","timeout":3}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "curl" {
		t.Errorf("captured name = %q, want curl", capturedName)
	}
	if !hasArgvPair(capturedArgs, "--max-time", "3") {
		t.Errorf("expected '--max-time' '3' pair in args, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "https://example.com") {
		t.Errorf("expected url as literal arg, got %v", capturedArgs)
	}
}

func TestNetworkPlugin_HttpCheckDefaultTimeout(t *testing.T) {
	np := NewNetworkPlugin()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := np.Execute(context.Background(), "http_check", json.RawMessage(`{"url":"https://example.com"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !hasArgvPair(capturedArgs, "--max-time", "5") {
		t.Errorf("default timeout should produce '--max-time' '5' pair, got %v", capturedArgs)
	}
}

func TestNetworkPlugin_ExecError(t *testing.T) {
	np := NewNetworkPlugin()
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("network unreachable"), fmt.Errorf("exit status 1")
	}}
	result, err := np.Execute(context.Background(), "ping", json.RawMessage(`{"host":"unreachable.local"}`), fake)
	if err != nil {
		t.Fatalf("Execute should not return Go error: %v", err)
	}
	if !result.IsError {
		t.Error("IsError should be true for failed command")
	}
}

func TestNetworkPlugin_UnknownTool(t *testing.T) {
	np := NewNetworkPlugin()
	_, err := np.Execute(context.Background(), "traceroute", json.RawMessage(`{}`), &fakeExec{})
	if err == nil {
		t.Error("should return error for unknown tool")
	}
}

// hasArgvPair reports whether args contains the flag immediately followed by value.
func hasArgvPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// argvContains reports whether args contains target as a complete element.
func argvContains(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}
