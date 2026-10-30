// userSink 是 ui.Sink 的 web 实现：ChatEvent 推 hub；ConfirmBatch/AskUser
// 阻塞等待 REST 决议回填，浏览器全部断开时按拒绝/取消放行（否则 Agent
// goroutine 与任务锁会永远卡住）。
package webapi

import (
	"encoding/json"
	"fmt"
	"sync"

	"gengar-pro/internal/ui"
)

type pendingCh struct {
	chConfirm chan bool
	chAnswer  chan string
}

type userSink struct {
	hub *hub

	pm       sync.Mutex
	nextID   int
	pendings map[string]*pendingCh
}

// newUserSink 创建 sink 并接线 hub 的 onIdle：订阅数 1→0（浏览器全关）
// 时取消全部待决议。接线须在 hub 可被其他 goroutine 访问前完成——
// unsubscribe 在锁内读 onIdle，构造后的无锁写会与其竞争。
func newUserSink(h *hub) *userSink {
	s := &userSink{hub: h, pendings: map[string]*pendingCh{}}
	h.onIdle = s.CancelPendings
	return s
}

func (s *userSink) emit(ev chatEvent) { s.hub.publishChat(ev) }

func (s *userSink) UserSubmit(text string) {
	s.emit(chatEvent{Type: "info", Text: text})
}

func (s *userSink) Thinking(start bool) { s.emit(chatEvent{Type: "thinking", Start: start}) }

func (s *userSink) AssistantTextDelta(delta string) { s.emit(chatEvent{Type: "delta", Text: delta}) }
func (s *userSink) AssistantTextEnd()               { s.emit(chatEvent{Type: "text_end"}) }

func (s *userSink) ToolCallStart(id, display string) {
	s.emit(chatEvent{Type: "tool_start", ID: id, Display: display})
}

func (s *userSink) ToolCallResult(id, body string, ok bool) {
	s.emit(chatEvent{Type: "tool_result", ID: id, Body: body, OK: ok})
}

func (s *userSink) Step(current, total int) {
	s.emit(chatEvent{Type: "step", Current: current, Total: total})
}

func (s *userSink) Forbidden(reason string) {
	s.emit(chatEvent{Type: "error", Text: "禁止: " + reason})
}
func (s *userSink) Warning(msg string) { s.emit(chatEvent{Type: "warning", Text: msg}) }
func (s *userSink) Error(msg string)   { s.emit(chatEvent{Type: "error", Text: msg}) }
func (s *userSink) Info(text string)   { s.emit(chatEvent{Type: "info", Text: text}) }
func (s *userSink) Close()             {}

// EmitDone 通知任务结束（webapi 在任务 goroutine 收尾调用）。
func (s *userSink) EmitDone() { s.emit(chatEvent{Type: "done"}) }

func (s *userSink) reg(p *pendingCh) string {
	s.pm.Lock()
	defer s.pm.Unlock()
	id := fmt.Sprintf("p%d", s.nextID)
	s.nextID++
	s.pendings[id] = p
	return id
}

func (s *userSink) unreg(id string) {
	s.pm.Lock()
	defer s.pm.Unlock()
	delete(s.pendings, id)
}

// ConfirmBatch 推 confirm 帧（进缓冲——刷新后重放会重新弹窗）并阻塞等决议。
// 决议结果同样入缓冲（confirm_done），重放时先弹后收，终态正确。
// ConfirmAutoApprove 置位时直接放行（ui.Sink 契约；服务进程恒不置位，
// 供未来自动化路径复用 webapi Sink 时不至于挂死）。
func (s *userSink) ConfirmBatch(reqs []ui.ConfirmRequest) bool {
	if ui.ConfirmAutoApprove {
		return true
	}
	p := &pendingCh{chConfirm: make(chan bool, 1)}
	id := s.reg(p)
	defer s.unreg(id)
	s.hub.publish(wsFrame{T: "confirm", ID: id, Reqs: reqs})
	return <-p.chConfirm
}

func (s *userSink) AskUser(input json.RawMessage) (string, error) {
	if ui.ConfirmAutoApprove { // 同上：自动选第一个选项（与 plain.Sink 同语义）
		var p struct {
			Options []struct {
				Label string `json:"label"`
			} `json:"options"`
		}
		if err := json.Unmarshal(input, &p); err != nil || len(p.Options) == 0 {
			return "", fmt.Errorf("ask_user: no options")
		}
		return p.Options[0].Label, nil
	}
	p := &pendingCh{chAnswer: make(chan string, 1)}
	id := s.reg(p)
	defer s.unreg(id)
	s.hub.publish(wsFrame{T: "ask", ID: id, In: input})
	return <-p.chAnswer, nil
}

// ResolveConfirm REST 决议回填；确认结果入缓冲。决议在锁内原子认领
// （删除）该 pending，保证只决议一次——不存在（已决议/已取消）返回 false。
func (s *userSink) ResolveConfirm(id string, approved bool) bool {
	s.pm.Lock()
	p, ok := s.pendings[id]
	if ok {
		delete(s.pendings, id)
	}
	s.pm.Unlock()
	if !ok {
		return false
	}
	s.hub.publish(wsFrame{T: "confirm_done", ID: id})
	p.chConfirm <- approved
	return true
}

// ResolveAsk 同 ResolveConfirm：锁内认领后回填，重复决议返回 false。
func (s *userSink) ResolveAsk(id, answer string) bool {
	s.pm.Lock()
	p, ok := s.pendings[id]
	if ok {
		delete(s.pendings, id)
	}
	s.pm.Unlock()
	if !ok {
		return false
	}
	s.hub.publish(wsFrame{T: "ask_done", ID: id})
	p.chAnswer <- answer
	return true
}

// CancelPendings 断连兜底：全部确认按拒绝、提问按空答案（=取消）放行。
// 每个 pending 恰有一个非 nil 通道（构造时只建自己那类）。done 帧先于
// 通道发送发布：发送会即时唤醒阻塞的任务 goroutine，其后续帧（EmitDone
// 等）可能抢在 done 帧前入缓冲，重放序错乱（客户端先见任务收尾、后见
// 确认框关闭）。阻塞发送安全：pm 锁认领互斥保证每通道恰一个发送者
// （与 Resolve* 同理），接收方阻塞等待、缓冲 1 永不阻塞。
func (s *userSink) CancelPendings() {
	s.pm.Lock()
	ps := make([]*pendingCh, 0, len(s.pendings))
	ids := make([]string, 0, len(s.pendings))
	for id, p := range s.pendings {
		ps = append(ps, p)
		ids = append(ids, id)
	}
	s.pendings = map[string]*pendingCh{}
	s.pm.Unlock()
	for i, p := range ps {
		if p.chConfirm != nil {
			s.hub.publish(wsFrame{T: "confirm_done", ID: ids[i]})
			p.chConfirm <- false
		} else {
			s.hub.publish(wsFrame{T: "ask_done", ID: ids[i]})
			p.chAnswer <- ""
		}
	}
}

// 编译期断言：userSink 实现 ui.Sink。
var _ ui.Sink = (*userSink)(nil)
