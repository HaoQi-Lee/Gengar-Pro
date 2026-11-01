// internal/webapi/termws.go
// 终端端点：REST 开（按主机 GetOrCreate）/关 + WS 双向流。term.Manager
// 的单一 Emitter 在此 fan-out 给该会话的全部订阅者（多端附加同一 PTY）。
package webapi

import (
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/host"
	"gengar-pro/internal/term"
)

// termConn 是 webapi 对终端管理器的依赖面（*term.Manager 实现；假实现
// 供单测脱离真实 SSH）。
type termConn interface {
	Open(h *host.Host, owner string) (string, error)
	FindByHost(hostID, owner string) (string, bool)
	Owner(id string) (string, bool)
	Exists(id string) bool
	Write(id, data string) error
	Resize(id string, cols, rows int) error
	Close(id string)
	CloseHost(hostID string)
	CloseAll()
	// Replay 返回会话的回放缓冲快照（评审 ④-6：attach 时补发最近输出；
	// 假实现返回 nil 即沿用旧行为）。
	Replay(id string) []byte
}

// termFrame 是终端 WS 帧。下行：data（base64 原样）/exit；上行：write/resize。
type termFrame struct {
	T     string `json:"t"` // data|exit|write|resize
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
}

// termHub 按 sessionID 管理订阅者通道；onTermEvent 是注入 term.Manager
// 的 Emitter（并发安全、非阻塞——满足 term.Emitter 契约）。
type termHub struct {
	mu   sync.Mutex
	subs map[string]map[chan termFrame]struct{}
	// lastSeen 记录每会话最近一次订阅者在线时间（reaper 判定基准）。
	lastSeen map[string]time.Time
}

func newTermHub() *termHub {
	return &termHub{subs: map[string]map[chan termFrame]struct{}{}, lastSeen: map[string]time.Time{}}
}

func (h *termHub) subscribe(id string) chan termFrame {
	ch := make(chan termFrame, 64)
	h.mu.Lock()
	if h.subs[id] == nil {
		h.subs[id] = map[chan termFrame]struct{}{}
	}
	h.subs[id][ch] = struct{}{}
	h.lastSeen[id] = time.Now()
	h.mu.Unlock()
	return ch
}

func (h *termHub) unsubscribe(id string, ch chan termFrame) {
	h.mu.Lock()
	if m := h.subs[id]; m != nil {
		delete(m, ch)
		if len(m) == 0 {
			delete(h.subs, id)
		}
	}
	h.mu.Unlock()
}

// reapIdle 回收无订阅超 1 小时的 PTY（评审 ③-2）：PTY 常驻但无人观看
// 即纯资源占用（SSH 连接 + 会话表）。关掉的终端前端重连时收到
// 「会话不存在」提示重开。只对「无订阅且超过期限」的动手——有人
// 在看（或 1 小时内看过）的终端不动。
func (h *termHub) reapIdle(mgr termConn) {
	const idleAfter = time.Hour
	h.mu.Lock()
	type reap struct {
		id   string
		last time.Time
	}
	var targets []reap
	now := time.Now()
	for id := range h.lastSeen {
		if _, online := h.subs[id]; !online && now.Sub(h.lastSeen[id]) > idleAfter {
			targets = append(targets, reap{id, h.lastSeen[id]})
		}
	}
	for _, t := range targets {
		delete(h.lastSeen, t.id)
	}
	h.mu.Unlock()
	for _, t := range targets {
		mgr.Close(t.id)
	}
}

// fanout 锁内快照 + 非阻塞投递（不关通道：向已关闭通道发送会 panic；
// 退订后 fanout 不再选中该通道）。锁外投递不破坏顺序：同一会话同一流的
// 事件在上游已被单一拷贝 goroutine 串行 emit（stdout/stderr 两泵之间本就
// 无序，真实 PTY 远端已合并），EventExit 在 Wait 返回后才发且每帧投递同步
// 完成后 emit 才返回，故 data 恒先于 exit——终端帧无序号无重放（断档靠新
// 输出推进），与 chat hub 锁内投递的差异即在此：那边多生产者需在锁内定 seq。
func (h *termHub) fanout(id string, f termFrame) {
	h.mu.Lock()
	subs := make([]chan termFrame, 0, len(h.subs[id]))
	for ch := range h.subs[id] {
		subs = append(subs, ch)
	}
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- f:
		default: // 慢消费者丢帧（终端流无回放，断档靠新输出推进）
		}
	}
}

