package executor

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"gengar-pro/internal/host"
)

// ErrHostKeyMismatch 表示远端主机密钥与 known_hosts 中的记录不符
// （疑似中间人攻击）。当前消费方直接展示错误文本；后续 UX 改进可用
// errors.Is 判别做修复引导。
var ErrHostKeyMismatch = errors.New("host key mismatch")

// tofuHostKeyCallback 实现信任首次使用（TOFU）：新指纹写入
// known_hosts 并放行；已有记录但不匹配则拒绝连接（防中间人）。
//
// 并发首连同一新主机的竞态：多个连接可能同时判定"无记录"并各自
// append。append 语义下同 key 产生重复行无害（knownhosts 匹配任意
// 一条即放行），alpha 接受此窗口，不加密钥。
func tofuHostKeyCallback(h *host.Host) ssh.HostKeyCallback {
	return func(_ string, remote net.Addr, key ssh.PublicKey) error {
		path, err := knownHostsPath()
		if err != nil {
			return err
		}
		verify, err := knownhosts.New(path)
		if os.IsNotExist(err) {
			return appendKnownHost(path, h, key)
		}
		if err != nil {
			return fmt.Errorf("load known_hosts: %w", err)
		}
		// remote 必须透传（不能用 nil）：x/crypto 的 knownhosts 在
		// check 内部无条件调 remote.String()，nil 会 panic。
		if err := verify(hostAddr(h), remote, key); err != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
				// Want 为空 = 该地址尚无记录指纹 → 首次使用，记录后放行
				return appendKnownHost(path, h, key)
			}
			// 双 %w：errors.Is(err, ErrHostKeyMismatch) 供 UX 判别，
			// knownhosts.KeyError 仍保留在链上供深层诊断
			return fmt.Errorf("%w for %s: %w", ErrHostKeyMismatch, h.Name, err)
		}
		return nil
	}
}

func hostAddr(h *host.Host) string {
	return net.JoinHostPort(h.Addr, strconv.Itoa(h.Port))
}

func appendKnownHost(path string, h *host.Host, key ssh.PublicKey) error {
	// 并发首连竞态下多次 append 同一 key → 重复行，无害（见上）
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("append known_hosts: %w", err)
	}
	defer f.Close()
	// Line 内部对地址做 Normalize（非 22 端口 → [host]:port），与
	// knownhosts 匹配逻辑读取的格式一致。
	line := knownhosts.Line([]string{hostAddr(h)}, key)
	if _, err := fmt.Fprintln(f, line); err != nil {
		return err
	}
	return nil
}

// knownHostsFile 显式重定向的 known_hosts 路径。产品入口（gengar-server /
// gengar-headless）在启动期设为 <data>/known_hosts；测试与集成亦可
// 重定向到临时目录（测试服务器主机密钥每次运行都变，须隔离 TOFU 库）。
var knownHostsFile string

// SetKnownHostsPath 重定向 known_hosts 文件位置。传空串恢复默认解析。
// 入口在启动期（单线程、任何拨号前）设置；测试可重定向。必须在任何
// 拨号（DialHost）之前调用：拨号过程中的 TOFU 记录/校验即读取该路径，
// 事后重定向不影响已建立的连接。
func SetKnownHostsPath(path string) { knownHostsFile = path }

// knownHostsPath 解析优先级：SetKnownHostsPath 显式重定向（产品入口
// gengar-server / gengar-headless 启动期设为 <data>/known_hosts——两个
// 入口恒走此档，env 对它们不生效；executor/integration 单测重定向到
// 临时目录）> GENGAR_KNOWN_HOSTS 环境变量（跨进程注入；e2e 曾依赖它
// 隔离 headless 子进程，现子进程自行设置同路径，此档退居兜底）> 默认
// ~/.gengar-pro/known_hosts。每次拨号现读 env，改无需重启。
func knownHostsPath() (string, error) {
	if knownHostsFile != "" {
		return knownHostsFile, nil
	}
	if p := os.Getenv("GENGAR_KNOWN_HOSTS"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gengar-pro", "known_hosts"), nil
}
