package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"gengar-pro/internal/rdbparser"
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
	// ss 与 pgrep 都报 6380：去重后应只输出一行
	if n := strings.Count(res.Content, ":6380"); n != 1 {
		t.Errorf("端口 6380 应去重为一行，实际出现 %d 次:\n%s", n, res.Content)
	}
}

func TestRedisDiscoverUnreachable(t *testing.T) {
	exec := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "pgrep":
			return []byte("1234 /usr/bin/redis-server *:6379\n"), nil
		case "ss":
			return []byte("State Recv-Q\n"), nil
		case "redis-cli":
			// connection refused：输出为空 + 非零退出
			return nil, fmt.Errorf("exit status 1")
		}
		return nil, fmt.Errorf("unexpected %s", name)
	}}
	res, _ := NewRedisPlugin().Execute(context.Background(), "redis_discover", nil, exec)
	if res.IsError {
		t.Fatalf("unreachable instance should not be a tool error: %s", res.Content)
	}
	if !strings.Contains(res.Content, ":6379") || !strings.Contains(res.Content, "[无响应]") {
		t.Errorf("dead port not marked 无响应:\n%s", res.Content)
	}
}

func TestRedisDiscoverProcessOnly(t *testing.T) {
	exec := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "pgrep":
			return []byte("1600 /usr/bin/redis-server /etc/redis/redis.conf\n"), nil
		case "ss":
			// 只有 sshd 监听，无任何 redis 端口
			return []byte("LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:((\"sshd\",pid=1,fd=3))\n"), nil
		}
		return nil, fmt.Errorf("unexpected %s", name)
	}}
	res, _ := NewRedisPlugin().Execute(context.Background(), "redis_discover", nil, exec)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "未解析到监听端口") {
		t.Errorf("missing 未解析到监听端口:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "/etc/redis/redis.conf") {
		t.Errorf("missing process line:\n%s", res.Content)
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
		"1600 /usr/bin/redis-server /etc/redis/redis.conf": 0,    // 无端口信息
		"1700 /usr/bin/redis-server ::1":                   0,    // IPv6 字面量，防御跳过
		"1800 /usr/bin/redis-server [::]:6400":             6400, // 合法 IPv6 带端口形态仍解析
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

// TestConfigGetValueWarningMixed: -a 密码警告混进输出时按键名定位仍能取值；
// 键名定位失败（如 NOAUTH 错误文本 exit-0 混入）必须返回空串而非兜底取
// 最后一行——让 detectRdbPath 走「请显式传 path」降级，而非拼出垃圾路径。
func TestConfigGetValueWarningMixed(t *testing.T) {
	if got := configGetValue("Warning: Using a password with '-a'...\ndir\n/var/lib/redis\n", "dir"); got != "/var/lib/redis" {
		t.Errorf("configGetValue(警告+dir) = %q, want /var/lib/redis", got)
	}
	if got := configGetValue("NOAUTH Authentication required.\n", "dir"); got != "" {
		t.Errorf("configGetValue(NOAUTH) = %q, want 空串（键名定位失败不再兜底）", got)
	}
}

func rdbExec(t *testing.T, statOut string, fetched *string) *fakeExec {
	t.Helper()
	return &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "redis-cli":
				joined := strings.Join(args, " ")
				switch {
				case strings.HasSuffix(joined, "CONFIG GET dir"):
					return []byte("dir\n/var/lib/redis\n"), nil
				case strings.HasSuffix(joined, "CONFIG GET dbfilename"):
					return []byte("dbfilename\ndump.rdb\n"), nil
				}
				return nil, fmt.Errorf("unexpected redis-cli %v", args)
			case "stat":
				if args[len(args)-1] == "/var/lib/redis/dump.rdb" {
					return []byte(statOut), nil
				}
				return nil, fmt.Errorf("unexpected stat %v", args)
			}
			return nil, fmt.Errorf("unexpected cmd %s %v", name, args)
		},
		fetchFn: func(_ context.Context, remotePath, localPath string) error {
			if remotePath != "/var/lib/redis/dump.rdb" {
				return fmt.Errorf("unexpected remote path %s", remotePath)
			}
			if fetched != nil {
				*fetched = localPath
			}
			return os.WriteFile(localPath, []byte("fake-rdb-bytes"), 0o644)
		},
	}
}

