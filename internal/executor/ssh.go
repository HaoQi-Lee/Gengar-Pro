package executor

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"gengar-pro/internal/host"
)

// DialHost 按主机的 AuthConfig 建立一条 SSH 连接并返回绑定该连接的
// Executor。认证材料从 SecretStore 解引用；密钥路径支持 ~ 前缀。
func DialHost(h *host.Host, secrets host.SecretStore, timeout time.Duration) (Executor, error) {
	client, err := DialClient(h, secrets, timeout)
	if err != nil {
		return nil, err
	}
	return &SSH{client: client, timeout: timeout}, nil
}

// DialHostOnce 一次性连接：同 DialHost 但不进连接管理器缓存，返回
// 执行器与关闭函数——表单内测试用未保存参数拨号，用后即弃。
func DialHostOnce(h *host.Host, secrets host.SecretStore, timeout time.Duration) (Executor, func(), error) {
	client, err := DialClient(h, secrets, timeout)
	if err != nil {
		return nil, nil, err
	}
	return &SSH{client: client, timeout: timeout}, func() { _ = client.Close() }, nil
}

// DialClient 按主机的 AuthConfig 建立一条 SSH 连接。拨号认证的唯一入口：
// executor（RunShell/SFTP）与 term（交互 PTY 会话）共用，认证/TOFU 行为
// 一处维护。
func DialClient(h *host.Host, secrets host.SecretStore, timeout time.Duration) (*ssh.Client, error) {
	var authMethods []ssh.AuthMethod
	switch h.Auth.Method {
	case host.AuthPassword:
		pass, err := secrets.Get(h.Auth.PasswordRef)
		if err != nil {
			return nil, fmt.Errorf("host %s: resolve password: %w", h.Name, err)
		}
		authMethods = append(authMethods, ssh.Password(pass))
	case host.AuthKey:
		keyPath, err := expandHome(h.Auth.KeyPath)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("host %s: read key %s: %w", h.Name, keyPath, err)
		}
		var signer ssh.Signer
		if h.Auth.KeyPassphraseRef != "" {
			pass, perr := secrets.Get(h.Auth.KeyPassphraseRef)
			if perr != nil {
				return nil, fmt.Errorf("host %s: resolve key passphrase: %w", h.Name, perr)
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey(raw)
		}
		if err != nil {
			return nil, fmt.Errorf("host %s: parse key: %w", h.Name, err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	default:
		return nil, fmt.Errorf("host %s: unsupported auth method %q", h.Name, h.Auth.Method)
	}

	cc := ssh.ClientConfig{
		User:            h.User,
		Auth:            authMethods,
		HostKeyCallback: tofuHostKeyCallback(h),
		Timeout:         timeout, // TCP 拨号超时；握手期 deadline 见下
	}
	// 不用 ssh.Dial：它只给 TCP 段设超时，kex/认证阶段无期限，对接上
	// 挂起的服务器拨号会永久阻塞。这里自行接管连接生命周期，给整个
	// 握手设 deadline，成功后清除。
	addr := net.JoinHostPort(h.Addr, strconv.Itoa(h.Port))
	dialer := net.Dialer{Timeout: timeout} // Timeout<=0 = 不限
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	nc, chans, reqs, err := ssh.NewClientConn(conn, addr, &cc)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Time{}) // 握手完成，业务期由各命令的 ctx 控制
	}
	return ssh.NewClient(nc, chans, reqs), nil
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// quoteArg 把单个参数以 POSIX 单引号安全引用，保证 argv 语义
// 在远端 shell 下不被重新解释。
func quoteArg(a string) string {
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// quoteArgv 把 argv 拼接为远端可安全执行的命令串。
func quoteArgv(name string, args ...string) string {
	parts := []string{quoteArg(name)}
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// SSH 是绑定一条 *ssh.Client 的 Executor。
type SSH struct {
	client  *ssh.Client
	timeout time.Duration
}

var _ Executor = (*SSH)(nil)

func (s *SSH) RunShell(ctx context.Context, cmd string) ([]byte, error) {
	return s.run(ctx, cmd)
}

func (s *SSH) RunArgv(ctx context.Context, name string, args ...string) ([]byte, error) {
	return s.run(ctx, quoteArgv(name, args...))
}

// withTimeout 在调用方 ctx 无 deadline 时用 s.timeout 兜底；零值表示
// 不设兜底超时（由调用方 ctx 全权决定）。与 Local.withTimeout 同语义。
func (s *SSH) withTimeout(ctx context.Context) context.Context {
	if s.timeout <= 0 {
		return ctx
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx
	}
	c, cancel := context.WithTimeout(ctx, s.timeout)
	// 同 Local.withTimeout：故意不提前 cancel，timer 存活至 deadline，
	// 泄漏窗口 = timeout，可接受；到期的中断由看门狗 goroutine 执行。
	_ = cancel
	return c
}

// run 起一个 session 执行命令串，合并 stdout+stderr。
// ctx 取消时关闭 session 中断远端命令。
// 输出上限（评审 ③-6）：单命令 2MB——`cat` 一个大文件不再把进程内存
// 打爆；超限截断并标注，模型看到标注知道输出被裁。
func (s *SSH) run(ctx context.Context, cmd string) ([]byte, error) {
	ctx = s.withTimeout(ctx)
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Close()
		case <-done:
		}
	}()
	output, err := sess.CombinedOutput(cmd)
	close(done)
	if ctx.Err() != nil {
		return truncateSessionOutput(output), ctx.Err()
	}
	return truncateSessionOutput(output), err
}

// maxSessionOutput 单命令输出上限（字节）。
const maxSessionOutput = 2 << 20 // 2MB

// truncateSessionOutput 超限时保留头部并标注省略量。
func truncateSessionOutput(out []byte) []byte {
	if len(out) <= maxSessionOutput {
		return out
	}
	note := []byte(fmt.Sprintf("\n...[session output truncated, %d bytes omitted]...", len(out)-maxSessionOutput))
	out = append(out[:maxSessionOutput:maxSessionOutput], note...)
	return out
}

// WriteFile 经 SFTP 写远端文件：写后 Chmod 0644（Create 的权限受服务端
// umask 影响不保证 0644）。每次新建 SFTP 通道，简单正确优先于复用。
// ctx 取消时关闭 SFTP client 中断挂起的请求——SFTP 协议本身不看 ctx，
// 不关就会永久阻塞。
func (s *SSH) WriteFile(ctx context.Context, path string, content []byte) error {
	ctx = s.withTimeout(ctx)
	client, err := sftp.NewClient(s.client)
	if err != nil {
		return fmt.Errorf("sftp client: %w", err)
	}
	defer client.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	defer close(done) // 后注册先执行：先停看门狗，再走上面的 client.Close
	f, err := client.Create(path)
	if err != nil {
		return fmt.Errorf("sftp create %s: %w", path, err)
	}
	defer f.Close() // 兜底；幂等（显式 Close 后再关只报已关闭错，被忽略）
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("sftp write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sftp close %s: %w", path, err)
	}
	if err := client.Chmod(path, 0o644); err != nil {
		return fmt.Errorf("sftp chmod %s: %w", path, err)
	}
	return nil
}

// FetchFile 经 SFTP 流式取回远端文件到本地路径。与 WriteFile 同款看门狗：
// ctx 取消时关闭 SFTP client 中断挂起的读；远端打开失败（权限/不存在）
// 原样返回错误。
func (s *SSH) FetchFile(ctx context.Context, remotePath, localPath string) error {
	ctx = s.withTimeout(ctx)
	client, err := sftp.NewClient(s.client)
	if err != nil {
		return fmt.Errorf("sftp client: %w", err)
	}
	defer client.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	defer close(done) // 后注册先执行：先停看门狗，再走上面的 client.Close
	rf, err := client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("sftp open %s: %w", remotePath, err)
	}
	defer rf.Close()
	lf, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", localPath, err)
	}
	if _, err := io.Copy(lf, rf); err != nil {
		_ = lf.Close()
		return fmt.Errorf("fetch %s -> %s: %w", remotePath, localPath, err)
	}
	if err := lf.Close(); err != nil {
		return fmt.Errorf("close %s: %w", localPath, err)
	}
	return nil
}
