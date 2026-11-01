package webapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/command"
	"gengar-pro/internal/ui"
)

// startHTTP 把 Server 挂到真实端口（WS 拨号需要）。
func startHTTP(t *testing.T, s *Server) string {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// injectPingCmd 用假命令替换注册表：测试探针用 /ping（不调 Claude API，
// 回显一行 info 后正常收尾）。
func injectPingCmd(s *Server) {
	s.cmdReg = command.NewRegistry(command.Command{
		Name:        "/ping",
		Description: "测试探针",
		Run:         func(ctx command.Context) { fmt.Fprintln(ctx.Out, "pong") },
	})
}

// wsChatOpen 登录态拨 /api/ws/chat?since=N&session=ID（session 空 = 服务端
// 新建），读 hello 返回（连接、读帧函数、hello 帧——Session/Next 断言用）。
func wsChatOpen(t *testing.T, hostAddr, cookie string, since int, session string) (*websocket.Conn, func() wsFrame, wsFrame) {
	t.Helper()
	hdr := map[string][]string{"Cookie": {cookie}}
	u := "ws://" + hostAddr + "/api/ws/chat?since=" + fmt.Sprint(since)
	if session != "" {
		u += "&session=" + session
	}
	c, _, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	read := func() wsFrame {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var f wsFrame
		if err := c.ReadJSON(&f); err != nil {
			t.Fatalf("read frame: %v", err)
		}
		return f
	}
	h := read()
	if h.T != "hello" {
		t.Fatalf("first frame = %+v; want hello", h)
	}
	if h.Session == "" {
		t.Fatal("hello 必须带 session")
	}
	return c, read, h
}

// readNoFrame 断言短窗口内无帧到达（会话隔离验证）：true = 无帧（超时）。
func readNoFrame(t *testing.T, c *websocket.Conn) bool {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var f wsFrame
	return c.ReadJSON(&f) != nil
}

// readUntil 读帧直到 pred 命中（含命中帧），返回读到的全部帧。
func readUntil(read func() wsFrame, pred func(wsFrame) bool) []wsFrame {
	var out []wsFrame
	for i := 0; i < 200; i++ {
		f := read()
		out = append(out, f)
		if pred(f) {
			return out
		}
	}
	return out
}

func isDone(f wsFrame) bool { return f.T == "chat" && f.Ev != nil && f.Ev.Type == "done" }

func chatBody(text, session string) string {
	b, _ := json.Marshal(map[string]string{"text": text, "session": session})
	return string(b)
}

func TestChatSlashCommandFlow(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	injectPingCmd(s)
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	ckAdmin := login(t, s, "admin", "pw-admin")
	// 先存 API Key 让 Agent 可组装（/ping 不调 Claude API，走命令注册表）
	w, _ := doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-test-1234","baseUrl":"","model":""}`, ckAdmin)
	if w.Code != 200 {
		t.Fatalf("settings = %d", w.Code)
	}

	_, read, hello := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := hello.Session
	w, _ = doJSON(t, s, "POST", "/api/chat", chatBody("/ping", sid), ck)
	if w.Code != 200 {
		t.Fatalf("chat = %d", w.Code)
	}
	var sawInfo bool
	for _, fr := range readUntil(read, isDone) {
		if fr.T == "chat" && fr.Ev != nil && fr.Ev.Type == "info" {
			sawInfo = true
		}
	}
	if !sawInfo {
		t.Fatal("/ping 未产生 info 帧")
	}
}

// 同账号两个会话（两个对话 tab）互不可见：A 的帧不进 B 的流。
// TestChatOwnership —— 会话属主隔离：bob 拿到 alice 的会话 id 也无法提交
// 消息/决议确认/WS 附加（REST 统一 400「会话不存在」防 id 存在性探测；
// WS attach 在 upgrade 前拒绝）。
func TestChatOwnership(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "bob": "pw2", "admin": "pw-admin"})
	injectPingCmd(s)
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	ckBob := login(t, s, "bob", "pw2")
	ckAdmin := login(t, s, "admin", "pw-admin")
	doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-test-1234"}`, ckAdmin)

	_, _, h := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := h.Session

	// bob 提交 → 400（与不存在同一文案）
	w, m := doJSON(t, s, "POST", "/api/chat", chatBody("/ping", sid), ckBob)
	if w.Code != 400 {
		t.Fatalf("bob submit = %d %v; want 400", w.Code, m)
	}
	// bob 决议确认 → 400
	w, _ = doJSON(t, s, "POST", "/api/confirm/xyz", `{"approved":true,"session":"`+sid+`"}`, ckBob)
	if w.Code != 400 {
		t.Fatalf("bob confirm = %d; want 400", w.Code)
	}
	// bob WS 附加 alice 会话 → 拨号被拒（403 先于 upgrade）
	hdr := map[string][]string{"Cookie": {ckBob}}
	_, _, err := websocket.DefaultDialer.Dial("ws://"+hostAddr+"/api/ws/chat?session="+sid, hdr)
	if err == nil {
		t.Fatal("bob attach alice 会话应被拒绝")
	}
	// alice 自己照常
	w, _ = doJSON(t, s, "POST", "/api/chat", chatBody("/ping", sid), ck)
	if w.Code != 200 {
		t.Fatalf("alice submit = %d", w.Code)
	}
}