func stubAnalyze(path string, topN int) (*rdbparser.Report, error) {
	return &rdbparser.Report{
		RDBVersion: 11, RedisVersion: "7.2.4",
		TotalKeys: 6, EstBytes: 851000000,
		DBs:   map[int]rdbparser.DBStat{0: {Keys: 6, EstBytes: 851000000}},
		Types: map[string]int64{"string": 2, "hash": 1, "list": 1, "sortedset": 1, "set": 1},
		NoTTL: 5, HasTTL: 1,
		PrefixesByBytes: []rdbparser.PrefixStat{{Prefix: "session:user", Keys: 2, EstBytes: 432}},
		PrefixesByKeys:  []rdbparser.PrefixStat{{Prefix: "session:user", Keys: 2, EstBytes: 432}},
		BigKeys:         []rdbparser.KeyStat{{Key: "user:rank", Type: "zset", EstBytes: 860}},
	}, nil
}

func TestRedisRdbAutoDetectPath(t *testing.T) {
	var tmp string
	rp := &RedisPlugin{analyze: stubAnalyze}
	res, err := rp.Execute(context.Background(), "redis_rdb",
		json.RawMessage(`{"port":6379}`), rdbExec(t, "119537664 1761763200\n", &tmp))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{
		"=== rdb: /var/lib/redis/dump.rdb",
		"总键数 6",
		"session:user",
		"永久键(无TTL): 83%",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q in:\n%s", want, res.Content)
		}
	}
	// 临时文件必须已清理（defer os.Remove）
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("temp file %s not cleaned up (err=%v)", tmp, err)
	}
}

func TestRedisRdbExplicitPath(t *testing.T) {
	var analyzed string
	rp := &RedisPlugin{analyze: func(path string, topN int) (*rdbparser.Report, error) {
		analyzed = path
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("analyze path %s not fetched: %v", path, err)
		}
		return &rdbparser.Report{TotalKeys: 0}, nil
	}}
	var fetchedRemote string
	exec := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "stat" {
				return []byte("10 0\n"), nil
			}
			return nil, fmt.Errorf("unexpected %s %v（显式 path 时不应调用 redis-cli）", name, args)
		},
		fetchFn: func(_ context.Context, remotePath, localPath string) error {
			fetchedRemote = remotePath
			return os.WriteFile(localPath, []byte("x"), 0o644)
		},
	}
	res, _ := rp.Execute(context.Background(), "redis_rdb",
		json.RawMessage(`{"path":"/data/dump.rdb"}`), exec)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if fetchedRemote != "/data/dump.rdb" {
		t.Errorf("fetched %q, want /data/dump.rdb", fetchedRemote)
	}
	if analyzed == "" {
		t.Error("analyze not called")
	}
}

func TestRedisRdbSizeGuard(t *testing.T) {
	rp := &RedisPlugin{analyze: stubAnalyze}
	var fetched bool
	exec := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "stat" {
				return []byte(fmt.Sprintf("%d 0\n", 2<<30)), nil // 2GB
			}
			return nil, fmt.Errorf("unexpected %s %v", name, args)
		},
		fetchFn: func(_ context.Context, remotePath, localPath string) error {
			fetched = true
			return nil
		},
	}
	res, _ := rp.Execute(context.Background(), "redis_rdb",
		json.RawMessage(`{"path":"/data/dump.rdb"}`), exec)
	if !res.IsError {
		t.Fatal("oversized rdb should error")
	}
	if fetched {
		t.Error("must not fetch when over limit")
	}
	if !strings.Contains(res.Content, "force") {
		t.Errorf("error should mention force: %q", res.Content)
	}
}

func TestRedisRdbForceOverridesGuard(t *testing.T) {
	rp := &RedisPlugin{analyze: stubAnalyze}
	exec := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "stat" {
				return []byte(fmt.Sprintf("%d 0\n", 2<<30)), nil
			}
			return nil, fmt.Errorf("unexpected %s %v", name, args)
		},
		fetchFn: func(_ context.Context, remotePath, localPath string) error {
			return os.WriteFile(localPath, []byte("x"), 0o644)
		},
	}
	res, _ := rp.Execute(context.Background(), "redis_rdb",
		json.RawMessage(`{"path":"/data/dump.rdb","force":true}`), exec)
	if res.IsError {
		t.Fatalf("force should allow: %s", res.Content)
	}
}

func TestRedisRdbDetectFailsSuggestsPath(t *testing.T) {
	rp := &RedisPlugin{analyze: stubAnalyze}
	exec := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "redis-cli" {
				return []byte("Could not connect to Redis at 127.0.0.1:6379: Connection refused"),
					fmt.Errorf("exit status 1")
			}
			return nil, fmt.Errorf("unexpected %s %v", name, args)
		},
	}
	res, _ := rp.Execute(context.Background(), "redis_rdb",
		json.RawMessage(`{}`), exec)
	if !res.IsError {
		t.Fatal("dead instance without path should error")
	}
	if !strings.Contains(res.Content, "path 参数") {
		t.Errorf("error should suggest explicit path: %q", res.Content)
	}
}
