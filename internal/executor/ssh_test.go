package executor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"gengar-pro/internal/host"
)

// TOFU 库隔离：重定向到系统临时目录并清空，绝不触碰真实
// ~/.gengar-pro/known_hosts（测试服务器主机密钥每次运行都变，
// 写进真实库会既污染又导致后续误报 mismatch）。
var testKnownHosts = filepath.Join(os.TempDir(), "gengar-test-known-hosts")

func init() {
	SetKnownHostsPath(testKnownHosts)
	_ = os.Remove(testKnownHosts) // 每次测试运行从空库开始
}

const testUser, testPass = "opuser", "oppass"

// TestKnownHostsPathEnvOverride —— 路径解析优先级：
// SetKnownHostsPath 显式重定向 > GENGAR_KNOWN_HOSTS env > 默认 home。
// env 档是 e2e 的 headless 子进程隔离通道（见 knownhostsPath 注释）。
func TestKnownHostsPathEnvOverride(t *testing.T) {
	orig := knownHostsFile
	defer SetKnownHostsPath(orig) // 还原 init() 的测试隔离重定向

	SetKnownHostsPath("")
	envPath := filepath.Join(t.TempDir(), "kh-env")
	t.Setenv("GENGAR_KNOWN_HOSTS", envPath)
	got, err := knownHostsPath()
	if err != nil {
		t.Fatalf("knownHostsPath: %v", err)
	}
	if got != envPath {
		t.Fatalf("env override: want %q, got %q", envPath, got)
	}

	// 显式重定向必须压过 env。
	explicit := filepath.Join(t.TempDir(), "kh-explicit")
	SetKnownHostsPath(explicit)
	if got, _ = knownHostsPath(); got != explicit {
		t.Fatalf("explicit override: want %q, got %q", explicit, got)
	}
}

// hostFromAddr 把 newTestServer 返回的 "127.0.0.1:port" 拆成 Host 的
// Addr/Port（DialClient 会经 net.JoinHostPort 重新拼接）。
func hostFromAddr(t *testing.T, addr, id string) *host.Host {
	t.Helper()
	hostPart, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr %q: %v", addr, err)
	}
	port, _ := strconv.Atoi(portStr)
	return &host.Host{ID: id, Name: id, Addr: hostPart, Port: port, User: testUser,
		Auth: host.AuthConfig{Method: host.AuthPassword, PasswordRef: id + "-pass"}}
}

func newSSHExecutor(t *testing.T) (Executor, *host.Host) {
	t.Helper()
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "t1")
	secrets := host.MemoryStore{"t1-pass": testPass}
	ex, err := DialHost(h, secrets, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return ex, h
}

func TestSSHRunShell(t *testing.T) {
	ex, _ := newSSHExecutor(t)
	out, err := ex.RunShell(context.Background(), "systemctl status nginx")
	if err != nil || !strings.Contains(string(out), "RAN: systemctl status nginx") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestSSHRunArgvQuotesArgs(t *testing.T) {
	// argv 参数含 shell 元字符时必须安全引用，远端不得展开。
	// 断言带引号形态：若实现漏引用，回显里不会出现 'a;rm -rf /'
	// （无引号的裸串不满足此断言）。
	ex, _ := newSSHExecutor(t)
	out, err := ex.RunArgv(context.Background(), "echo", "a;rm -rf /")
	if err != nil || !strings.Contains(string(out), `'a;rm -rf /'`) {
		t.Fatalf("argv must be quoted verbatim: out=%q err=%v", out, err)
	}
}

func TestSSHAuthFailure(t *testing.T) {
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "t2")
	_, err := DialHost(h, host.MemoryStore{"t2-pass": "wrong"}, 5*time.Second)
	if err == nil {
		t.Fatal("expected auth error")
	}
}

func TestSSHWriteFile(t *testing.T) {
	ex, _ := newSSHExecutor(t)
	// 相对路径落在测试进程 CWD（假服务器的 SFTP 服务于本机文件系统）
	name := "sftp-write-test-" + time.Now().Format("150405.000000000") + ".txt"
	defer os.Remove(name)
	if err := ex.WriteFile(context.Background(), name, []byte("hello sftp")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(name)
	if err != nil || string(raw) != "hello sftp" {
		t.Fatalf("file content %q err=%v", raw, err)
	}
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("mode %v: want regular file", fi.Mode())
	}
	if runtime.GOOS == "windows" {
		// Windows 的 Perm() 只反映只读位（可写文件恒 0666），无法断言
		// 0644；Chmod 后回读成功即覆盖写路径未坏。POSIX 权限断言留给
		// 05-T3 对真实 Linux 主机的集成测试。
		return
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("perm = %o, want 644", fi.Mode().Perm())
	}
}