func TestChatSessionsIsolated(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	injectPingCmd(s)
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	ckAdmin := login(t, s, "admin", "pw-admin")
	doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-test-1234"}`, ckAdmin)

	_, read1, h1 := wsChatOpen(t, hostAddr, ck, 0, "")
	sid1 := h1.Session
	c2, _, h2 := wsChatOpen(t, hostAddr, ck, 0, "")
	sid2 := h2.Session
	if sid1 == sid2 {
		t.Fatal("两会话 id 应不同")
	}
	w, _ := doJSON(t, s, "POST", "/api/chat", chatBody("/ping", sid1), ck)
	if w.Code != 200 {
		t.Fatalf("chat = %d", w.Code)
	}
	readUntil(read1, isDone)
	if !readNoFrame(t, c2) {
		t.Fatal("会话 B 不应收到会话 A 的帧")
	}
	// 每会话独立单任务锁：A 置忙不影响 B 提交
	csA := s.chatFor(sid1)
	csA.taskMu.Lock()
	csA.busy = true
	csA.taskMu.Unlock()
	w, _ = doJSON(t, s, "POST", "/api/chat", chatBody("/ping", sid2), ck)
	if w.Code != 200 {
		t.Fatalf("B chat = %d; 会话锁应独立", w.Code)
	}
}

// stale/空 session：WS 复活为新会话（hello 分配新 id，next 从 0 起）。
func TestChatWSRevivesStaleSession(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	_, _, hStale := wsChatOpen(t, hostAddr, ck, 0, "deadbeef")
	if hStale.Session == "deadbeef" {
		t.Fatal("stale session 应复活为新 id")
	}
	// 复活的会话可正常提交（未知 session 的 POST 才是 400）
	w, _ := doJSON(t, s, "POST", "/api/chat", chatBody("hi", "nope"), ck)
	if w.Code != 400 {
		t.Fatalf("unknown session = %d; want 400", w.Code)
	}
}

func TestChatReplayAfterReconnect(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	injectPingCmd(s)
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	ckAdmin := login(t, s, "admin", "pw-admin")
	doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-test-1234"}`, ckAdmin)

	c1, read1, h0 := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := h0.Session
	doJSON(t, s, "POST", "/api/chat", chatBody("/ping", sid), ck)
	var lastSeq int
	for _, fr := range readUntil(read1, isDone) {
		if fr.Seq > lastSeq {
			lastSeq = fr.Seq
		}
	}
	_ = c1.Close()

	// 同会话 since=lastSeq → hello.session 一致且 next=lastSeq（无重放窗口）
	_, _, hRes := wsChatOpen(t, hostAddr, ck, lastSeq, sid)
	if hRes.Session != sid || hRes.Next != lastSeq {
		t.Fatalf("resume hello = %+v; want session=%s next=%d", hRes, sid, lastSeq)
	}

	// since=0 → 全量重放（有 chat 帧）
	_, read3, _ := wsChatOpen(t, hostAddr, ck, 0, sid)
	if f := read3(); f.T != "chat" {
		t.Fatalf("replay first frame = %+v; want chat", f)
	}

	// since 超过服务端最新序号（服务重启后客户端持旧序号）→ reset
	_, read4, _ := wsChatOpen(t, hostAddr, ck, lastSeq+100, sid)
	if f := read4(); f.T != "reset" {
		t.Fatalf("restart-resume first frame = %+v; want reset", f)
	}
}

func TestChatBusyRejected(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	_, read, hello := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := hello.Session
	st := s.chatFor(sid)
	st.taskMu.Lock()
	st.busy = true // 白盒置忙，制造确定性
	st.taskMu.Unlock()
	defer func() {
		st.taskMu.Lock()
		st.busy = false
		st.taskMu.Unlock()
	}()
	doJSON(t, s, "POST", "/api/chat", chatBody("hi", sid), ck)
	var sawBusy bool
	for i := 0; i < 20 && !sawBusy; i++ {
		f := read()
		if f.T == "chat" && f.Ev != nil && strings.Contains(f.Ev.Text, "单任务锁") {
			sawBusy = true
		}
	}
	if !sawBusy {
		t.Fatal("忙时提交未收到单任务锁错误帧")
	}
}

