// 终端端点用例：假 termConn（无真 SSH）覆盖 REST 开/附加/关、WS 双向流、
// 断开不关 PTY、未知会话 404 与登录门槛。
package webapi

import (
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"gengar-pro/internal/host"
	"gengar-pro/internal/term"
)

// fakeTerm 是 termConn 的假实现（无真 SSH）。replay 模拟回放缓冲
// （评审 ④-6 attach 补发测试用）。
type fakeTerm struct {
	mu     sync.Mutex
	next   int
	byHost map[string]string   // 「hostID\x00owner」→ 会话 id（按属主隔离）
	owners map[string]string   // 会话 id → 属主
	writes map[string][]string // 在线会话（Exists 依据）
	closed []string
	replay map[string][]byte // 会话 id → 回放缓冲（Replay 依据）
}

func newFakeTerm() *fakeTerm {
	return &fakeTerm{byHost: map[string]string{}, owners: map[string]string{}, writes: map[string][]string{}, replay: map[string][]byte{}}
}

func (f *fakeTerm) Open(h *host.Host, owner string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := h.ID + "\x00" + owner
	if id, ok := f.byHost[key]; ok {
		return id, nil
	}
	f.next++
	id := fmt.Sprintf("t%d", f.next)
	f.byHost[key] = id
	f.owners[id] = owner
	f.writes[id] = []string{} // 开即在线（Exists 依据 writes 键，Close 时删除）
	return id, nil
}

func (f *fakeTerm) FindByHost(hostID, owner string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byHost[hostID+"\x00"+owner]
	return id, ok
}

func (f *fakeTerm) Owner(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner, ok := f.owners[id]
	return owner, ok
}

func (f *fakeTerm) Exists(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.writes[id] // writes 里有键即视为在线
	return ok
}

func (f *fakeTerm) Write(id, data string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.writes[id]; !ok { // 与真实 Manager 一致：死会话写报错
		return fmt.Errorf("term: unknown session %s", id)
	}
	f.writes[id] = append(f.writes[id], data)
	return nil
}

func (f *fakeTerm) Resize(id string, cols, rows int) error { return nil }

func (f *fakeTerm) Close(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	delete(f.writes, id)
	for h, sid := range f.byHost {
		if sid == id {
			delete(f.byHost, h)
		}
	}
}

func (f *fakeTerm) CloseHost(hostID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byHost, hostID)
}

func (f *fakeTerm) CloseAll() {}

// Replay 返回会话回放缓冲（测试注入）；未注入返回 nil（沿用无补发行为）。
func (f *fakeTerm) Replay(id string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replay[id]
}

// pushReplay 测试助手：向会话的回放缓冲追加内容。
func (f *fakeTerm) pushReplay(id string, p []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replay[id] = append(f.replay[id], p...)
}

func (f *fakeTerm) wrote(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes[id]...)
}

func (f *fakeTerm) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.closed)
}

// newTermTestServer：用户 alice + 一台主机 + 假终端。
func newTermTestServer(t *testing.T) (*Server, *fakeTerm, string, string, string) {
	t.Helper()
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ft := newFakeTerm()
	s.termMgr = ft
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	w, m := doJSON(t, s, "POST", "/api/hosts", `{"name":"web1","addr":"10.0.0.9","user":"root","authMethod":"password"}`, ck)
	if w.Code != 200 {
		t.Fatalf("create host = %d", w.Code)
	}
	id, _ := m["id"].(string)
	return s, ft, hostAddr, ck, id
}

func TestTermOpenAttach(t *testing.T) {
	s, ft, _, ck, hostID := newTermTestServer(t)

	w, m := doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	if w.Code != 200 {
		t.Fatalf("open = %d %v", w.Code, m)
	}
	id1, _ := m["id"].(string)
	w, m = doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	id2, _ := m["id"].(string)
	if id1 == "" || id1 != id2 {
		t.Fatalf("attach: %q vs %q", id1, id2)
	}
	if ft.closeCount() != 0 {
		t.Fatal("open 不应触发关闭")
	}
}

// TestTermWSReplayOnAttach: 评审 ④-6——attach 前产生的输出（回放缓冲）
// 在 attach 后补发，open 与 attach 之间的输出不再丢失。
func TestTermWSReplayOnAttach(t *testing.T) {
	s, ft, hostAddr, ck, hostID := newTermTestServer(t)
	_, m := doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	id, _ := m["id"].(string)

	// open 之后、attach 之前：输出进了回放缓冲
	ft.pushReplay(id, []byte("welcome banner\r\n$ "))

	hdr := map[string][]string{"Cookie": {ck}}
	c, _, err := websocket.DefaultDialer.Dial("ws://"+hostAddr+"/api/ws/term?id="+id, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// 首帧应是回放补发（base64 of "welcome banner\r\n$ "）
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var f termFrame
	if err := c.ReadJSON(&f); err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if f.T != "data" {
		t.Fatalf("first frame type = %q; want data (replay)", f.T)
	}
	decoded, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		t.Fatalf("replay not base64: %v", err)
	}
	if !strings.Contains(string(decoded), "welcome banner") {
		t.Fatalf("replay content = %q", decoded)
	}

	// 回放之后的实时帧照常（顺序：补发先于实时）
	waitSubscribed(t, s, id, 1)
	s.onTermEvent(term.Event{Type: term.EventData, ID: id, Data: "aGk="})
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := c.ReadJSON(&f); err != nil {
		t.Fatalf("read live: %v", err)
	}
	if f.T != "data" || f.Data != "aGk=" {
		t.Fatalf("live frame = %+v", f)
	}
}

