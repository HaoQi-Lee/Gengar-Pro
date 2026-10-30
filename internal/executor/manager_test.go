package executor

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gengar-pro/internal/host"
)

func TestManagerCachesConnection(t *testing.T) {
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "m1")
	secrets := host.MemoryStore{"m1-pass": testPass}
	m := NewManager(secrets, 5*time.Second)

	e1, err := m.ExecutorFor(h)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	e2, err := m.ExecutorFor(h)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if e1 != e2 {
		t.Fatal("same host should reuse the cached executor")
	}
	if got := m.ActiveConns(); got != 1 {
		t.Fatalf("expected 1 active conn, got %d", got)
	}
}

func TestManagerDropsClosedClient(t *testing.T) {
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "m2")
	secrets := host.MemoryStore{"m2-pass": testPass}
	m := NewManager(secrets, 5*time.Second)

	e1, err := m.ExecutorFor(h)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	m.Close("m2") // 模拟连接死亡
	e2, err := m.ExecutorFor(h)
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	if e1 == e2 {
		t.Fatal("closed client must trigger redial")
	}
}

func TestManagerCloseAll(t *testing.T) {
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "m3")
	secrets := host.MemoryStore{"m3-pass": testPass}
	m := NewManager(secrets, 5*time.Second)
	if _, err := m.ExecutorFor(h); err != nil {
		t.Fatalf("dial: %v", err)
	}
	m.CloseAll()
	if got := m.ActiveConns(); got != 0 {
		t.Fatalf("expected 0 after CloseAll, got %d", got)
	}
}

// TestManagerRedialsDeadClient：缓存条目仍在、底层连接已死但无人显式
// Close——ExecutorFor 的探活必须发现死亡并重拨返回新实例。
func TestManagerRedialsDeadClient(t *testing.T) {
	h := hostFromAddr(t, newTestServer(t, testUser, testPass), "m4")
	secrets := host.MemoryStore{"m4-pass": testPass}
	m := NewManager(secrets, 5*time.Second)

	e1, err := m.ExecutorFor(h)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	e1.(*SSH).client.Close() // 绕过 Manager 直接关底层连接，缓存条目仍在
	e2, err := m.ExecutorFor(h)
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	if e1 == e2 {
		t.Fatal("dead cached client must trigger redial")
	}
}

// TestManagerRedialsBlackholedClient：握手正常但全局请求永不回复的黑洞
// 服务器——探活 SendRequest 永久挂起，看门狗期限 min(timeout, 5s) 到期
// 必须判死、关连接并重拨。无看门狗的实现会在探活上持锁冻结整个 Manager。
func TestManagerRedialsBlackholedClient(t *testing.T) {
	h := hostFromAddr(t, newBlackholeGlobalReqServer(t, testUser, testPass), "m5")
	secrets := host.MemoryStore{"m5-pass": testPass}
	m := NewManager(secrets, 500*time.Millisecond) // 探活期限 = min(500ms, 5s)

	e1, err := m.ExecutorFor(h)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	start := time.Now()
	e2, err := m.ExecutorFor(h) // 命中缓存 → 探活黑洞 → 看门狗到期 → 判死重拨
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	if e1 == e2 {
		t.Fatal("blackholed client must be judged dead and redialed")
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("probe gave up before deadline: %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("probe watchdog took %v, want < 3s", elapsed)
	}
}

// newBlackholeGlobalReqServer 起一个握手正常但全局请求永不回复的 SSH
// 服务器：握手完成后不消费全局请求通道，客户端 wantReply 的 keepalive
// 探活将永久挂起（探活看门狗测试专用；exec/sftp 不可用，勿挪作他用）。
func newBlackholeGlobalReqServer(t *testing.T, user, password string) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)

	cfg := &ssh.ServerConfig{}
	cfg.PasswordCallback = func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if meta.User() == user && string(pass) == password {
			return nil, nil
		}
		return nil, fmt.Errorf("auth rejected for %s", meta.User())
	}
	cfg.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				_ = reqs          // 黑洞：永不读取——全局请求永远无回复
				for range chans { // 只为占住连接直到客户端关闭；本测试不开 channel
				}
			}(conn)
		}
	}()
	return listener.Addr().String()
}

// slowAcceptServer 起一个 accept 后先睡 delay 再握手的 SSH 服务器：
// 拨号全程慢，用于验证「一台慢主机的拨号不阻塞其他主机的 ExecutorFor」。
func slowAcceptServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				time.Sleep(delay) // 拨号慢的来源
				sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for range chans {
				}
			}(conn)
		}
	}()
	return listener.Addr().String()
}

// TestManagerSlowHostDoesNotBlockOthers: 评审 ③-4——拨号移出全局锁后，
// 慢主机（1.2s 握手延迟）的拨号进行中，健康主机的 ExecutorFor 必须立即
// 返回，不被前者阻塞。慢主机认证无密码回调会失败，但拨号耗时足够观察。
func TestManagerSlowHostDoesNotBlockOthers(t *testing.T) {
	slowAddr := slowAcceptServer(t, 1200*time.Millisecond)
	fastAddr := newTestServer(t, testUser, testPass)

	hSlow := hostFromAddr(t, slowAddr, "slow")
	hFast := hostFromAddr(t, fastAddr, "fast")
	secrets := host.MemoryStore{"fast-pass": testPass, "slow-pass": testPass}

	m := NewManager(secrets, 5*time.Second)

	done := make(chan error, 1)
	go func() {
		_, err := m.ExecutorFor(hSlow)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // 让慢拨号先进入阻塞段

	start := time.Now()
	if _, err := m.ExecutorFor(hFast); err != nil {
		t.Fatalf("fast host dial: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 800*time.Millisecond {
		t.Fatalf("fast host blocked %v by slow host dial; want < 800ms (per-host lock)", elapsed)
	}
	<-done // 等慢拨号收尾（认证失败返回），防 goroutine 泄漏干扰后续
}

// TestTruncateSessionOutput: 评审 ③-6 单命令 2MB 上限——超限保留头部
// 并标注省略量；未超限原样返回。
func TestTruncateSessionOutput(t *testing.T) {
	small := []byte("short output")
	if got := truncateSessionOutput(small); string(got) != "short output" {
		t.Fatalf("small output must pass through, got %q", got)
	}
	big := bytes.Repeat([]byte("x"), maxSessionOutput+1024)
	got := truncateSessionOutput(big)
	if len(got) <= maxSessionOutput {
		t.Fatalf("truncated output len = %d; want > cap (note appended)", len(got))
	}
	if !bytes.Contains(got, []byte("[session output truncated, 1024 bytes omitted]")) {
		t.Fatalf("truncation note missing: %q", got[len(got)-60:])
	}
	if !bytes.Equal(got[:maxSessionOutput], bytes.Repeat([]byte("x"), maxSessionOutput)) {
		t.Fatal("head must be preserved up to cap")
	}
}
