package webapi

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"gengar-pro/internal/ui"
)

func TestHubBufferAndReplay(t *testing.T) {
	h := newHub()
	s1 := h.subscribe()
	h.publishChat(chatEvent{Type: "info", Text: "a"})
	h.publishChat(chatEvent{Type: "info", Text: "b"})
	// 活跃订阅者实时收到
	got := <-s1
	if got.Ev == nil || got.Ev.Text != "a" || got.Seq != 1 {
		t.Fatalf("frame1 = %+v", got)
	}
	<-s1 // b

	// 重放：since=1 → 只有 seq 2
	oldest, evs := h.snapshot(1)
	if oldest != 1 || len(evs) != 1 || evs[0].f.Seq != 2 {
		t.Fatalf("snapshot(1) = oldest %d, %d events", oldest, len(evs))
	}
	// since=0 → 全量
	_, evs = h.snapshot(0)
	if len(evs) != 2 {
		t.Fatalf("snapshot(0) len = %d; want 2", len(evs))
	}
	h.unsubscribe(s1)
	// 退订后再发布不 panic、不送已退订通道
	h.publishChat(chatEvent{Type: "info", Text: "c"})
	// 非阻塞确认无帧到达已退订通道。仅覆盖顺序情形（退订先于发布返回）；
	// 并发窗口内的投递（publish 锁内投递与退订竞争）是安全且合法的，
	// 不在此断言。
	select {
	case f := <-s1:
		t.Fatalf("已退订通道收到帧 %+v", f)
	default:
	}
}

func TestHubBufferBounded(t *testing.T) {
	h := newHub()
	for i := 0; i < bufCap+50; i++ {
		h.publishChat(chatEvent{Type: "info"})
	}
	oldest, evs := h.snapshot(0)
	if len(evs) != bufCap || oldest != 51 {
		t.Fatalf("buffer len = %d, oldest = %d; want %d, 51", len(evs), oldest, bufCap)
	}
}

func TestHubIdleCallback(t *testing.T) {
	h := newHub()
	called := 0
	h.onIdle = func() { called++ }
	ch1 := h.subscribe()
	ch2 := h.subscribe()
	h.unsubscribe(ch1)
	if called != 0 {
		t.Fatalf("idle fired early: %d", called)
	}
	h.unsubscribe(ch2)
	if called != 1 {
		t.Fatalf("idle fired %d times; want 1（只在 1→0 跳变）", called)
	}
}

func TestSinkConfirmAskLifecycle(t *testing.T) {
	h := newHub()
	s := newUserSink(h, nil)
	sub := h.subscribe()

	res := make(chan bool, 1)
	go func() {
		res <- s.ConfirmBatch([]ui.ConfirmRequest{{ToolName: "shell", Summary: "rm x"}})
	}()
	var confirmID string
	for i := 0; i < 10; i++ {
		f := <-sub
		if f.T == "confirm" {
			confirmID = f.ID
			break
		}
	}
	if confirmID == "" {
		t.Fatal("未收到 confirm 帧")
	}
	// 断连（1→0）→ 取消 → ConfirmBatch 返回 false
	h.unsubscribe(sub)
	if <-res {
		t.Fatal("ConfirmBatch after disconnect should be false")
	}

	// ask：正常决议路径
	sub2 := h.subscribe()
	resAsk := make(chan string, 1)
	go func() {
		ans, err := s.AskUser([]byte(`{"question":"q"}`))
		if err != nil {
			resAsk <- "err:" + err.Error()
		} else {
			resAsk <- ans
		}
	}()
	f := <-sub2
	if f.T != "ask" || f.ID == "" {
		t.Fatalf("ask frame = %+v", f)
	}
	if !s.ResolveAsk(f.ID, "2") {
		t.Fatal("ResolveAsk should succeed")
	}
	// 重复决议 → false
	if s.ResolveAsk(f.ID, "3") {
		t.Fatal("double ResolveAsk should fail")
	}
	// 决议后 ask_done 帧已入缓冲（重放终态正确）
	oldest, evs := h.snapshot(0)
	_ = oldest
	var sawAskDone bool
	for _, e := range evs {
		if e.f.T == "ask_done" && e.f.ID == f.ID {
			sawAskDone = true
		}
	}
	if !sawAskDone {
		t.Fatal("ask_done 未入缓冲")
	}
	if got := <-resAsk; got != "2" {
		t.Fatalf("AskUser = %q; want 2", got)
	}
	h.unsubscribe(sub2)
}

