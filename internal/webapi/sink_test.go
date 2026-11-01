// sink_test.go userSink 待决议的取消计数与 onIdle 包装契约（Task 5：
// ws_auto_cancel 审计据此判定 n>0 才记）。生命周期/竞争用例见 hub_test.go。
package webapi

import "testing"

// TestCancelPendingsCount CancelPendings 返回实际取消数：空表 0；一个
// confirm + 一个 ask → 2，且通道各收 false/""（断连放行语义回归）；二次
// 调用幂等返回 0。pending 经 reg 直注（同包私有可达），免去
// ConfirmBatch/AskUser 的 goroutine 编排。
func TestCancelPendingsCount(t *testing.T) {
	s := newUserSink(newHub(), nil)

	if n := s.CancelPendings(); n != 0 {
		t.Fatalf("空 pending 返回 %d; want 0", n)
	}

	pConfirm := &pendingCh{chConfirm: make(chan bool, 1)}
	pAsk := &pendingCh{chAnswer: make(chan string, 1)}
	s.reg(pConfirm)
	s.reg(pAsk)

	if n := s.CancelPendings(); n != 2 {
		t.Fatalf("2 pending 返回 %d; want 2", n)
	}
	if ok := <-pConfirm.chConfirm; ok {
		t.Fatal("confirm 应按拒绝放行（false）")
	}
	if ans := <-pAsk.chAnswer; ans != "" {
		t.Fatalf("ask 应按空答案放行, got %q", ans)
	}
	if n := s.CancelPendings(); n != 0 {
		t.Fatalf("二次取消返回 %d; want 0（认领后表已空）", n)
	}
}

// TestNewUserSinkOnIdleCallback onIdle 包装契约：取消数 n>0 才回调、
// n=0 不回调、nil 回调安全。回调串行发生在 unsubscribe 调用内（hub 锁
// 外），无并发写 got。Server 级 ws_auto_cancel 审计在 chat_test
// （TestWSAutoCancelAudit）端到端覆盖，此处钉 sink 层闸门语义。
func TestNewUserSinkOnIdleCallback(t *testing.T) {
	h := newHub()
	var got []int
	s := newUserSink(h, func(n int) { got = append(got, n) })

	// 无 pending 的 1→0：不回调（ws_auto_cancel n=0 不记的依据）
	sub := h.subscribe()
	h.unsubscribe(sub)
	if len(got) != 0 {
		t.Fatalf("无 pending 不应回调: %v", got)
	}

	// 1 个 pending → 回调 n=1
	sub = h.subscribe()
	p := &pendingCh{chConfirm: make(chan bool, 1)}
	s.reg(p)
	h.unsubscribe(sub)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("回调 = %v; want [1]", got)
	}
	if ok := <-p.chConfirm; ok {
		t.Fatal("confirm 应按拒绝放行")
	}

	// nil 回调不 panic（TestSinkConfirmAskLifecycle 亦覆盖，此处显式钉）
	s2 := newUserSink(newHub(), nil)
	sub2 := s2.hub.subscribe()
	p2 := &pendingCh{chAnswer: make(chan string, 1)}
	s2.reg(p2)
	s2.hub.unsubscribe(sub2)
	if ans := <-p2.chAnswer; ans != "" {
		t.Fatalf("ask 应按空答案放行, got %q", ans)
	}
}