func TestSSHFetchFile(t *testing.T) {
	ex, _ := newSSHExecutor(t)
	// 相对路径落在测试进程 CWD（假服务器的 SFTP 服务于本机文件系统）
	name := "sftp-fetch-test-" + time.Now().Format("150405.000000000") + ".txt"
	defer os.Remove(name)
	content := []byte("REDIS0012-sftp-fixture")
	if err := ex.WriteFile(context.Background(), name, content); err != nil {
		t.Fatalf("write: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "fetched.txt")
	if err := ex.FetchFile(context.Background(), name, dst); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("fetched content %q err=%v, want %q", got, err, content)
	}
}

func TestSSHFetchFileCancelInterrupts(t *testing.T) {
	// SFTP 读挂在网络上，fixture 无法像 __block__ 那样挂起远端读——
	// 改用预取消的 ctx + 大文件：看门狗（ctx.Done → 关 SFTP client）
	// 与首个请求并发触发，传输必被中断，不得傻等或成功返回。
	ex, _ := newSSHExecutor(t)
	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, bytes.Repeat([]byte{0xA5}, 64<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 进入前已取消：看门狗立刻关 client
	start := time.Now()
	err := ex.FetchFile(ctx, src, filepath.Join(t.TempDir(), "out.bin"))
	if err == nil {
		t.Fatal("expected error when ctx already cancelled")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("interrupt took %v, want < 3s", elapsed)
	}
}

func TestSSHHostKeyMismatchRejected(t *testing.T) {
	// 先连一次让 TOFU 记录真实指纹；再把 known_hosts 中该地址的记录
	// 整条替换为伪造密钥（同格式、不同 key）——重连时 knownhosts 会找到
	// 该地址的记录但密钥不匹配（KeyError.Want 非空），必须拒绝（防 MITM）。
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "t3")
	secrets := host.MemoryStore{"t3-pass": testPass}
	if _, err := DialHost(h, secrets, 5*time.Second); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	_, forgedPriv, _ := ed25519.GenerateKey(rand.Reader)
	forgedKey, err := ssh.NewPublicKey(forgedPriv.Public())
	if err != nil {
		t.Fatalf("forge key: %v", err)
	}
	path, err := knownHostsPath()
	if err != nil {
		t.Fatalf("known_hosts path: %v", err)
	}
	forgedLine := knownhosts.Line([]string{hostAddr(h)}, forgedKey)
	if err := os.WriteFile(path, []byte(forgedLine+"\n"), 0o600); err != nil {
		t.Fatalf("tamper known_hosts: %v", err)
	}
	_, err = DialHost(h, secrets, 5*time.Second)
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("expected ErrHostKeyMismatch on redial, got: %v", err)
	}
}

func TestSSHRedialWithKnownHost(t *testing.T) {
	// 首连 TOFU 记录指纹；重连走 known 条目匹配路径（非空 Want +
	// 密钥相等 → 放行）——此前该路径零覆盖
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "t4")
	secrets := host.MemoryStore{"t4-pass": testPass}
	if _, err := DialHost(h, secrets, 5*time.Second); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	ex, err := DialHost(h, secrets, 5*time.Second)
	if err != nil {
		t.Fatalf("redial with recorded host key: %v", err)
	}
	out, err := ex.RunShell(context.Background(), "echo again")
	if err != nil || !strings.Contains(string(out), "RAN: echo again") {
		t.Fatalf("redial exec out=%q err=%v", out, err)
	}
}

func TestSSHZeroTimeout(t *testing.T) {
	// timeout 零值 = 不设兜底超时（与 Local 同语义）：无 deadline 的
	// ctx 下普通命令必须照常完成；若误用 WithTimeout(0) 会立即过期，
	// 每个命令必败
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "t6")
	ex, err := DialHost(h, host.MemoryStore{"t6-pass": testPass}, 0)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	out, err := ex.RunArgv(context.Background(), "echo", "ok")
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestSSHCancelInterrupts(t *testing.T) {
	// 远端命令长跑（fixture 魔法命令 __block__ 睡 30s）时 ctx 到期
	// 必须中断等待（看门狗关 session），不得傻等到命令自然结束
	ex, _ := newSSHExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ex.RunShell(ctx, "__block__")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error when ctx expires mid-command")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("interrupt took %v, want < 3s", elapsed)
	}
}

func TestSSHHandshakeDeadline(t *testing.T) {
	// 只接受 TCP 但永不握手的服务器：ssh.Dial 的 Timeout 只覆盖 TCP 段，
	// kex/认证阶段若无 deadline 拨号会永久阻塞
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		var held []net.Conn // 持有连接（防 GC 关闭），永不发送任何字节
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	h := hostFromAddr(t, ln.Addr().String(), "t5")
	start := time.Now()
	_, err = DialHost(h, host.MemoryStore{"t5-pass": testPass}, 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected dial error against non-handshaking server")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("handshake deadline took %v, want < 3s", elapsed)
	}
}

func TestQuoteArg(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "''"},                       // 空串 → 一对空单引号
		{"-rf", "'-rf'"},                 // 选项前缀原样保留
		{"it's", `'it'\''s'`},            // 内嵌单引号 → '\'' 转义
		{`a\b`, `'a\b'`},                 // 反斜杠在单引号内是字面量
		{"line\nbreak", "'line\nbreak'"}, // 换行符原样传递
		{"a;rm -rf /", `'a;rm -rf /'`},   // 元字符整串隔离
	}
	for _, tc := range tests {
		if got := quoteArg(tc.in); got != tc.want {
			t.Errorf("quoteArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got, want := quoteArgv("echo"), `'echo'`; got != want {
		t.Errorf("quoteArgv(echo) = %q, want %q", got, want)
	}
	if got, want := quoteArgv("echo", "a b"), `'echo' 'a b'`; got != want {
		t.Errorf("quoteArgv(echo, a b) = %q, want %q", got, want)
	}
}
