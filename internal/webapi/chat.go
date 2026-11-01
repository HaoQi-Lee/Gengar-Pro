// internal/webapi/chat.go
// 对话端点：SubmitMessage/CancelTask/Resolve* 的 web 移植 + WS 事件流。
// 任务 goroutine 语义与桌面 app.go 一致（单任务锁、panic 防护、EmitDone
// 收尾），差别仅在 ctx 用 Background 派生（任务长于 HTTP 请求）。
package webapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/command"
	"gengar-pro/internal/ui"
)

// userAudit 给每条审计 Entry 盖操作者用户名（agent.AuditLogger 实现）。
// 契约（见 agent.AuditLogger）：内层为 nil 时不得包装（WithAudit 的归一化
// 只识别 *audit.Logger 的 typed-nil），调用方已用 if auditLog != nil 保证。
type userAudit struct {
	l    *audit.Logger
	user string
}

func (u *userAudit) Log(e audit.Entry) error {
	e.User = u.user
	return u.l.Log(e)
}

// WS 空闲/写超时：停滞连接由 deadline 掐断（见 handleChatWS 读循环）。
const (
	wsReadIdle  = 90 * time.Second // 无上行（含心跳）即判停滞
	wsWriteWait = 10 * time.Second // 单帧写超时（对端缓冲满时不永久阻塞）
)

var upgrader = websocket.Upgrader{
	// 同源校验，与 cookie SameSite=Lax 双保险：跨站 WS 握手既带不上
	// cookie（Lax），也过不了 Origin≠Host（此处）。Origin 缺头按同源
	// 放行——非浏览器客户端（curl/probe）不发送 Origin。
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	},
}

