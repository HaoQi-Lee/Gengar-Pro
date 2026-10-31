package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/rdbparser"
)

// RedisPlugin 提供 Redis 中间件运维工具：实例发现、命令透传、RDB 离线
// 分析。与 docker/systemd 插件同构：无状态、argv 形式执行、Targeting=true
// 按主机 fan-out。密码经 -a 传递、会出现在对话与审计中（本期明确接受，
// 见 docs/superpowers/specs/2026-10-31-redis-ops-plugin-design.md）；
// 不加 --no-auth-warning：redis-cli 3.x/5.x 不识别该 flag 会直接报错。
type RedisPlugin struct {
	// analyze 可注入替换（测试用）；NewRedisPlugin 默认 rdbparser.Analyze。
	analyze func(path string, topN int) (*rdbparser.Report, error)
}

func NewRedisPlugin() *RedisPlugin {
	return &RedisPlugin{analyze: rdbparser.Analyze}
}

func (rp *RedisPlugin) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "redis_discover",
			Description: "Discover Redis instances on target hosts: redis-server processes, listening ports, version/mode (via PING + INFO server), and whether auth is required.\nUse when: first contact with a host; locating which Redis instances run where before operating on them.\nReturns: one line per instance (port / version / mode / auth). Prefer over execute_command + pgrep/ss/redis-cli.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "redis_cli",
			Description: "Run an arbitrary redis-cli command on the target host (argv-safe, no shell).\nUse when: any Redis operation without a dedicated tool — GET/SET, CONFIG GET/SET, INFO, SLOWLOG, replication ops, client management. Note: this tool is risk-gated — every call (including read-only GET) asks for user confirmation; multiple calls in one turn share a single batch confirmation.\nInput: args = the Redis command as a word array, e.g. [\"GET\",\"foo\"] or [\"CONFIG\",\"GET\",\"maxmemory\"].\nReturns: redis-cli output verbatim.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"args":{"type":"array","items":{"type":"string"},"description":"Redis command as argv words, e.g. [\"GET\",\"foo\"]"},"port":{"type":"integer","description":"Redis port (default 6379)","default":6379},"password":{"type":"string","description":"Auth password if the instance requires one (NOAUTH)"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["args"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "redis_rdb",
			Description: "Analyze a Redis RDB file offline: fetch dump.rdb from the host over SFTP, parse it locally, report keyspace statistics.\nUse when: \"what is using memory\"; capacity review; permanent-key (no TTL) audit — zero impact on the live instance.\nReturns: key count, memory estimate (approximate), DB/type distribution, top prefixes by memory, biggest keys, TTL stats. Path defaults to dir+dbfilename from CONFIG GET of the live instance at port; files >1GB require force:true.\nPrefer over manual cat/scp of the rdb file.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"RDB file path on the target host; omit to auto-detect from the live instance"},"port":{"type":"integer","description":"Redis port for auto-detect (default 6379)","default":6379},"password":{"type":"string","description":"Auth password for auto-detect CONFIG GET"},"top_n":{"type":"integer","description":"Top prefixes / bigkeys count (default 10)","default":10},"force":{"type":"boolean","description":"Allow fetching files larger than 1GB (default false)","default":false},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
	}
}

func (rp *RedisPlugin) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	switch toolName {
	case "redis_discover":
		return rp.redisDiscover(ctx, exec)
	case "redis_cli":
		return rp.redisCli(ctx, input, exec)
	case "redis_rdb":
		return rp.redisRdb(ctx, input, exec)
	default:
		return ToolResult{}, fmt.Errorf("RedisPlugin: unknown tool %q", toolName)
	}
}

// redisExec 统一执行 + 错误透传（同 network/diagnostic 插件模式）。
func redisExec(ctx context.Context, exec executor.Executor, name string, args ...string) (ToolResult, error) {
	output, err := exec.RunArgv(ctx, name, args...)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}

// redisArgv 组装 redis-cli 参数：本地回环 + 端口 + 可选密码。
func redisArgv(port int, password string, cmd ...string) []string {
	argv := []string{"redis-cli", "-h", "127.0.0.1", "-p", strconv.Itoa(port)}
	if password != "" {
		argv = append(argv, "-a", password)
	}
	return append(argv, cmd...)
}

type redisCliInput struct {
	Args     []string `json:"args"`
	Port     int      `json:"port"`
	Password string   `json:"password"`
}

func (rp *RedisPlugin) redisCli(ctx context.Context, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	var p redisCliInput
	if err := json.Unmarshal(input, &p); err != nil {
		return ToolResult{}, fmt.Errorf("parse input: %w", err)
	}
	if len(p.Args) == 0 {
		return ToolResult{
			Content: "Error: args is required (Redis command as word array, e.g. [\"GET\",\"foo\"])",
			IsError: true,
		}, nil
	}
	if p.Port == 0 {
		p.Port = 6379
	}
	argv := redisArgv(p.Port, p.Password, p.Args...)
	return redisExec(ctx, exec, argv[0], argv[1:]...)
}