func TestTermWSFlow(t *testing.T) {
	s, ft, hostAddr, ck, hostID := newTermTestServer(t)
	_, m := doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	id, _ := m["id"].(string)

	hdr := map[string][]string{"Cookie": {ck}}
	c, _, err := websocket.DefaultDialer.Dial("ws://"+hostAddr+"/api/ws/term?id="+id, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	waitSubscribed(t, s, id, 1)

	// 服务端输出 → data 帧（base64 原样透传）
	s.onTermEvent(term.Event{Type: term.EventData, ID: id, Data: "aGk="})
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var f termFrame
	if err := c.ReadJSON(&f); err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.T != "data" || f.Data != "aGk=" {
		t.Fatalf("frame = %+v", f)
	}

	// 上行 write 帧 → Manager.Write
	_ = c.WriteJSON(termFrame{T: "write", Data: "ls\n"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w := ft.wrote(id); len(w) == 1 && w[0] == "ls\n" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if w := ft.wrote(id); len(w) != 1 || w[0] != "ls\n" {
		t.Fatalf("writes = %v", w)
	}

	// WS 断开不关 PTY
	_ = c.Close()
	time.Sleep(150 * time.Millisecond)
	if ft.closeCount() != 0 {
		t.Fatal("WS 断开不应关闭会话")
	}

	// REST close → 关闭生效（exit 帧由真实 Manager 的 wait goroutine 发；
	// fake 不发，此处只验证 REST 侧关闭动作）
	w2, _ := doJSON(t, s, "POST", "/api/term/"+id+"/close", "", ck)
	if w2.Code != 200 || ft.closeCount() != 1 {
		t.Fatalf("close = %d, closed = %d", w2.Code, ft.closeCount())
	}
}

func TestTermWSUnknownID(t *testing.T) {
	_, _, hostAddr, ck, _ := newTermTestServer(t)
	hdr := map[string][]string{"Cookie": {ck}}
	_, resp, err := websocket.DefaultDialer.Dial("ws://"+hostAddr+"/api/ws/term?id=nope", hdr)
	if err == nil {
		t.Fatal("unknown id should be rejected")
	}
	if resp == nil || resp.StatusCode != 404 {
		t.Fatalf("resp = %v; want 404", resp)
	}
}

func TestTermOpenRequireAuth(t *testing.T) {
	s := newTestServer(t, nil)
	w, _ := doJSON(t, s, "POST", "/api/hosts/x/term", "", "")
	if w.Code != 401 {
		t.Fatalf("open without login = %d", w.Code)
	}
}

// TestTermOwnership —— 终端属主隔离：bob 不能附加/关闭 alice 的会话。
// 主机归属 alice（其创建）——bob 对他人主机开终端被 404（主机视角隔离，
// isolation_test.go）；bob 的自有主机上另开自己的 PTY（id 不同）。
func TestTermOwnership(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "bob": "pw2"})
	ft := newFakeTerm()
	s.termMgr = ft
	ck := login(t, s, "alice", "pw1")
	ckBob := login(t, s, "bob", "pw2")
	w, m := doJSON(t, s, "POST", "/api/hosts", `{"name":"web1","addr":"10.0.0.9","user":"root","authMethod":"password"}`, ck)
	if w.Code != 200 {
		t.Fatalf("create host = %d %v", w.Code, m)
	}
	hostID, _ := m["id"].(string)
	// bob 也建自己的主机（bob 视角内合法目标）
	w, m = doJSON(t, s, "POST", "/api/hosts", `{"name":"bob-web","addr":"10.0.0.9","user":"root","authMethod":"password"}`, ckBob)
	if w.Code != 200 {
		t.Fatalf("create bob host = %d %v", w.Code, m)
	}
	bobHostID, _ := m["id"].(string)

	// alice 开终端
	w, m = doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	if w.Code != 200 {
		t.Fatalf("alice open = %d %v", w.Code, m)
	}
	idAlice, _ := m["id"].(string)

	// bob 对 alice 的主机开 → 404（属主隔离：主机不存在）
	w, m = doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ckBob)
	if w.Code != 404 {
		t.Fatalf("bob open alice host = %d %v; want 404", w.Code, m)
	}
	// bob 对自己的主机开 → 自己的会话（非 alice 的）
	w, m = doJSON(t, s, "POST", "/api/hosts/"+bobHostID+"/term", "", ckBob)
	if w.Code != 200 {
		t.Fatalf("bob open own = %d %v", w.Code, m)
	}
	idBob, _ := m["id"].(string)
	if idAlice == idBob {
		t.Fatalf("bob 复用了 alice 的会话 %q", idBob)
	}

	// bob 关 alice 的会话 → 403，且未实际关闭
	w, m = doJSON(t, s, "POST", "/api/term/"+idAlice+"/close", "", ckBob)
	if w.Code != 403 {
		t.Fatalf("bob close alice session = %d %v", w.Code, m)
	}
	if ft.closeCount() != 0 {
		t.Fatal("403 关闭不得实际生效")
	}

	// bob 经 WS 附加 alice 的会话 → 403（upgrade 前拒绝）
	req := httptest.NewRequest("GET", "/api/ws/term?id="+idAlice, nil)
	req.Header.Set("Cookie", ckBob)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("bob attach alice session = %d", rec.Code)
	}

	// alice 自己关闭幂等正常
	w, m = doJSON(t, s, "POST", "/api/term/"+idAlice+"/close", "", ck)
	if w.Code != 200 {
		t.Fatalf("alice close own = %d %v", w.Code, m)
	}
}