func (s *Server) handleSubmitChat(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Text    string `json:"text"`
		Session string `json:"session"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	st := s.chatForUser(req.Session, username)
	if st == nil {
		writeErr(w, http.StatusBadRequest, "会话不存在（服务可能已重启，请刷新页面）")
		return
	}
	st.taskMu.Lock()
	if st.busy {
		st.taskMu.Unlock()
		st.sink.Error("上一个任务仍在执行中（单任务锁），请等它完成或按取消。")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.busy = true
	st.cancel = cancel
	st.taskMu.Unlock()

	go func() {
		// 与桌面 app.go 相同的 defer 顺序：recover 先注册（后执行），
		// panic 时先由 busy defer 恢复锁状态，再被这里接住转成错误帧。
		defer func() {
			if rec := recover(); rec != nil {
				st.sink.Error(fmt.Sprintf("任务异常: %v", rec))
			}
		}()
		defer func() {
			st.taskMu.Lock()
			st.busy = false
			st.cancel = nil
			st.taskMu.Unlock()
			cancel()
			st.sink.EmitDone()
		}()
		line := strings.TrimSpace(req.Text)
		if line == "" {
			return
		}
		// chat_input 审计：非空提交即记（斜杠命令同样是用户输入），记
		// 原始 req.Text 而非拼接目标前缀后的 line；8KB rune 截断带显式
		// 省略标注（truncAuditText）。
		s.auditOp(username, "chat_input", "session="+st.id,
			map[string]string{"text": truncAuditText(req.Text)})
		st.agentMu.Lock()
		ag := st.agent
		st.agentMu.Unlock()
		if ag == nil {
			st.sink.Error("Agent 未就绪（未配置 API Key 或组装失败），请在设置页检查。")
			return
		}
		if strings.HasPrefix(line, "/") {
			cmdName, args := command.SplitArgs(line)
			if s.cmdReg != nil {
				if cmd := s.cmdReg.Lookup(cmdName); cmd != nil {
					// Out 必须非 nil：Skill 命令会 Fprintln 到 ctx.Out，
					// 走 sink.Info 把加载提示推给浏览器。
					cmd.Run(command.Context{
						Agent: ag, Out: ui.NewLineWriter(st.sink.Info),
						Ctx: ctx, Args: args,
					})
					return
				}
			}
		}
		// 当轮注入本次对话目标：系统提示词已含范围规则，但这行紧贴用户
		// 消息、显著性压过对话历史的目标惯性——旧线程（如多机对比）会让
		// 模型照抄早前的 hosts 参数绕过新设定的目标。
		if names := s.scopeNames(st); names != "" {
			line = "[本次对话目标: " + names + "]\n" + line
		}
		_ = ag.ProcessInput(ctx, line)
	}()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCancelChat 取消任务：ctx cancel + 待决议放行（阻塞在确认框的
// 任务取消后也要能退出——比桌面多一步，桌面靠进程内 Esc 直连）。
func (s *Server) handleCancelChat(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Session string `json:"session"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	st := s.chatForUser(req.Session, username)
	if st == nil {
		writeErr(w, http.StatusBadRequest, "会话不存在（服务可能已重启，请刷新页面）")
		return
	}
	st.taskMu.Lock()
	cancel := st.cancel
	st.taskMu.Unlock()
	if cancel != nil {
		cancel()
		// task_cancel 仅记用户主动取消（无活动任务不记，避免噪音；断连
		// 自动取消由 ws_auto_cancel 承载，见设计 §3.5）。
		s.auditOp(username, "task_cancel", "session="+st.id, nil)
	}
	st.sink.CancelPendings()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleResolveConfirm(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Approved bool   `json:"approved"`
		Session  string `json:"session"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	st := s.chatForUser(req.Session, username)
	if st == nil {
		writeErr(w, http.StatusBadRequest, "会话不存在（服务可能已重启，请刷新页面）")
		return
	}
	ok := st.sink.ResolveConfirm(r.PathValue("id"), req.Approved)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": ok})
}

func (s *Server) handleResolveAsk(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Answer  string `json:"answer"`
		Session string `json:"session"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	st := s.chatForUser(req.Session, username)
	if st == nil {
		writeErr(w, http.StatusBadRequest, "会话不存在（服务可能已重启，请刷新页面）")
		return
	}
	ok := st.sink.ResolveAsk(r.PathValue("id"), req.Answer)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": ok})
}

// handleChatWS 对话事件流：登录（cookie）→ upgrade → hello(最新序号) →
// （必要时 reset +）按 since 重放 → 实时推送。上行帧忽略（心跳用）。
//
// 顺序是 subscribe → snapshot：subscribe 之后、snapshot 之前发布的新帧
// 既进通道又进缓冲，客户端会把同一 seq 收两次——不能改成 snapshot 先于
// subscribe（会漏帧）；客户端按 seq ≤ lastSeen 跳过即可（T4.3 events.ts）。
// snapshot 后、hello 前的微窗口同理由重放帧补齐（seq > since 自然送达）。
func (s *Server) handleChatWS(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	// 属主校验在 upgrade 之前：附加他人会话 id 直接 403，不留半开连接。
	// 空/未知 id 照常放行（下方 chatOpen 复活为新会话）。
	if sid := r.URL.Query().Get("session"); sid != "" {
		if cs := s.chatFor(sid); cs != nil && cs.owner != username {
			writeErr(w, http.StatusForbidden, "无权附加他人的对话会话")
			return
		}
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade 已写错误响应
	}
	defer conn.Close()
	// session 为空/未知（含服务重启后的 stale id）→ 复活为新会话；
	// hello.session 告知客户端实际 id，客户端据此绑定/重绑。
	st := s.chatOpen(r.URL.Query().Get("session"), username)

	ch := st.hub.subscribe()
	defer st.hub.unsubscribe(ch)
	// WS 存在即活动：reaper 以此为存活信号（订阅期间不清收）
	st.lastActive = time.Now()

	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	if since < 0 {
		since = 0
	}
	oldest, replay := st.hub.snapshot(since)
	next := st.hub.nextSeq() // hello 与 reset 守卫共用同一快照值
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	if err := conn.WriteJSON(wsFrame{T: "hello", Next: next, Session: st.id, Scope: s.scopeNames(st)}); err != nil {
		return
	}
	// 重放守卫——两种缺口都必须发 reset 让客户端清状态后全量重建：
	// 1. since < oldest-1：缓冲头已截断，since..oldest 间有丢失区间；
	// 2. since > next：服务重启后 hub 序号清零、客户端持旧生命周期的高
	//    序号——重放为空且新旧序号空间错位，不发 reset 客户端会误判
	//    「无新事件」而丢弃后续合法帧。
	if since > 0 && (since < oldest-1 || since > next) {
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		if err := conn.WriteJSON(wsFrame{T: "reset"}); err != nil {
			return
		}
	}
	for _, e := range replay {
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		if err := conn.WriteJSON(e.f); err != nil {
			return
		}
	}

	// 读循环：感知断连 + 停滞检测。90s 无上行（客户端心跳即刷新源）即
	// 读超时退出——半开 TCP/静默死链由此掐断：退出→close(done)→主循环
	// return→unsubscribe→onIdle→CancelPendings，阻塞在确认框的任务被
	// 放行，用户 busy 不会永久卡住。帧内容忽略（心跳/上行帧直接丢弃）。
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.SetReadLimit(1 << 16)
		_ = conn.SetReadDeadline(time.Now().Add(wsReadIdle))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(wsReadIdle))
		}
	}()
	for {
		select {
		case f := <-ch:
			// 写超时：对端缓冲满时 WriteJSON 不永久阻塞，写失败同样
			// 触发上面的退出→unsubscribe→onIdle 链路
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteJSON(f); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