func (rp *RedisPlugin) redisDiscover(ctx context.Context, exec executor.Executor) (ToolResult, error) {
	pgrepOut, _ := exec.RunArgv(ctx, "pgrep", "-af", "redis-server")
	procLines := nonEmptyLines(pgrepOut)

	ssOut, _ := exec.RunArgv(ctx, "ss", "-tlnp")
	ports := redisListenPorts(string(ssOut))
	for _, l := range procLines {
		if p, ok := portFromCmdline(l); ok {
			ports = append(ports, p)
		}
	}
	ports = dedupPorts(ports)

	var b strings.Builder
	if len(ports) == 0 && len(procLines) == 0 {
		b.WriteString("未发现 redis-server 进程或监听端口。")
		return ToolResult{Content: b.String()}, nil
	}
	if len(ports) == 0 {
		fmt.Fprintf(&b, "发现 %d 个 redis-server 进程但未解析到监听端口（可能仅 unix socket 或权限不足）:\n", len(procLines))
		for _, l := range procLines {
			b.WriteString("  " + l + "\n")
		}
		return ToolResult{Content: b.String()}, nil
	}
	for _, port := range ports {
		describeRedisPort(ctx, exec, port, &b)
	}
	return ToolResult{Content: b.String()}, nil
}

// describeRedisPort 逐端口探测：PONG→INFO server 取版本/模式；
// NOAUTH/WRONGPASS→标注需密码；连接失败→标注无响应。
func describeRedisPort(ctx context.Context, exec executor.Executor, port int, b *strings.Builder) {
	argv := redisArgv(port, "", "PING")
	pingOut, pingErr := exec.RunArgv(ctx, argv[0], argv[1:]...)
	ping := string(pingOut)
	switch {
	case pingErr == nil && strings.TrimSpace(ping) == "PONG":
		iargv := redisArgv(port, "", "INFO", "server")
		infoOut, _ := exec.RunArgv(ctx, iargv[0], iargv[1:]...)
		ver := redisInfoField(string(infoOut), "redis_version")
		mode := redisInfoField(string(infoOut), "redis_mode")
		if ver == "" {
			ver = "?"
		}
		if mode == "" {
			mode = "?"
		}
		fmt.Fprintf(b, ":%d  redis %s  %s  auth=no\n", port, ver, mode)
	case strings.Contains(ping, "NOAUTH") || strings.Contains(ping, "WRONGPASS"):
		fmt.Fprintf(b, ":%d  [需密码] redis_cli 传 password 参数后再操作\n", port)
	case pingErr != nil && strings.TrimSpace(ping) == "":
		fmt.Fprintf(b, ":%d  [无响应] %v\n", port, pingErr)
	default:
		// 补尾换行 + TrimSpace：防多行/无换行的 ping 输出把两端口挤进一行
		fmt.Fprintf(b, ":%d  [非预期响应] %s\n", port, strings.TrimSpace(ping))
	}
}

// redisListenPorts 从 ss -tlnp 输出提取 redis-server 进程监听的端口。
func redisListenPorts(ssOut string) []int {
	seen := map[int]bool{}
	var ports []int
	for _, line := range strings.Split(ssOut, "\n") {
		if !strings.Contains(line, "LISTEN") || !strings.Contains(line, "redis-server") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		addr := fields[3] // Local Address:Port（IPv6 形如 [::]:6379）
		i := strings.LastIndex(addr, ":")
		if i < 0 {
			continue
		}
		p, err := strconv.Atoi(addr[i+1:])
		if err != nil || p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	return ports
}

// portFromCmdline 从 pgrep -af 的命令行提取端口：--port N、--port=N、
// 或位置参数形如 127.0.0.1:6380 / *:26379。第一个字段是 PID，跳过。
func portFromCmdline(line string) (int, bool) {
	fields := strings.Fields(line)
	for i, f := range fields {
		if i == 0 {
			continue
		}
		if f == "--port" && i+1 < len(fields) {
			if p, err := strconv.Atoi(fields[i+1]); err == nil && p > 0 {
				return p, true
			}
		}
		if strings.HasPrefix(f, "--port=") {
			if p, err := strconv.Atoi(strings.TrimPrefix(f, "--port=")); err == nil && p > 0 {
				return p, true
			}
		}
		if strings.Contains(f, ":") {
			i := strings.LastIndex(f, ":")
			if i > 0 && f[i-1] == ':' { // IPv6 字面量（::1 / ::），跳过
				continue
			}
			tail := f[i+1:]
			if p, err := strconv.Atoi(tail); err == nil && p > 0 && !strings.Contains(tail, "/") {
				return p, true
			}
		}
	}
	return 0, false
}

func dedupPorts(ports []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func nonEmptyLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// redisInfoField 从 INFO 输出取字段值（redis_version 等）。
func redisInfoField(info, field string) string {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, field+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, field+":"))
		}
	}
	return ""
}

