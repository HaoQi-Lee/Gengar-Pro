package term

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/host"
)

// TOFU 库隔离（同 executor 测试思路）：绝不触碰真实 known_hosts。
// 路径带 PID：多包并行测试各占一个进程，避免互相覆盖文件。
func init() {
	p := filepath.Join(os.TempDir(), fmt.Sprintf("gengar-term-test-known-hosts-%d", os.Getpid()))
	executor.SetKnownHostsPath(p)
	_ = os.Remove(p)
}

// newTermServer 启动进程内 SSH 服务器：密码认证 + PTY/shell 请求放行 +
// stdin 原样回显（io.Copy(ch, ch)）+ pty-req/window-change 尺寸记录。
// conns 暴露已接受连接，供服务器侧主动断连用例。所有 chan 一律非阻塞
// 投递：压力用例会开上百会话，无人消费的阻塞投递会卡死请求处理循环，
// 进而让客户端 RequestPty/WindowChange 永远等不到回复——整场死锁。
func newTermServer(t *testing.T, user, pass string) (addr string, winCh, ptyCh chan [2]uint32, conns chan net.Conn) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{}
	cfg.PasswordCallback = func(meta ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if meta.User() == user && string(p) == pass {
			return nil, nil
		}
		return nil, fmt.Errorf("auth rejected")
	}
	cfg.AddHostKey(signer)
	winCh = make(chan [2]uint32, 16)
	ptyCh = make(chan [2]uint32, 16)
	conns = make(chan net.Conn, 16)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			select {
			case conns <- conn:
			default:
			}
			go serveTermConn(conn, cfg, winCh, ptyCh)
		}
	}()
	return l.Addr().String(), winCh, ptyCh, conns
}

func serveTermConn(conn net.Conn, cfg *ssh.ServerConfig, winCh, ptyCh chan [2]uint32) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue // Accept 失败 ch 为 nil，进处理会 panic
		}
		go serveTermSession(ch, chReqs, winCh, ptyCh)
	}
}

func serveTermSession(ch ssh.Channel, reqs <-chan *ssh.Request, winCh, ptyCh chan [2]uint32) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			// RFC 4254 §6.2：term, cols, rows, wpx, hpx（尾部 modes 不解）。
			var pr struct {
				Term                 string
				Cols, Rows, Wpx, Hpx uint32
			}
			_ = ssh.Unmarshal(req.Payload, &pr)
			select {
			case ptyCh <- [2]uint32{pr.Cols, pr.Rows}:
			default:
			}
			_ = req.Reply(true, nil)
		case "env":
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			go func() { _, _ = io.Copy(ch, ch) }() // 回显：stdin → stdout
		case "window-change":
			// RFC 4254 §6.7：cols, rows, width_px, height_px
			var wc struct{ Cols, Rows, Wpx, Hpx uint32 }
			_ = ssh.Unmarshal(req.Payload, &wc)
			select {
			case winCh <- [2]uint32{wc.Cols, wc.Rows}:
			default:
			}
			_ = req.Reply(true, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// openOnErr 是 openOn 的无 t 版本：压力用例在非测试 goroutine 里调用，
// 而 t.Fatalf 只允许在测试主 goroutine 使用（会 runtime.Goexit 卡死 wg）。
func openOnErr(m *Manager, hostID, addr string) (string, error) {
	hostPart, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	port, _ := strconv.Atoi(portStr)
	h := &host.Host{ID: hostID, Name: hostID, Addr: hostPart, Port: port, User: "opuser",
		Auth: host.AuthConfig{Method: host.AuthPassword, PasswordRef: hostID + "-pass"}}
	return m.Open(h, "e2e-owner")
}

// openOn 在已建 Manager 上对指定主机开一个会话（各用例共用的小脚手架）。
func openOn(t *testing.T, m *Manager, hostID, addr string) string {
	t.Helper()
	id, err := openOnErr(m, hostID, addr)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return id
}

func waitEvent(t *testing.T, events chan Event, wantID string) Event {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if ev.ID == wantID {
				return ev
			}
		case <-time.After(5 * time.Second):
			t.Fatal("等待事件超时")
		}
	}
}

func decodeB64(t *testing.T, s string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return string(raw)
}

