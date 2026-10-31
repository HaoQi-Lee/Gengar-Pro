package plugin

import (
	"context"
	"encoding/json"
	"fmt"
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
	if !strings.Contains(res.Content, "args is required") {
		t.Errorf("Content = %q, want args-is-required guard message", res.Content)
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

const discoverPgrepOut = `1234 /usr/bin/redis-server 127.0.0.1:6380
1300 /usr/bin/redis-server *:26379 [sentinel]
`

const discoverSsOut = `State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      511    0.0.0.0:6380         0.0.0.0:*            users:(("redis-server",pid=1234,fd=6))
LISTEN 0      511    0.0.0.0:26379        0.0.0.0:*            users:(("redis-server",pid=1300,fd=6))
LISTEN 0      128    0.0.0.0:22           0.0.0.0:*            users:(("sshd",pid=1,fd=3))
`

func discoverExec(t *testing.T) *fakeExec {
	t.Helper()
	return &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "pgrep":
			if strings.Join(args, " ") != "-af redis-server" {
				t.Errorf("pgrep args = %v", args)
			}
			return []byte(discoverPgrepOut), nil
		case "ss":
			return []byte(discoverSsOut), nil
		case "redis-cli":
			port := args[3]
			rest := strings.Join(args[4:], " ")
			switch {
			case port == "6380" && rest == "PING":
				return []byte("PONG\n"), nil
			case port == "6380" && strings.HasPrefix(rest, "INFO"):
				return []byte("# Server\r\nredis_version:7.2.4\r\nredis_mode:standalone\r\n"), nil
			case port == "26379" && rest == "PING":
				return []byte("PONG\n"), nil
			case port == "26379" && strings.HasPrefix(rest, "INFO"):
				return []byte("# Server\r\nredis_version:7.2.4\r\nredis_mode:sentinel\r\n"), nil
			}
			return []byte("unexpected"), fmt.Errorf("unexpected redis-cli %v", args)
		}
		return nil, fmt.Errorf("unexpected cmd %s %v", name, args)
	}}
}

func TestRedisDiscover(t *testing.T) {
	res, err := NewRedisPlugin().Execute(context.Background(), "redis_discover", nil, discoverExec(t))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{
		":6380  redis 7.2.4  standalone  auth=no",
		":26379  redis 7.2.4  sentinel  auth=no",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q in:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, ":22") {
		t.Errorf("sshd port leaked into result:\n%s", res.Content)
	}
}

func TestRedisDiscoverNoauthMarked(t *testing.T) {
	exec := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "pgrep":
			return []byte("1234 /usr/bin/redis-server *:6379\n"), nil
		case "ss":
			return []byte("LISTEN 0 511 0.0.0.0:6379 0.0.0.0:* users:((\"redis-server\",pid=1234,fd=6))\n"), nil
		case "redis-cli":
			return []byte("NOAUTH Authentication required.\n"), nil
		}
		return nil, fmt.Errorf("unexpected %s", name)
	}}
	res, _ := NewRedisPlugin().Execute(context.Background(), "redis_discover", nil, exec)
	if !strings.Contains(res.Content, ":6379") || !strings.Contains(res.Content, "需密码") {
		t.Errorf("NOAUTH not marked:\n%s", res.Content)
	}
}

func TestRedisDiscoverNothing(t *testing.T) {
	exec := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, fmt.Errorf("exit status 1") // pgrep 无匹配
		}
		if name == "ss" {
			return []byte("State Recv-Q\n"), nil
		}
		return nil, fmt.Errorf("unexpected %s", name)
	}}
	res, _ := NewRedisPlugin().Execute(context.Background(), "redis_discover", nil, exec)
	if res.IsError {
		t.Errorf("empty discovery should not be an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "未发现") {
		t.Errorf("Content = %q", res.Content)
	}
}

func TestRedisListenPortsParsesSs(t *testing.T) {
	ports := redisListenPorts(discoverSsOut)
	if len(ports) != 2 || ports[0] != 6380 || ports[1] != 26379 {
		t.Errorf("ports = %v, want [6380 26379]", ports)
	}
}

func TestPortFromCmdline(t *testing.T) {
	cases := map[string]int{
		"1234 /usr/bin/redis-server 127.0.0.1:6380":        6380,
		"1300 /usr/bin/redis-server *:26379 [sentinel]":    26379,
		"1500 /usr/bin/redis-server --port 6400":           6400,
		"1600 /usr/bin/redis-server /etc/redis/redis.conf": 0, // 无端口信息
	}
	for line, want := range cases {
		got, ok := portFromCmdline(line)
		if want == 0 {
			if ok {
				t.Errorf("portFromCmdline(%q) = %d, want none", line, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("portFromCmdline(%q) = %d/%v, want %d", line, got, ok, want)
		}
	}
}