// TestResolveCancelRace 并发压力：同一 pending 上 N 个 Resolve 与 M 个
// CancelPendings 同时竞争，验证单认领属性（T2.3 依赖的并发不变量）：
// ConfirmBatch/AskUser 恰好被放行一次（缓冲里恰一个 *_done 帧）、至多
// 一个 Resolve 成功、无 panic。Cancel 可能抢到认领——此时 0 个 Resolve
// 返回 true 属合法结果，故断言「≤1 个 true + 恰一个 done 帧」（每次
// 认领恰发布一个 done 帧、恰放行一次阻塞方，二者合起来即恰一次认领）
// 而非「恰一个 true」。循环 50 次放大竞争窗口（按仓库约定不加 -race）。
func TestResolveCancelRace(t *testing.T) {
	const (
		resolvers = 8
		cancels   = 4
		iters     = 50
	)
	for iter := 0; iter < iters; iter++ {
		h := newHub()
		s := newUserSink(h, nil)
		sub := h.subscribe() // 挂着不退订：不触发 onIdle，取消只来自 racer

		resConfirm := make(chan bool, 1)
		go func() {
			resConfirm <- s.ConfirmBatch([]ui.ConfirmRequest{{ToolName: "shell"}})
		}()
		confirmID := recvFrame(t, sub).ID
		resAsk := make(chan string, 1)
		go func() {
			ans, err := s.AskUser([]byte(`{"question":"q"}`))
			if err != nil {
				resAsk <- "err:" + err.Error()
			} else {
				resAsk <- ans
			}
		}()
		askID := recvFrame(t, sub).ID

		start := make(chan struct{})
		var wg sync.WaitGroup
		confirmOKs := make(chan bool, resolvers)
		askOKs := make(chan bool, resolvers)
		for i := 0; i < resolvers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				confirmOKs <- s.ResolveConfirm(confirmID, i%2 == 0)
			}(i)
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				askOKs <- s.ResolveAsk(askID, fmt.Sprintf("ans-%d", i))
			}(i)
		}
		for i := 0; i < cancels; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				s.CancelPendings()
			}()
		}
		close(start)

		wgDone := make(chan struct{})
		go func() { wg.Wait(); close(wgDone) }()
		select {
		case <-wgDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: racer 未全部返回（发送阻塞 = 双认领）", iter)
		}
		close(confirmOKs)
		close(askOKs)

		confirmTrue, askTrue := 0, 0
		for ok := range confirmOKs {
			if ok {
				confirmTrue++
			}
		}
		for ok := range askOKs {
			if ok {
				askTrue++
			}
		}
		if confirmTrue > 1 || askTrue > 1 {
			t.Fatalf("iter %d: 同一 pending 被多次 Resolve（confirm=%d, ask=%d）", iter, confirmTrue, askTrue)
		}

		// 恰一次认领：每个 id 恰一个 *_done 帧入缓冲（认领方发布）
		_, evs := h.snapshot(0)
		confirmDone, askDone := 0, 0
		for _, e := range evs {
			switch {
			case e.f.T == "confirm_done" && e.f.ID == confirmID:
				confirmDone++
			case e.f.T == "ask_done" && e.f.ID == askID:
				askDone++
			}
		}
		if confirmDone != 1 || askDone != 1 {
			t.Fatalf("iter %d: confirm_done=%d ask_done=%d; want 1,1（恰一次认领）", iter, confirmDone, askDone)
		}

		// ConfirmBatch/AskUser 恰好返回一次（不挂死、不双发）
		select {
		case <-resConfirm:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: ConfirmBatch 未被放行", iter)
		}
		select {
		case <-resAsk:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: AskUser 未被放行", iter)
		}
	}
}

// recvFrame 带超时收帧，回归时快速失败而不是挂死整套测试。
func recvFrame(t *testing.T, sub chan wsFrame) wsFrame {
	t.Helper()
	select {
	case f := <-sub:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("5s 内未收到帧")
		return wsFrame{}
	}
}