// waitGone 轮询直至 Write 报错：EventExit 在会话出表之前发出，收到事件
// 后表项可能还在一个微小窗口期内，直接断言会闪失败。
func waitGone(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := m.Write(id, "x"); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("会话在远端退出后仍未回收")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOpenWriteEcho(t *testing.T) {
	addr, _, ptyCh, _ := newTermServer(t, "opuser", "oppass")
	events := make(chan Event, 64)
	m := NewManager(host.MemoryStore{"t1-pass": "oppass"}, func(ev Event) { events <- ev })
	t.Cleanup(m.CloseAll)

	id := openOn(t, m, "t1", addr)
	// 锁死 PTY 初始尺寸陷阱：x/crypto 的 RequestPty 参数序是 (term, h, w)，
	// 一旦写反，此处抓个正着。
	select {
	case got := <-ptyCh:
		if got != [2]uint32{80, 24} {
			t.Fatalf("初始 PTY = %v, want [80 24]", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 pty-req 超时")
	}
	if err := m.Write(id, "hi"); err != nil {
		t.Fatalf("write: %v", err)
	}
	ev := waitEvent(t, events, id)
	if ev.Type != EventData {
		t.Fatalf("首个事件应为 data，got %+v", ev)
	}
	if got := decodeB64(t, ev.Data); got != "hi" {
		t.Fatalf("回显 = %q, want %q", got, "hi")
	}
}

func TestResizeSendsWindowChange(t *testing.T) {
	addr, winCh, _, _ := newTermServer(t, "opuser", "oppass")
	events := make(chan Event, 64)
	m := NewManager(host.MemoryStore{"t1-pass": "oppass"}, func(ev Event) { events <- ev })
	t.Cleanup(m.CloseAll)

	id := openOn(t, m, "t1", addr)
	if err := m.Resize(id, 120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	select {
	case got := <-winCh:
		if got != [2]uint32{120, 40} {
			t.Fatalf("window-change = %v, want [120 40]", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 window-change 超时")
	}
}

func TestWriteUnknownSession(t *testing.T) {
	m := NewManager(host.MemoryStore{}, func(Event) {})
	if err := m.Write("nope", "x"); err == nil {
		t.Fatal("未知会话 Write 应报错")
	}
	if err := m.Resize("nope", 80, 24); err == nil {
		t.Fatal("未知会话 Resize 应报错")
	}
}

func TestCloseIsIdempotentAndEmitsExit(t *testing.T) {
	addr, _, _, _ := newTermServer(t, "opuser", "oppass")
	events := make(chan Event, 64)
	m := NewManager(host.MemoryStore{"t1-pass": "oppass"}, func(ev Event) { events <- ev })

	id := openOn(t, m, "t1", addr)
	m.Close(id)
	m.Close(id) // 幂等：不 panic
	ev := waitEvent(t, events, id)
	if ev.Type != EventExit {
		t.Fatalf("Close 后应收到 exit，got %+v", ev)
	}
	if err := m.Write(id, "x"); err == nil {
		t.Fatal("关闭后 Write 应报错（会话已从表移除）")
	}
}

func TestCloseHost(t *testing.T) {
	addr, _, _, _ := newTermServer(t, "opuser", "oppass")
	events := make(chan Event, 64)
	// 两台“主机”各开一个会话（同一测试服务器即可，hostID 区分）
	m := NewManager(host.MemoryStore{"h1-pass": "oppass", "h2-pass": "oppass"}, func(ev Event) { events <- ev })
	t.Cleanup(m.CloseAll)
	id1 := openOn(t, m, "h1", addr)
	id2 := openOn(t, m, "h2", addr)
	m.CloseHost("h1")
	if err := m.Write(id1, "x"); err == nil {
		t.Fatal("CloseHost 后 h1 会话应已移除")
	}
	if err := m.Write(id2, "x"); err != nil {
		t.Fatalf("h2 会话不应受影响: %v", err)
	}
}

func TestFindByHostAndExists(t *testing.T) {
	addr, _, _, _ := newTermServer(t, "opuser", "oppass")
	m := NewManager(host.MemoryStore{"h1-pass": "oppass"}, func(Event) {})
	t.Cleanup(m.CloseAll)

	id1 := openOn(t, m, "h1", addr)
	if got, ok := m.FindByHost("h1", "e2e-owner"); !ok || got != id1 {
		t.Fatalf("FindByHost = %q, %v; want %q, true", got, ok, id1)
	}
	if _, ok := m.FindByHost("h2", "e2e-owner"); ok {
		t.Fatal("FindByHost(h2) 应为 false")
	}
	if !m.Exists(id1) {
		t.Fatal("Exists(id1) 应为 true")
	}
	m.Close(id1)
	waitGone(t, m, id1)
	if _, ok := m.FindByHost("h1", "e2e-owner"); ok {
		t.Fatal("关闭后 FindByHost 应为 false")
	}
	if m.Exists(id1) {
		t.Fatal("关闭后 Exists 应为 false")
	}
}

// TestOwnerIsolation —— 多用户隔离：同主机不同属主各得各的 PTY（id 不同，
// 互不可见）；Owner 反查正确；id 为随机 hex（不可枚举）。
func TestOwnerIsolation(t *testing.T) {
	addr, _, _, _ := newTermServer(t, "opuser", "oppass")
	m := NewManager(host.MemoryStore{"h1-pass": "oppass"}, func(Event) {})
	t.Cleanup(m.CloseAll)

	idA := openOnErr2(t, m, "h1", addr, "alice")
	idB := openOnErr2(t, m, "h1", addr, "bob")
	if idA == idB {
		t.Fatalf("同主机不同属主拿到同一会话 id %q", idA)
	}
	if got, ok := m.FindByHost("h1", "alice"); !ok || got != idA {
		t.Fatalf("alice 视角 FindByHost = %q, %v; want %q, true", got, ok, idA)
	}
	if got, ok := m.FindByHost("h1", "bob"); !ok || got != idB {
		t.Fatalf("bob 视角 FindByHost = %q, %v; want %q, true", got, ok, idB)
	}
	if owner, ok := m.Owner(idA); !ok || owner != "alice" {
		t.Fatalf("Owner(idA) = %q, %v; want alice, true", owner, ok)
	}
	if _, ok := m.Owner("nope"); ok {
		t.Fatal("Owner(未知 id) 应为 false")
	}
	if len(idA) != 32 { // 16 字节 hex
		t.Fatalf("会话 id 应为 16 字节 hex（32 字符），got %d 字符: %q", len(idA), idA)
	}
}

// openOnErr2 是带属主版本的小脚手架（openOn 固定 e2e-owner）。
func openOnErr2(t *testing.T, m *Manager, hostID, addr, owner string) string {
	t.Helper()
	hostPart, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	h := &host.Host{ID: hostID, Name: hostID, Addr: hostPart, Port: port, User: "opuser",
		Auth: host.AuthConfig{Method: host.AuthPassword, PasswordRef: hostID + "-pass"}}
	id, err := m.Open(h, owner)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return id
}

func TestServerDisconnectEmitsExit(t *testing.T) {
	addr, _, _, conns := newTermServer(t, "opuser", "oppass")
	events := make(chan Event, 64)
	m := NewManager(host.MemoryStore{"t1-pass": "oppass"}, func(ev Event) { events <- ev })

	id := openOn(t, m, "t1", addr)
	select {
	case conn := <-conns:
		_ = conn.Close() // 服务器侧断开已建立的连接（非用户主动 Close 路径）
	case <-time.After(5 * time.Second):
		t.Fatal("等待服务器侧连接超时")
	}
	ev := waitEvent(t, events, id)
	if ev.Type != EventExit {
		t.Fatalf("服务器断连后应收到 exit，got %+v", ev)
	}
	waitGone(t, m, id)
}

// TestOpenSameHostReuses 顺序路径：同主机第二次 Open 幂等返回同一会话
// （按主机单会话的 GetOrCreate 语义）。
func TestOpenSameHostReuses(t *testing.T) {
	addr, _, _, _ := newTermServer(t, "opuser", "oppass")
	m := NewManager(host.MemoryStore{"h1-pass": "oppass"}, func(Event) {})
	t.Cleanup(m.CloseAll)

	id1 := openOn(t, m, "h1", addr)
	id2 := openOn(t, m, "h1", addr)
	if id1 != id2 {
		t.Fatalf("同主机重复 Open 应复用会话: %q vs %q", id1, id2)
	}
}

// TestOpenSameHostConcurrentSingleDial 并发路径：N 个 goroutine 同时 Open
// 同一主机，按主机拨号串行化 + 锁内重查应收敛为单会话——全部返回同一 ID
// （每次真拨号 nextID 自增，若出现双拨号必有不同 ID）。CI 抖动兜底：
// -count=20 压力复跑。
func TestOpenSameHostConcurrentSingleDial(t *testing.T) {
	addr, _, _, _ := newTermServer(t, "opuser", "oppass")
	m := NewManager(host.MemoryStore{"h1-pass": "oppass"}, func(Event) {})
	t.Cleanup(m.CloseAll)

	const n = 8
	ids := make(chan string, n)
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := openOnErr(m, "h1", addr) // goroutine 内禁用 t.Fatalf
			if err != nil {
				errCh <- err
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Errorf("并发 open: %v", e)
	}
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("并发 Open 同主机应串行化为单会话，got %d 个不同 id: %v", len(seen), seen)
	}
	got, ok := m.FindByHost("h1", "e2e-owner")
	if !ok || !seen[got] {
		t.Fatalf("FindByHost = %q, %v; want 唯一 id %v", got, ok, seen)
	}
}

func TestConcurrentStress(t *testing.T) {
	addr, _, _, _ := newTermServer(t, "opuser", "oppass")
	events := make(chan Event, 1024)
	// 持续排空事件：Emitter 契约禁止无限期阻塞，回显洪峰涨破缓冲时会卡死
	// 输出泵；本用例不看事件内容，排空即可（goroutine 随测试进程退出回收）。
	go func() {
		for range events {
		}
	}()
	m := NewManager(host.MemoryStore{"s1-pass": "oppass", "s2-pass": "oppass"}, func(ev Event) { events <- ev })
	t.Cleanup(m.CloseAll)
	var wg sync.WaitGroup
	errCh := make(chan error, 128)
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			hostID := "s1"
			if g%2 == 1 {
				hostID = "s2"
			}
			for i := 0; i < 20; i++ {
				id, err := openOnErr(m, hostID, addr) // goroutine 内禁用 t.Fatalf
				if err != nil {
					errCh <- fmt.Errorf("open %s: %w", hostID, err)
					continue
				}
				_ = m.Write(id, "x")
				_ = m.Resize(id, 80+i%10, 24)
				if i%3 == 0 {
					m.Close(id)
				}
			}
		}(g)
	}
	wg.Wait()
	m.CloseAll()
	close(errCh)
	for e := range errCh {
		t.Errorf("压力用例: %v", e)
	}
	// 不断言事件计数（时序无关）；能跑完不死锁不 panic 即通过。
}
