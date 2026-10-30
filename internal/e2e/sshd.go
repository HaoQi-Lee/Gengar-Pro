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

// HostSpec 描述一台就绪的 sshd 目标机。
type HostSpec struct {
	Name string
	Addr string
	Port int
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

// runSSHd 启动一个 sshd 容器（镜像须已构建），返回映射到本机的端口。
func runSSHd(t *testing.T, image string) int {
	t.Helper()
	out, err := exec.Command("docker", "run", "-d", "-P", image).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	portOut, err := exec.Command("docker", "port", id, "22").CombinedOutput()
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, portOut)
	}
	// docker port 输出形如 0.0.0.0:32771 或 [::]:32771，可能多行（IPv4/IPv6
	// 各一行）；取全输出最后一个字段——任一行都是同一映射端口的 host:port。
	_, portStr, _ := net.SplitHostPort(lastField(string(portOut)))
	dialReady(t, net.JoinHostPort("127.0.0.1", portStr))
	port, _ := strconv.Atoi(portStr)
	return port
}

// startHosts 起 n 台独立 sshd 容器（e2e-host-1..n）：镜像只构建一次，
// 循环内只做 run / port / dialReady，供 cases.go 的 hosts 字段驱动。
func startHosts(t *testing.T, n int) []HostSpec {
	t.Helper()
	image := buildSSHdImage(t)
	specs := make([]HostSpec, 0, n)
	for i := 1; i <= n; i++ {
		port := runSSHd(t, image)
		specs = append(specs, HostSpec{
			Name: fmt.Sprintf("e2e-host-%d", i), Addr: "127.0.0.1", Port: port,
		})
	}
	return specs
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