func TestConfirmResolveViaREST(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	_, read, hello := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := hello.Session
	st := s.chatFor(sid)

	res := make(chan bool, 1)
	go func() {
		res <- st.sink.ConfirmBatch([]ui.ConfirmRequest{{ToolName: "shell", Summary: "rm x"}})
	}()
	var id string
	for i := 0; i < 10 && id == ""; i++ {
		if f := read(); f.T == "confirm" {
			id = f.ID
		}
	}
	if id == "" {
		t.Fatal("未收到 confirm 帧")
	}
	w, m := doJSON(t, s, "POST", "/api/confirm/"+id, `{"approved":true,"session":"`+sid+`"}`, ck)
	if w.Code != 200 || m["ok"] != true {
		t.Fatalf("resolve = %d %v", w.Code, m)
	}
	if <-res != true {
		t.Fatal("ConfirmBatch 应返回 true")
	}
}

func TestChatWSRequireAuth(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	hostAddr := startHTTP(t, s)
	_, resp, err := websocket.DefaultDialer.Dial("ws://"+hostAddr+"/api/ws/chat?since=0", nil)
	if err == nil {
		t.Fatal("未登录应拒绝")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("resp = %v; want 401", resp)
	}
}

// 取消端点放行阻塞中的确认：ConfirmBatch 返回 false，confirm_done 与
// done 帧均送达（done 由白盒 goroutine 模拟任务收尾 EmitDone——真实链路
// 里 ConfirmBatch 返回后任务继续走，busy defer 最终 EmitDone；驱动真实
// Agent 确认需 Claude API，单测不可达）。若无 done，读帧 5s 超时失败。
func TestCancelDuringConfirm(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	_, read, hello := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := hello.Session
	st := s.chatFor(sid)

	res := make(chan bool, 1)
	go func() {
		approved := st.sink.ConfirmBatch([]ui.ConfirmRequest{{ToolName: "shell", Summary: "rm x"}})
		res <- approved
		st.sink.EmitDone()
	}()
	var id string
	for i := 0; i < 10 && id == ""; i++ {
		if f := read(); f.T == "confirm" {
			id = f.ID
		}
	}
	if id == "" {
		t.Fatal("未收到 confirm 帧")
	}
	w, m := doJSON(t, s, "POST", "/api/chat/cancel", `{"session":"`+sid+`"}`, ck)
	if w.Code != 200 || m["ok"] != true {
		t.Fatalf("cancel = %d %v", w.Code, m)
	}
	if <-res != false {
		t.Fatal("ConfirmBatch 应被取消（返回 false）")
	}
	var sawConfirmDone bool
	frames := readUntil(read, isDone)
	for _, fr := range frames {
		if fr.T == "confirm_done" && fr.ID == id {
			sawConfirmDone = true
		}
	}
	if !sawConfirmDone {
		t.Fatal("取消后未收到 confirm_done")
	}
}

// done 后 busy 复位：连续两轮 /ping 都应完整执行，第二轮不得被单任务锁拒绝。
func TestBusyResetsAfterDone(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	injectPingCmd(s)
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	ckAdmin := login(t, s, "admin", "pw-admin")
	doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-test-1234"}`, ckAdmin)
	_, read, hello := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := hello.Session

	run := func(n int) {
		w, _ := doJSON(t, s, "POST", "/api/chat", chatBody("/ping", sid), ck)
		if w.Code != 200 {
			t.Fatalf("chat #%d = %d", n, w.Code)
		}
		frames := readUntil(read, isDone)
		if len(frames) == 0 || !isDone(frames[len(frames)-1]) {
			t.Fatalf("round #%d: 未收到 done 帧", n)
		}
		for _, fr := range frames {
			if fr.T == "chat" && fr.Ev != nil && strings.Contains(fr.Ev.Text, "单任务锁") {
				t.Fatalf("round #%d: 误收单任务锁错误帧 %+v", n, fr)
			}
		}
	}
	run(1)
	run(2)
}

func TestAskResolveViaREST(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "admin": "pw-admin"})
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	_, read, hello := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := hello.Session
	st := s.chatFor(sid)

	res := make(chan string, 1)
	go func() {
		ans, _ := st.sink.AskUser(json.RawMessage(`{"question":"装哪个","options":[{"label":"nginx"}]}`))
		res <- ans
	}()
	var id string
	for i := 0; i < 10 && id == ""; i++ {
		if f := read(); f.T == "ask" {
			id = f.ID
		}
	}
	if id == "" {
		t.Fatal("未收到 ask 帧")
	}
	w, m := doJSON(t, s, "POST", "/api/ask/"+id, `{"answer":"nginx","session":"`+sid+`"}`, ck)
	if w.Code != 200 || m["ok"] != true {
		t.Fatalf("ask resolve = %d %v", w.Code, m)
	}
	if <-res != "nginx" {
		t.Fatal("AskUser 应返回 nginx")
	}
}

func TestUserAuditLog(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "audit")
	l, err := audit.Open(base + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	u := &userAudit{l: l, user: "alice"}
	if err := u.Log(audit.Entry{TS: "t", Tool: "x"}); err != nil {
		t.Fatal(err)
	}
	shard := base + "-" + time.Now().Format("2006-01-02") + ".jsonl"
	raw, err := os.ReadFile(shard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"user":"alice"`) {
		t.Fatalf("audit missing user: %s", raw)
	}
}
