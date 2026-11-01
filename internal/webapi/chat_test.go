package webapi

import (
	"context"
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

// TestChatInputAudit chat_input 审计：非空提交记原文（Tool/User/Output/
// Input 四字段钉死），9K 长文截断为 8K rune 前缀 + 显式省略标注（rune 安全，
// 多字节不切半），空文本不记。会话用白盒 chatOpen 建（POST 路由只查不建）；
// 三次提交各用独立会话避开单任务锁的收尾竞态。Agent 未组装也可审计
// （事件在 Agent 就绪检查之前）。
func TestChatInputAudit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	l := openTestAudit(t, s)
	st1 := s.chatOpen("", "alice")
	st2 := s.chatOpen("", "alice")
	st3 := s.chatOpen("", "alice")

	// 审计在任务 goroutine 内异步写：轮询等条目出现（最长 ~2s）
	waitEntries := func(n int) []audit.Entry {
		t.Helper()
		for i := 0; i < 200; i++ {
			if es := readAuditEntries(t, l); len(es) >= n {
				return es
			}
			time.Sleep(10 * time.Millisecond)
		}
		return readAuditEntries(t, l)
	}

	// ① 普通文本 → chat_input：User=操作者、Output=session=<id>、Input 含原文
	w, m := doJSON(t, s, "POST", "/api/chat", chatBody("帮我看看磁盘", st1.id), ck)
	if w.Code != 200 {
		t.Fatalf("chat = %d %v", w.Code, m)
	}
	es := waitEntries(1)
	if len(es) != 1 || es[0].Tool != "chat_input" || es[0].User != "alice" {
		t.Fatalf("chat_input 事件 = %+v", es)
	}
	if es[0].Output != "session="+st1.id {
		t.Fatalf("chat_input Output = %q; want session=%s", es[0].Output, st1.id)
	}
	if in := string(es[0].Input); !strings.Contains(in, `"text":"帮我看看磁盘"`) {
		t.Fatalf("chat_input Input = %s; want 含原文", in)
	}
	// Session 结构化回补：与会话内 agent 条目跨条目关联（Task 5 回补断言）
	if es[0].Session != st1.id {
		t.Fatalf("chat_input Session = %q; want %s", es[0].Session, st1.id)
	}

	// ② 9K 中文长文 → 截断为 8192 rune 前缀 + …[+808 chars]（超限可辨识）
	long := strings.Repeat("磁盘", 4500) // 9000 rune（27000 字节，验 rune 安全）
	w, _ = doJSON(t, s, "POST", "/api/chat", chatBody(long, st2.id), ck)
	if w.Code != 200 {
		t.Fatalf("long chat = %d", w.Code)
	}
	es = waitEntries(2)
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(es[1].Input, &in); err != nil {
		t.Fatalf("chat_input Input 解析失败: %v (%s)", err, es[1].Input)
	}
	want := strings.Repeat("磁盘", 4096) + "…[+808 chars]" // 8192 rune 前缀 + 标注
	if in.Text != want {
		t.Fatalf("截断不符：len(rune)=%d 尾=%q", len([]rune(in.Text)), in.Text[len(in.Text)-40:])
	}

	// ③ 空白文本（TrimSpace 后空）→ 不记
	w, _ = doJSON(t, s, "POST", "/api/chat", chatBody("   ", st3.id), ck)
	if w.Code != 200 {
		t.Fatalf("blank chat = %d", w.Code)
	}
	if es = readAuditEntries(t, l); len(es) != 2 {
		t.Fatalf("空文本后条数 = %d; want 2（不记）", len(es))
	}
}