// handleTermOpen 开/附加终端会话：同一用户对同一主机已有会话则复用（该
// 用户的多端共享 PTY）；不同用户各开各的。属主校验前置（findHostFor）：
// 他人主机不可开终端（404，防存在性探测）。
func (s *Server) handleTermOpen(w http.ResponseWriter, r *http.Request, username string) {
	id := r.PathValue("id")
	_, isAdmin := s.viewerOf(username)
	h := s.findHostFor(id, username, isAdmin)
	if h == nil {
		writeErr(w, http.StatusNotFound, "主机不存在")
		return
	}
	if s.termMgr == nil {
		writeErr(w, http.StatusServiceUnavailable, "终端未启用")
		return
	}
	sessID, ok := s.termMgr.FindByHost(h.ID, username)
	if !ok {
		var err error
		sessID, err = s.termMgr.Open(h, username)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	s.logTerm(username, "term_open", h.Name, sessID)
	writeJSON(w, http.StatusOK, map[string]string{"id": sessID})
}

// handleTermClose 显式关闭会话（幂等）。WS 断开不触发关闭。仅属主可关
// 他人会话（会话已不存在时保持幂等 ok——无权限语义可言）。
func (s *Server) handleTermClose(w http.ResponseWriter, r *http.Request, username string) {
	if s.termMgr == nil {
		writeErr(w, http.StatusServiceUnavailable, "终端未启用")
		return
	}
	id := r.PathValue("id")
	if owner, ok := s.termMgr.Owner(id); ok && owner != username {
		writeErr(w, http.StatusForbidden, "无权操作他人的终端会话")
		return
	}
	s.termMgr.Close(id)
	s.logTerm(username, "term_close", "", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// logTerm 记终端操作审计。会话 ID 放 Output（"session=<id>"）而非 Hosts
// ——Hosts 语义是「实际生效的主机名」，会话 ID 不是主机名，不混用。
func (s *Server) logTerm(username, tool, hostName, sessionID string) {
	if s.audit == nil {
		return
	}
	hosts := []string{}
	if hostName != "" {
		hosts = append(hosts, hostName)
	}
	e := audit.Entry{
		TS: time.Now().Format(time.RFC3339), User: username,
		Tool: tool, Hosts: hosts,
	}
	if sessionID != "" {
		e.Output = "session=" + sessionID
	}
	_ = s.audit.Log(e)
}

// onTermEvent 是注入 term.NewManager 的 Emitter：Manager 并发调用，
// 只做 fan-out，不阻塞。
func (s *Server) onTermEvent(ev term.Event) {
	switch ev.Type {
	case term.EventData:
		s.termHub.fanout(ev.ID, termFrame{T: "data", Data: ev.Data})
	case term.EventExit:
		f := termFrame{T: "exit"}
		if ev.Err != "" {
			f.Error = ev.Err
		}
		s.termHub.fanout(ev.ID, f)
	}
}

// handleTermWS 终端双向流。attach 时会话必须存在（先经 REST open）且
// 属主为当前登录用户——会话 ID 虽已随机化，属主校验仍是硬边界（防 id
// 经日志/分享泄露后被附加：读输出 + 注入击键）。attach 后先补发回放
// 缓冲（评审 ④-6：open 与 attach 之间的输出不再丢失），再进入实时流。
func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	if s.termMgr == nil {
		writeErr(w, http.StatusServiceUnavailable, "终端未启用")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if owner, ok := s.termMgr.Owner(id); !ok {
		writeErr(w, http.StatusNotFound, "会话不存在")
		return
	} else if owner != username {
		writeErr(w, http.StatusForbidden, "无权附加他人的终端会话")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 回放补发先于 subscribe：补发直接写 conn，subscribe 之后的新输出经
	// hub 实时送达——顺序即输出顺序（输出泵在 emit 的 fanout 返回前不会
	// 写下一块，补发窗口内不可能有实时帧插队）。
	if replay := s.termMgr.Replay(id); len(replay) > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		if err := conn.WriteJSON(termFrame{T: "data", Data: base64.StdEncoding.EncodeToString(replay)}); err != nil {
			return
		}
	}

	ch := s.termHub.subscribe(id)
	// 死附加守卫：首查 Exists 与 subscribe 之间会话可能已被关（REST close/
	// 远端退出）——补发一帧 exit 让浏览器落地终止态（无会话可附加），
	// 退订后返回；残余竞态由上行 write 报错路径兜底（见下）。
	if !s.termMgr.Exists(id) {
		s.termHub.unsubscribe(id, ch)
		termExitThenDrain(conn)
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // 下行泵：hub → 浏览器；stop 或写失败退出。
		// 不 close(ch)：fanout 的非阻塞发送打到已关闭通道会 panic；
		// unsubscribe 之后 fanout 不再选中本通道，用 stop 信号收泵。
		defer close(done)
		for {
			select {
			case f := <-ch:
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				if err := conn.WriteJSON(f); err != nil {
					// 主动关连接让主循环的 ReadJSON 立即报错退出，
					// 不空等 5 分钟空闲超时（Close 与并发读安全）。
					_ = conn.Close()
					return
				}
			case <-stop:
				return
			}
		}
	}()
	defer func() {
		s.termHub.unsubscribe(id, ch) // 先摘订阅（此后 fanout 不再投递）
		close(stop)                   // 再停下行泵
		<-done
	}()
	// 上行循环：write/resize 帧（close 走 REST，不经 WS）。
	// 读 deadline：终端无固定心跳，宽松 5 分钟空闲掐断（浏览器重连即恢复，
	// PTY 保持）。读上限与 chat WS 一致（单帧 64KB，粘贴大块也够用）。
	conn.SetReadLimit(1 << 16)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	for {
		var f termFrame
		if err := conn.ReadJSON(&f); err != nil {
			return // 浏览器断开/空闲超时；PTY 保持（显式 close 才关）
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		switch f.T {
		case "write":
			if err := s.termMgr.Write(id, f.Data); err != nil {
				// 会话已死（读循环与 REST close/远端退出的竞态）：补发
				// exit 帧并停止转发，按键不再无声消失——浏览器提示重开。
				termExitThenDrain(conn)
				return
			}
		case "resize":
			_ = s.termMgr.Resize(id, f.Cols, f.Rows)
		}
	}
}

// termExitThenDrain 补发一帧 exit（会话已死：附加守卫/上行写报错）后排空
// 等待对端关闭。不能发完立即 closesocket：Windows 回环下偶发 RST 抢在
// exit 帧送达之前，对端读到连接中止而非帧；等对端先关即保证帧可见，
// 兜底 30s 读超时防不关的客户端长挂。
func termExitThenDrain(conn *websocket.Conn) {
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	_ = conn.WriteJSON(termFrame{T: "exit"})
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		var f termFrame
		if err := conn.ReadJSON(&f); err != nil {
			return // 对端已关/超时；后续上行全部丢弃（会话已死）
		}
	}
}
