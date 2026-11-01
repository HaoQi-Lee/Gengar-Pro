//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	e2ePassRef = "e2e-pass"       // --secrets 引用名（SeedHosts 的 PasswordRef）
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

// 本套件的两个镜像 tag：base 与 internal/integration 共用（sshd.Dockerfile
// 保持最小、零改动）；redis 变体在 base 上叠加预装层（E08 的被测面）——
// apk 的一次性网络成本在构建期付清并进层缓存，运行期（startRedisHost）
// 零网络依赖。
const (
	e2eSSHDImage      = "gengar-e2e-sshd"
	e2eSSHDRedisImage = "gengar-e2e-sshd-redis"
)

// 镜像构建进程级缓存（同 harness.go buildHeadless 的 sync.Once 哲学）：
// 同一 go test 进程只构建一次。跨进程重建本就命中 Docker 层缓存，这里省
// 的是每次 build 的缓存校验往返（Windows Docker Desktop 上每次 ~1-2s），
// 全套件每个用例各省一笔。
var (
	sshdImageOnce sync.Once
	sshdImage     string
	sshdImageErr  error

	sshdRedisImageOnce sync.Once
	sshdRedisImage     string
	sshdRedisImageErr  error
)

// buildSSHdImage 构建本套件的 sshd 基础镜像（进程内一次）。
func buildSSHdImage(t *testing.T) string {
	t.Helper()
	dockerCheck(t)
	sshdImageOnce.Do(func() {
		integrationDir, err := filepath.Abs(filepath.Join(repoRoot(t), "internal", "integration"))
		if err != nil {
			sshdImageErr = err
			return
		}
		out, err := exec.Command("docker", "build", "-t", e2eSSHDImage, "-f",
			filepath.Join(integrationDir, "sshd.Dockerfile"), integrationDir).CombinedOutput()
		if err != nil {
			sshdImageErr = fmt.Errorf("docker build: %v\n%s", err, out)
			return
		}
		sshdImage = e2eSSHDImage
	})
	if sshdImageErr != nil {
		t.Fatalf("%v", sshdImageErr)
	}
	return sshdImage
}

// buildSSHdRedisImage 构建预装 redis 的 e2e 专用镜像，两段式保 integration
// 零改动：先建 base（buildSSHdImage，integration 的 Dockerfile 原样），
// 再 FROM base + apk add redis。叠加层 Dockerfile 经 stdin 喂给
// docker build -f -（无 COPY/ADD，上下文用空临时目录即可）；base 层缓存
// 不变时 redis 层同样命中缓存，进程内再经 sync.Once 只校验一次。
func buildSSHdRedisImage(t *testing.T) string {
	t.Helper()
	base := buildSSHdImage(t) // dockerCheck/Skip 语义在 base 侧逐调用生效
	sshdRedisImageOnce.Do(func() {
		cmd := exec.Command("docker", "build", "-t", e2eSSHDRedisImage, "-f", "-", t.TempDir())
		cmd.Stdin = strings.NewReader("FROM " + base + "\nRUN apk add --no-cache redis\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			sshdRedisImageErr = fmt.Errorf("docker build redis image: %v\n%s", err, out)
			return
		}
		sshdRedisImage = e2eSSHDRedisImage
	})
	if sshdRedisImageErr != nil {
		t.Fatalf("%v", sshdRedisImageErr)
	}
	return sshdRedisImage
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

// StartHosts 起 n 台独立 sshd 容器（e2e-host-1..n）：镜像只构建一次，
// 循环内只做 run / port / dialReady，供 cases.go 的 hosts 字段与 webapi
// 档的 W 用例驱动。（导出：internal/e2e/webapi 复用，StartStubLLM 先例。）
func StartHosts(t *testing.T, n int) []HostSpec {
	t.Helper()
	return startHostsWithImage(t, buildSSHdImage(t), n)
}

// startHostsRedis 起 n 台预装 redis 的 sshd 容器（redis: true 的用例）。
func startHostsRedis(t *testing.T, n int) []HostSpec {
	t.Helper()
	return startHostsWithImage(t, buildSSHdRedisImage(t), n)
}

// startHostsWithImage 用给定镜像起 n 台 e2e-host-1..n（两个 startHosts*
// 入口的公共体）。
func startHostsWithImage(t *testing.T, image string, n int) []HostSpec {
	t.Helper()
	specs := make([]HostSpec, 0, n)
	for i := 1; i <= n; i++ {
		cid, port := runSSHd(t, image)
		specs = append(specs, HostSpec{
			Name: fmt.Sprintf("e2e-host-%d", i), Addr: "127.0.0.1", Port: port, CID: cid,
		})
	}
	return specs
}

// redisDumpPath 是 startRedisHost 造出的 dump 的容器内路径；E08 的 YAML
// 里 redis_rdb 的显式 path 写的是同一值（YAML 引不了 Go 常量，两侧以
// 注释互指，改路径须两处同步）。
const redisDumpPath = "/tmp/dump.rdb"

// redisHostSetup 在预装好 redis 的容器（gengar-e2e-sshd-redis）内启动
// 实例、造 6 键并 SAVE。就绪探测用有界重试（50 × 0.1s 的 until
// redis-cli PING）取代固定 sleep——慢机不赌 1 秒够用，快机也不白等。
// set -e 保证任一步失败整个夹具立刻报错而不是静默半成品。
const redisHostSetup = `set -e
redis-server --daemonize yes --port 6379 --dir /tmp --save ""
i=0
until redis-cli PING 2>/dev/null | grep -q PONG; do
	i=$((i+1))
	if [ "$i" -ge 50 ]; then
		echo "redis not ready after 50 probes" >&2
		exit 1
	fi
	sleep 0.1
done
redis-cli SET session:user:1001 alice
redis-cli SET session:user:1002 bob EX 3600
redis-cli HMSET cache:user:1 f1 v1 f2 v2
redis-cli RPUSH queue:job a b c
redis-cli ZADD rank 1 a 2 b
redis-cli SADD tags x y
redis-cli SAVE`

// startRedisHost 启动指定 e2e 主机容器内预装的 redis、造 6 键并 SAVE，
// 返回容器内 dump 路径（redisDumpPath，redis_rdb 用例依赖）。6 键与
// rdbparser 夹具同构：2 SET（其一 EX 3600）+ HMSET + RPUSH + ZADD +
// SADD——string 2 / hash 1 / list 1 / zset 1 / set 1、NoTTL 5 + HasTTL 1，
// rdbparser 报告的「总键数 6」正是用例断言点（镜像预装的 redis 是
// 7.2.9 / RDB v11，与 rdbparser testdata/dump-redis7.2 同版）。
// redis-server daemonize 后脱离 exec 会话存活（reparent 到 PID 1 的
// sshd）。docker exec 有 120s 硬上限：redis 启动/造键被卡死（镜像坏、
// daemon 假活）时按超时失败，不吊 go test 的单轮 5 分钟预算。
func startRedisHost(t *testing.T, h HostSpec) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", h.CID, "sh", "-c", redisHostSetup)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker exec redis setup on %s: %v\n%s", h.Name, err, out)
	}
	return redisDumpPath
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