// TestTaskCancelAudit task_cancel 审计：有活动任务（cancel 非 nil）记
// session=，Session 结构化回填（与会话内 agent 条目跨条目关联，Task 6
// 回补断言）；无活动任务不记（避免噪音）。busy/cancel 白盒置位——真实
// 任务需 Claude API 单测不可达，置位两行即 handleSubmitChat 任务锁段的
// 等价物。
func TestTaskCancelAudit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	l := openTestAudit(t, s)
	st := s.chatOpen("", "alice")

	// 无活动任务：cancel 不落条目
	w, m := doJSON(t, s, "POST", "/api/chat/cancel", `{"session":"`+st.id+`"}`, ck)
	if w.Code != 200 || m["ok"] != true {
		t.Fatalf("cancel idle = %d %v", w.Code, m)
	}
	if es := readAuditEntries(t, l); len(es) != 0 {
		t.Fatalf("无任务 cancel 条数 = %d; want 0", len(es))
	}

	// 白盒造活动任务（cancel 函数非 nil 即视为有任务）
	_, cancelFn := context.WithCancel(context.Background())
	defer cancelFn() // vet lostcancel：取消由 handler 触发，此兜底幂等无害
	st.taskMu.Lock()
	st.busy = true
	st.cancel = cancelFn
	st.taskMu.Unlock()

	w, m = doJSON(t, s, "POST", "/api/chat/cancel", `{"session":"`+st.id+`"}`, ck)
	if w.Code != 200 || m["ok"] != true {
		t.Fatalf("cancel busy = %d %v", w.Code, m)
	}
	es := readAuditEntries(t, l)
	if len(es) != 1 || es[0].Tool != "task_cancel" || es[0].User != "alice" || es[0].Output != "session="+st.id {
		t.Fatalf("task_cancel 事件 = %+v", es)
	}
	if es[0].Session != st.id {
		t.Fatalf("task_cancel Session = %q; want %s", es[0].Session, st.id)
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

// recvFrameOf 收帧直到 T==want（跳过决议测试里上一步发布的 *_done 等
// 无关帧），20 帧内未命中即失败。
func recvFrameOf(t *testing.T, sub chan wsFrame, want string) wsFrame {
	t.Helper()
	for i := 0; i < 20; i++ {
		if f := recvFrame(t, sub); f.T == want {
			return f
		}
	}
	t.Fatalf("20 帧内未等到 %s 帧", want)
	return wsFrame{}
}

// TestConfirmResolveAudit confirm_resolve 决议审计（设计 §3.4/§3.5）：REST
// 认领成功（ok=true）才记——decision=approve|deny、Input 带 session+
// confirm_id、Session 结构化回填；重复决议（ok=false）不是用户动作不追加。
// pending 用白盒 ConfirmBatch 制造（真实 Risky 链路需 Claude API 单测不可达，
// 与 TestConfirmResolveViaREST 同手法）；审计在 handler 返回前同步落盘，
// POST 返回即可直接读回。
func TestConfirmResolveAudit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	l := openTestAudit(t, s)
	st := s.chatOpen("", "alice")
	sub := st.hub.subscribe()
	t.Cleanup(func() { st.hub.unsubscribe(sub) })

	resolve := func(approved bool) string {
		res := make(chan bool, 1)
		go func() {
			res <- st.sink.ConfirmBatch([]ui.ConfirmRequest{{ToolName: "shell", Summary: "rm x"}})
		}()
		f := recvFrameOf(t, sub, "confirm")
		w, m := doJSON(t, s, "POST", "/api/confirm/"+f.ID,
			fmt.Sprintf(`{"approved":%t,"session":"%s"}`, approved, st.id), ck)
		if w.Code != 200 || m["ok"] != true {
			t.Fatalf("resolve(%t) = %d %v", approved, w.Code, m)
		}
		if got := <-res; got != approved {
			t.Fatalf("ConfirmBatch = %t; 与决议 %t 不一致", got, approved)
		}
		return f.ID
	}
	idApprove := resolve(true)
	idDeny := resolve(false)

	// 重复决议已认领过的 pid → ok=false，不追加条目
	w, m := doJSON(t, s, "POST", "/api/confirm/"+idApprove,
		`{"approved":true,"session":"`+st.id+`"}`, ck)
	if w.Code != 200 || m["ok"] != false {
		t.Fatalf("重复 resolve = %d %v; want ok=false", w.Code, m)
	}

	es := readAuditEntries(t, l)
	if len(es) != 2 {
		t.Fatalf("条数 = %d; want 2（重复决议不记）", len(es))
	}
	for i, want := range []struct{ decision, pid string }{{"approve", idApprove}, {"deny", idDeny}} {
		e := es[i]
		if e.Tool != "confirm_resolve" || e.User != "alice" {
			t.Fatalf("es[%d] = %+v", i, e)
		}
		if e.Session != st.id {
			t.Fatalf("es[%d].Session = %q; want %s", i, e.Session, st.id)
		}
		if e.Output != "decision="+want.decision {
			t.Fatalf("es[%d].Output = %q; want decision=%s", i, e.Output, want.decision)
		}
		var in map[string]string
		if err := json.Unmarshal(e.Input, &in); err != nil {
			t.Fatalf("es[%d].Input 解析失败: %v (%s)", i, err, e.Input)
		}
		if in["session"] != st.id || in["confirm_id"] != want.pid {
			t.Fatalf("es[%d].Input = %v; want session=%s confirm_id=%s", i, in, st.id, want.pid)
		}
	}
}