// readTermFrame 从 WS 连接读一帧 termFrame（5s 超时）。
func readTermFrame(t *testing.T, c *websocket.Conn) termFrame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var f termFrame
	if err := c.ReadJSON(&f); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return f
}

// dialTerm 登录态拨终端 WS。
func dialTerm(t *testing.T, hostAddr, ck, id string) *websocket.Conn {
	t.Helper()
	hdr := map[string][]string{"Cookie": {ck}}
	c, _, err := websocket.DefaultDialer.Dial("ws://"+hostAddr+"/api/ws/term?id="+id, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// waitSubscribed 白盒等待该会话订阅数达 want：握手返回早于 handler 的
// subscribe（Upgrade 在前），dial 后立刻 onTermEvent 会丢帧——先等订阅
// 建立再发事件，消除竞态。
func waitSubscribed(t *testing.T, s *Server, id string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.termHub.mu.Lock()
		n := len(s.termHub.subs[id])
		s.termHub.mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("订阅数未达 %d（当前 %d）", want, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTermMultiSubscriber 多订阅者 fan-out：同会话两个 WS 附加，一个事件
// 双方都收到；一方断开后另一方仍能收到后续帧（退订不影响幸存者）。
func TestTermMultiSubscriber(t *testing.T) {
	s, _, hostAddr, ck, hostID := newTermTestServer(t)
	_, m := doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	id, _ := m["id"].(string)

	c1 := dialTerm(t, hostAddr, ck, id)
	c2 := dialTerm(t, hostAddr, ck, id)
	waitSubscribed(t, s, id, 2)

	s.onTermEvent(term.Event{Type: term.EventData, ID: id, Data: "aGk="})
	for i, c := range []*websocket.Conn{c1, c2} {
		if f := readTermFrame(t, c); f.T != "data" || f.Data != "aGk=" {
			t.Fatalf("conn #%d frame = %+v; want data aGk=", i+1, f)
		}
	}

	// c1 断开（服务端退订）后，c2 仍收到后续帧
	_ = c1.Close()
	time.Sleep(150 * time.Millisecond)
	s.onTermEvent(term.Event{Type: term.EventData, ID: id, Data: "eHk="}) // "xy"
	if f := readTermFrame(t, c2); f.T != "data" || f.Data != "eHk=" {
		t.Fatalf("幸存者 frame = %+v; want data eHk=", f)
	}
}

// TestTermExitFrame EventExit → {"t":"exit","error":...} 下行帧。
func TestTermExitFrame(t *testing.T) {
	s, _, hostAddr, ck, hostID := newTermTestServer(t)
	_, m := doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	id, _ := m["id"].(string)

	c := dialTerm(t, hostAddr, ck, id)
	waitSubscribed(t, s, id, 1)
	s.onTermEvent(term.Event{Type: term.EventExit, ID: id, Err: "boom"})
	if f := readTermFrame(t, c); f.T != "exit" || f.Error != "boom" {
		t.Fatalf("frame = %+v; want exit/boom", f)
	}
}

// TestTermWriteAfterCloseGetsExit 会话被 REST close 后，WS 上行 write 打到
// 死会话 → 服务端补发 exit 帧并断流（按键不再无声消失）。
func TestTermWriteAfterCloseGetsExit(t *testing.T) {
	s, _, hostAddr, ck, hostID := newTermTestServer(t)
	_, m := doJSON(t, s, "POST", "/api/hosts/"+hostID+"/term", "", ck)
	id, _ := m["id"].(string)

	c := dialTerm(t, hostAddr, ck, id)
	doJSON(t, s, "POST", "/api/term/"+id+"/close", "", ck)
	_ = c.WriteJSON(termFrame{T: "write", Data: "x"})
	if f := readTermFrame(t, c); f.T != "exit" {
		t.Fatalf("frame = %+v; want exit", f)
	}
}
