//go:build integration

// Package integration 针对 Docker 里的真实 sshd 验证 SSHExecutor 与
// Registry fan-out 的端到端行为（不经 LLM，确定性断言）。
//
// 运行：go test -tags integration ./internal/integration/ -v
package integration

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gengar-pro/internal/appcore"
	"gengar-pro/internal/executor"
	"gengar-pro/internal/host"
	"gengar-pro/internal/plugin"
)

const itUser, itPass = "root", "gengar-it-pass"

// startSSHd 构建并启动 sshd 容器，返回映射到本机的端口。
func startSSHd(t *testing.T) int {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	// CLI 在而守护进程没起（Docker Desktop 未运行）也算不可用：
	// SKIP 而非 FAIL，与"无 Docker 则 Skip"的约定一致。
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("docker", "build", "-t", "gengar-it-sshd", "-f", filepath.Join(dir, "sshd.Dockerfile"), dir)
	id := run("docker", "run", "-d", "-P", "gengar-it-sshd")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	portLine := run("docker", "port", id, "22")
	// 形如 0.0.0.0:32771 或 [::]:32771（可能多行，取第一行的端口）
	_, portStr, _ := net.SplitHostPort(lastField(portLine))
	dialReady(t, net.JoinHostPort("127.0.0.1", portStr))
	port, _ := strconv.Atoi(portStr)
	return port
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

func itSecrets() host.SecretStore {
	return host.MemoryStore{"it-pass": itPass}
}

// redirectKnownHosts 把 TOFU 库隔离到临时目录（容器主机密钥每次运行都变）。
func redirectKnownHosts(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	executor.SetKnownHostsPath(path)
}

func TestSSHExecutorAgainstContainer(t *testing.T) {
	port := startSSHd(t)
	redirectKnownHosts(t)
	h := &host.Host{ID: "it1", Name: "it1", Addr: "127.0.0.1", Port: port, User: itUser,
		Auth: host.AuthConfig{Method: host.AuthPassword, PasswordRef: "it-pass"}}
	ex, err := executor.DialHost(h, itSecrets(), 15*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	out, err := ex.RunShell(context.Background(), "echo hello-gengar")
	if err != nil || !strings.Contains(string(out), "hello-gengar") {
		t.Fatalf("RunShell out=%q err=%v", out, err)
	}

	// 引用的 argv 在真实 shell 下逐字保留（单机版假服务器测不到这一层）
	out, err = ex.RunArgv(context.Background(), "echo", "a; danger", "$(x)", "'q'")
	if err != nil || !strings.Contains(string(out), `a; danger $(x) 'q'`) {
		t.Fatalf("RunArgv quoting out=%q err=%v", out, err)
	}

	if err := ex.WriteFile(context.Background(), "/tmp/gengar-it.txt", []byte("written")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	out, err = ex.RunShell(context.Background(), "cat /tmp/gengar-it.txt")
	if err != nil || strings.TrimSpace(string(out)) != "written" {
		t.Fatalf("read back out=%q err=%v", out, err)
	}

	// POSIX 权限断言（Windows 本地测不了的，这里补上——03-T3 审查移交项）
	out, err = ex.RunShell(context.Background(), "stat -c %a /tmp/gengar-it.txt")
	if err != nil || strings.TrimSpace(string(out)) != "644" {
		t.Fatalf("WriteFile must chmod 0644: out=%q err=%v", out, err)
	}
}

func TestRegistryFanOutOverSSH(t *testing.T) {
	port := startSSHd(t)
	redirectKnownHosts(t)

	core, err := appcore.OpenAt(t.TempDir(), itSecrets(), 15*time.Second)
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	defer core.Close()
	// 两台"逻辑主机"指向同一容器，验证 fan-out 聚合
	auth := host.AuthConfig{Method: host.AuthPassword, PasswordRef: "it-pass"}
	_ = core.Store().Save(&host.Host{ID: "w1", Name: "web-it-1", Addr: "127.0.0.1", Port: port, User: itUser, Auth: auth, Tags: []string{"webit"}})
	_ = core.Store().Save(&host.Host{ID: "w2", Name: "web-it-2", Addr: "127.0.0.1", Port: port, User: itUser, Auth: auth, Tags: []string{"webit"}})

	reg := plugin.NewRegistry()
	reg.Register(plugin.NewShellExecutor())
	reg.SetResolver(core)

	res, err := reg.Execute(context.Background(), "execute_command",
		[]byte(`{"command":"echo fanout-ok","hosts":["webit"]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{"===== web-it-1 =====", "===== web-it-2 =====", "fanout-ok"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing %q in:\n%s", want, res.Content)
		}
	}
	if res.IsError || len(res.Hosts) != 2 {
		t.Fatalf("res=%+v", res)
	}
}

// TestCoreNamesResolveSymmetryOverSSH —— 04-T5 推迟的真连接对称性：
// Names 与 Resolve 对相同 refs 产出相同主机名集合（承重不变量的实测）。
func TestCoreNamesResolveSymmetryOverSSH(t *testing.T) {
	port := startSSHd(t)
	redirectKnownHosts(t)

	core, err := appcore.OpenAt(t.TempDir(), itSecrets(), 15*time.Second)
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	defer core.Close()
	auth := host.AuthConfig{Method: host.AuthPassword, PasswordRef: "it-pass"}
	_ = core.Store().Save(&host.Host{ID: "w1", Name: "web-it-1", Addr: "127.0.0.1", Port: port, User: itUser, Auth: auth, Tags: []string{"webit"}})
	_ = core.Store().Save(&host.Host{ID: "w2", Name: "db-it-1", Addr: "127.0.0.1", Port: port, User: itUser, Auth: auth, Tags: []string{"dbit"}})
	core.SetPinned([]string{"w1"})

	for _, refs := range [][]string{{"webit"}, {"all"}, {"web-it-1", "dbit"}, nil} {
		names, _ := core.Names(refs)
		targets, _, err := core.Resolve(refs)
		if err != nil {
			t.Fatalf("refs=%v resolve: %v", refs, err)
		}
		var rnames []string
		for _, tg := range targets {
			rnames = append(rnames, tg.Name)
		}
		if strings.Join(names, ",") != strings.Join(rnames, ",") {
			t.Fatalf("refs=%v asymmetry: Names=%v Resolve=%v", refs, names, rnames)
		}
	}
}