// TestAskResolveAudit ask_resolve 决议审计：answer 入 Output（256 rune 截断，
// 自由文本防灌爆），Input 带 session+ask_id，Session 结构化回填；决议原文
// 全文送达 AskUser（截断只发生在审计侧）。
func TestAskResolveAudit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	l := openTestAudit(t, s)
	st := s.chatOpen("", "alice")
	sub := st.hub.subscribe()
	t.Cleanup(func() { st.hub.unsubscribe(sub) })

	ask := func(answer string) string {
		res := make(chan string, 1)
		go func() {
			ans, _ := st.sink.AskUser(json.RawMessage(`{"question":"装哪个","options":[{"label":"x"}]}`))
			res <- ans
		}()
		f := recvFrameOf(t, sub, "ask")
		body, _ := json.Marshal(map[string]string{"answer": answer, "session": st.id})
		w, m := doJSON(t, s, "POST", "/api/ask/"+f.ID, string(body), ck)
		if w.Code != 200 || m["ok"] != true {
			t.Fatalf("ask resolve = %d %v", w.Code, m)
		}
		if got := <-res; got != answer {
			t.Fatalf("AskUser 应收到未截断全文（len=%d）", len([]rune(got)))
		}
		return f.ID
	}
	idShort := ask("nginx")
	long := strings.Repeat("好", 300) // 300 rune，超 256 截断档
	idLong := ask(long)

	es := readAuditEntries(t, l)
	if len(es) != 2 {
		t.Fatalf("条数 = %d; want 2", len(es))
	}
	for i, pid := range []string{idShort, idLong} {
		e := es[i]
		if e.Tool != "ask_resolve" || e.User != "alice" || e.Session != st.id {
			t.Fatalf("es[%d] = %+v", i, e)
		}
		var in map[string]string
		if err := json.Unmarshal(e.Input, &in); err != nil {
			t.Fatalf("es[%d].Input 解析失败: %v (%s)", i, err, e.Input)
		}
		if in["session"] != st.id || in["ask_id"] != pid {
			t.Fatalf("es[%d].Input = %v; want session=%s ask_id=%s", i, in, st.id, pid)
		}
	}
	if es[0].Output != "answer=nginx" {
		t.Fatalf("短答案 Output = %q; want answer=nginx", es[0].Output)
	}
	if want := "answer=" + strings.Repeat("好", 256) + "…"; es[1].Output != want {
		t.Fatalf("长答案 Output 截断不符: 尾=%q", es[1].Output[len(es[1].Output)-20:])
	}
}

// TestWSAutoCancelAudit ws_auto_cancel 审计（设计 §3.5 拒绝来源可区分）：
// 浏览器全断开（订阅 1→0 的 onIdle 路径）取消待决议时记 session=<id>
// cancelled=<n>；n=0 的 onIdle 不记。onIdle 用 subscribe→unsubscribe 归零
// 触发（与真实断链同路径，TestHubIdleCallback 手法）；用户取消按钮路径
// 不触发 onIdle（handleCancelChat 直调 CancelPendings），由 task_cancel
// 承载——两路径不串扰。pending 用白盒 ConfirmBatch/AskUser 挂起。
func TestWSAutoCancelAudit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	l := openTestAudit(t, s)
	st := s.chatOpen("", "alice")

	// ① 无 pending 的 1→0：不落条目
	ch := st.hub.subscribe()
	st.hub.unsubscribe(ch)
	if es := readAuditEntries(t, l); len(es) != 0 {
		t.Fatalf("无 pending onIdle 条数 = %d; want 0", len(es))
	}

	// ② 挂 2 个 pending（confirm+ask）→ 1→0 → cancelled=2
	sub := st.hub.subscribe()
	go func() { _ = st.sink.ConfirmBatch([]ui.ConfirmRequest{{ToolName: "shell"}}) }()
	recvFrameOf(t, sub, "confirm")
	go func() { _, _ = st.sink.AskUser(json.RawMessage(`{"question":"q"}`)) }()
	recvFrameOf(t, sub, "ask")
	st.hub.unsubscribe(sub) // 1→0 → onIdle → CancelPendings(2) → ws_auto_cancel

	es := readAuditEntries(t, l)
	if len(es) != 1 {
		t.Fatalf("条数 = %d; want 1", len(es))
	}
	e := es[0]
	if e.Tool != "ws_auto_cancel" || e.User != "alice" || e.Session != st.id {
		t.Fatalf("ws_auto_cancel = %+v", e)
	}
	if e.Output != fmt.Sprintf("session=%s cancelled=2", st.id) {
		t.Fatalf("Output = %q; want session=%s cancelled=2", e.Output, st.id)
	}
}
