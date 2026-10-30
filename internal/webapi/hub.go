// hub 是单用户的事件枢纽：有界 FIFO 缓冲（序号单调递增，重连按 since
// 重放）+ 活跃 WS 订阅者 fan-out。慢消费者不阻塞发布者（非阻塞投递，
// 丢帧由客户端按序号断档检测并全量重连）。
package webapi

import (
	"encoding/json"
	"sync"

	"gengar-pro/internal/ui"
)

// bufCap 缓冲容量（帧）。内存上界：最坏帧是 tool_result 聚合 body——
// 单主机在 agent 层截断（4000/1500 head/tail，agent.go），多主机聚合受
// Registry 总预算 10000 字符约束（≤20 主机；plugin/registry.go），按
// 2000×~10KB ≈ 20MB/用户量级；主机数超过 20 后 per-host 下限 500 字符
// 生效、总量随主机数线性增长，属已知上界。
const bufCap = 2000

// wsFrame 是 WS 下行帧。T: hello|chat|confirm|ask|confirm_done|ask_done|reset
type wsFrame struct {
	T       string              `json:"t"`
	Seq     int                 `json:"seq,omitempty"`
	Next    int                 `json:"next,omitempty"`    // hello：当前最新序号
	Session string              `json:"session,omitempty"` // hello：服务端分配的会话 ID（客户端据此绑定/重绑）
	Scope   string              `json:"scope,omitempty"`   // hello：本次对话目标主机名（顿号连接；空=未设定）
	Ev      *chatEvent          `json:"ev,omitempty"`      // t=chat
	ID      string              `json:"id,omitempty"`      // confirm/ask/confirm_done/ask_done
	Reqs    []ui.ConfirmRequest `json:"reqs,omitempty"`    // t=confirm
	In      json.RawMessage     `json:"input,omitempty"`   // t=ask
}

// chatEvent 与前端 types.ts ChatEvent 对应（gui.ChatEvent 同构）。
type chatEvent struct {
	Type    string   `json:"type"`
	ID      string   `json:"id,omitempty"`
	Text    string   `json:"text,omitempty"`
	Display string   `json:"display,omitempty"`
	Body    string   `json:"body,omitempty"`
	OK      bool     `json:"ok,omitempty"`
	Start   bool     `json:"start,omitempty"`
	Current int      `json:"current,omitempty"`
	Total   int      `json:"total,omitempty"`
	Hosts   []string `json:"hosts,omitempty"`
}

type bufEvent struct {
	seq int
	f   wsFrame
}

type hub struct {
	mu     sync.Mutex
	seq    int
	buf    []bufEvent
	subs   map[chan wsFrame]struct{}
	onIdle func() // 订阅数 1→0 跳变时回调（取消待决议）
}

func newHub() *hub {
	return &hub{subs: map[chan wsFrame]struct{}{}}
}

// publish 入缓冲并 fan-out，返回该帧序号。发送在锁内进行：select/default
// 非阻塞投递不会持锁阻塞，而锁序化了并发 publish，保证每个订阅者按
// 序号顺序收到帧（乱序会让客户端误判断档、触发不必要的全量重连）。
func (h *hub) publish(f wsFrame) int {
	h.mu.Lock()
	h.seq++
	f.Seq = h.seq
	h.buf = append(h.buf, bufEvent{h.seq, f})
	if len(h.buf) > bufCap {
		h.buf = h.buf[len(h.buf)-bufCap:]
	}
	for ch := range h.subs {
		select {
		case ch <- f:
		default: // 慢消费者丢帧：客户端按序号断档全量重连
		}
	}
	h.mu.Unlock()
	return f.Seq
}

func (h *hub) publishChat(ev chatEvent) int {
	return h.publish(wsFrame{T: "chat", Ev: &ev})
}

func (h *hub) subscribe() chan wsFrame {
	ch := make(chan wsFrame, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan wsFrame) {
	h.mu.Lock()
	delete(h.subs, ch)
	idle := len(h.subs) == 0
	cb := h.onIdle
	h.mu.Unlock()
	if idle && cb != nil {
		cb() // 1→0：浏览器全关，阻塞中的确认/提问按拒绝/取消放行
	}
}

// snapshot 返回 (缓冲最旧序号, seq > since 的帧)。since 早于缓冲头时
// 调用方应先发 reset 帧让客户端清状态再应用全量重放。
func (h *hub) snapshot(since int) (oldest int, evs []bufEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.buf) > 0 {
		oldest = h.buf[0].seq
	}
	for _, e := range h.buf {
		if e.seq > since {
			evs = append(evs, e)
		}
	}
	return oldest, evs
}

func (h *hub) nextSeq() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// hasSubs 报告是否有在线订阅者（reaper 判定基准）。
func (h *hub) hasSubs() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) > 0
}

// reset 清空缓冲并向订阅者发 reset 帧（设置变更重建 Agent 后调用，
// 评审 ②-5）：客户端收到后清对话状态。序号继续递增不复位——缓冲已空，
// 旧 since 续传会因断档触发全量重连（此时缓冲空 = 无帧可补，客户端
// 恰好得到干净状态）；在线订阅者则立即收到 reset。
func (h *hub) reset() {
	h.mu.Lock()
	h.buf = nil
	h.seq++
	f := wsFrame{T: "reset", Seq: h.seq}
	for ch := range h.subs {
		select {
		case ch <- f:
		default:
		}
	}
	h.mu.Unlock()
}