// rdbFetchLimit 是 RDB 拉取的默认上限（1GB），防 SFTP 拉爆带宽与本地磁盘。
const rdbFetchLimit = 1 << 30

type redisRdbInput struct {
	Path     string `json:"path"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	TopN     int    `json:"top_n"`
	Force    bool   `json:"force"`
}

func (rp *RedisPlugin) redisRdb(ctx context.Context, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	var p redisRdbInput
	if err := json.Unmarshal(input, &p); err != nil {
		return ToolResult{}, fmt.Errorf("parse input: %w", err)
	}
	if p.Port == 0 {
		p.Port = 6379
	}
	if p.TopN == 0 {
		p.TopN = 10
	}

	rdbPath := p.Path
	if rdbPath == "" {
		var err error
		rdbPath, err = rp.detectRdbPath(ctx, exec, p.Port, p.Password)
		if err != nil {
			return ToolResult{
				Content: fmt.Sprintf("Error: %v\n（实例不在 %d 端口或不存活时，请显式传 path 参数）", err, p.Port),
				IsError: true,
			}, nil
		}
	}

	size, mtime, err := statRemoteFile(ctx, exec, rdbPath)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v", err), IsError: true}, nil
	}
	if size > rdbFetchLimit && !p.Force {
		return ToolResult{
			Content: fmt.Sprintf("Error: %s 大小 %s 超过 1GB 拉取上限；确认需要分析请加 force:true",
				rdbPath, rdbparser.HumanBytes(uint64(size))),
			IsError: true,
		}, nil
	}

	tmp, err := os.CreateTemp("", "gengar-rdb-*.rdb")
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: create temp: %v", err), IsError: true}, nil
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath) // RDB 含真实键值数据，成败都清理

	if err := exec.FetchFile(ctx, rdbPath, tmpPath); err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v", err), IsError: true}, nil
	}

	report, err := rp.analyze(tmpPath, p.TopN)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v", err), IsError: true}, nil
	}
	return ToolResult{Content: rdbparser.Render(rdbPath, size, mtime, report)}, nil
}

// detectRdbPath 从存活实例的 CONFIG GET dir + dbfilename 拼出 RDB 路径。
func (rp *RedisPlugin) detectRdbPath(ctx context.Context, exec executor.Executor, port int, password string) (string, error) {
	run := func(args ...string) (string, error) {
		argv := redisArgv(port, password, args...)
		out, err := exec.RunArgv(ctx, argv[0], argv[1:]...)
		return string(out), err
	}
	dirOut, err := run("CONFIG", "GET", "dir")
	if err != nil {
		return "", fmt.Errorf("CONFIG GET dir: %v\n%s", err, dirOut)
	}
	fileOut, err := run("CONFIG", "GET", "dbfilename")
	if err != nil {
		return "", fmt.Errorf("CONFIG GET dbfilename: %v\n%s", err, fileOut)
	}
	dir := configGetValue(dirOut, "dir")
	name := configGetValue(fileOut, "dbfilename")
	if dir == "" || name == "" {
		return "", fmt.Errorf("CONFIG GET 返回无法解析:\n%s%s", dirOut, fileOut)
	}
	// 远端是 Linux：path.Join 而非 filepath（Windows 开发机会引入反斜杠）
	return path.Join(dir, name), nil
}

// configGetValue 解析 redis-cli CONFIG GET 输出中的值。
// 输出形如 "key\nvalue\n"，但 -a 密码警告可能混进输出，故按键名定位。
func configGetValue(out, key string) string {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == key && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	// 兜底：最后一行非空内容
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

// statRemoteFile 取远端文件大小与 mtime（unix 秒）。
func statRemoteFile(ctx context.Context, exec executor.Executor, p string) (int64, int64, error) {
	out, err := exec.RunArgv(ctx, "stat", "-c", "%s %Y", p)
	if err != nil {
		return 0, 0, fmt.Errorf("stat %s: %v\n%s", p, err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("stat 输出无法解析: %q", string(out))
	}
	size, err1 := strconv.ParseInt(fields[0], 10, 64)
	mtime, err2 := strconv.ParseInt(fields[1], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("stat 输出无法解析: %q", string(out))
	}
	return size, mtime, nil
}
