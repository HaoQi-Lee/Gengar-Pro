//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sshd 容器夹具：与 internal/integration 共用同一镜像源（其 sshd.Dockerfile
// 内置 root:gengar-it-pass 账号）。E2E 的目标机 = 真实 SSH 真实执行。
//
// Docker 不可用（CLI 缺失或守护进程未起）→ t.Skip，与 integration 套件
// 的约定一致——本机常态即 Skip，用例只在有 Docker 的环境全量运行。

const (
	e2eUser    = "root"
	e2ePass    = "gengar-it-pass" // 与 internal/integration/sshd.Dockerfile 一致
	e2ePassRef = "e2e-pass"       // --secrets 引用名（seedHosts 的 PasswordRef）
)

// HostSpec 描述一台就绪的 sshd 目标机。CID 是容器 ID（docker run 的
// stdout）：SSH 之外还需要容器内操作时（startRedisHost 的 docker exec）
// 用它定位容器——端口映射只够 SSH 拨号，够不着 exec。
type HostSpec struct {
	Name string
	Addr string
	Port int
	CID  string
}

// dockerCheck 验证 Docker 可用：CLI 在且守护进程可达（Docker Desktop
// 未运行也算不可用）。不可用 → Skip。
func dockerCheck(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
}

// buildSSHdImage 构建本套件的 sshd 镜像（tag gengar-e2e-sshd）。每批
// 主机只构建一次；跨用例重建命中 Docker 层缓存，近乎零成本。
func buildSSHdImage(t *testing.T) string {
	t.Helper()
	dockerCheck(t)
	integrationDir, err := filepath.Abs(filepath.Join(repoRoot(t), "internal", "integration"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "build", "-t", "gengar-e2e-sshd", "-f",
		filepath.Join(integrationDir, "sshd.Dockerfile"), integrationDir).CombinedOutput()
	if err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	return "gengar-e2e-sshd"
}

// runSSHd 启动一个 sshd 容器（镜像须已构建），返回容器 ID 与映射到本机
// 的端口。ID 供 HostSpec.CID（docker exec 等容器内操作），port 供 SSH。
func runSSHd(t *testing.T, image string) (cid string, port int) {
	t.Helper()
	out, err := exec.Command("docker", "run", "-d", "-P", image).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	portOut, portErr := exec.Command("docker", "port", id, "22").CombinedOutput()
	if portErr != nil {
		t.Fatalf("docker port: %v\n%s", portErr, portOut)
	}
	// docker port 输出形如 0.0.0.0:32771 或 [::]:32771，可能多行（IPv4/IPv6
	// 各一行）；取全输出最后一个字段——任一行都是同一映射端口的 host:port。
	_, portStr, _ := net.SplitHostPort(lastField(string(portOut)))
	dialReady(t, net.JoinHostPort("127.0.0.1", portStr))
	port, _ = strconv.Atoi(portStr)
	return id, port
}

// startHosts 起 n 台独立 sshd 容器（e2e-host-1..n）：镜像只构建一次，
// 循环内只做 run / port / dialReady，供 cases.go 的 hosts 字段驱动。
func startHosts(t *testing.T, n int) []HostSpec {
	t.Helper()
	image := buildSSHdImage(t)
	specs := make([]HostSpec, 0, n)
	for i := 1; i <= n; i++ {
		cid, port := runSSHd(t, image)
		specs = append(specs, HostSpec{
			Name: fmt.Sprintf("e2e-host-%d", i), Addr: "127.0.0.1", Port: port, CID: cid,
		})
	}
	return specs
}

// startRedisHost 在指定 e2e 主机容器内安装并启动 redis、造 6 键并 SAVE，
// 返回容器内 dump 路径（redis_rdb 用例依赖，YAML 侧硬编码同一常量）。
// 6 键与 rdbparser 夹具同构：2 SET（其一 EX 3600）+ HMSET + RPUSH +
// ZADD + SADD——string 2 / hash 1 / list 1 / zset 1 / set 1、NoTTL 5 +
// HasTTL 1，rdbparser 报告的「总键数 6」正是用例断言点。
// 实现走 docker exec（容器是 alpine:3.20，apk 装的 redis 是 7.2.9，
// RDB v11 与 rdbparser testdata/dump-redis7.2 同版）。redis-server
// daemonize 后脱离 exec 会话存活（reparent 到 PID 1 的 sshd）；set -e
// 保证任一步失败（如 daemon 未起）整个夹具立刻报错而不是静默半成品。
func startRedisHost(t *testing.T, h HostSpec) string {
	t.Helper()
	script := `set -e
apk add -q redis
redis-server --daemonize yes --port 6379 --dir /tmp --save ""
sleep 1
redis-cli SET session:user:1001 alice
redis-cli SET session:user:1002 bob EX 3600
redis-cli HMSET cache:user:1 f1 v1 f2 v2
redis-cli RPUSH queue:job a b c
redis-cli ZADD rank 1 a 2 b
redis-cli SADD tags x y
redis-cli SAVE`
	if out, err := exec.Command("docker", "exec", h.CID, "sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("docker exec redis setup on %s: %v\n%s", h.Name, err, out)
	}
	return "/tmp/dump.rdb"
}

func lastField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func dialReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("sshd not ready at %s", addr)
}
