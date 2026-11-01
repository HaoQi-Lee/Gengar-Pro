//go:build e2e

package webapi

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitHealth 两条失败路径的无进程单测（等待循环抽成返回 error 的方法
// 正是为此）：超时与进程早退。手工构造 serverProc——done 为 nil channel
// 时 select 恒阻塞，即「进程还活着」；已 close 则是「已退出」。

func TestWaitHealthTimeout(t *testing.T) {
	// 取一个刚释放的端口当「永远连不上的地址」：比写死端口稳（不撞他者）。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	p := &serverProc{
		baseURL: "http://127.0.0.1:" + strconv.Itoa(port),
		done:    make(chan struct{}), // 不关 = 进程活着
	}
	err = p.waitHealth(400 * time.Millisecond)
	if err == nil {
		t.Fatal("无人监听的 health 应超时报错")
	}
	for _, want := range []string{"超时", "health"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("超时错误 %q 不含 %q", err.Error(), want)
		}
	}
}

func TestWaitHealthEarlyExit(t *testing.T) {
	p := &serverProc{baseURL: "http://127.0.0.1:1", done: make(chan struct{})}
	p.out.Write([]byte("2026/11/01 log.Fatalf: 端口已被占用\n"))
	close(p.done) // Wait goroutine 的等价物：进程已退出
	// 1 端口连不通，但早退分支必须先于 10s 超时立即触发。
	err := p.waitHealth(10 * time.Second)
	if err == nil {
		t.Fatal("进程早退应立即报错而非等满超时")
	}
	for _, want := range []string{"提前退出", "端口已被占用"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("早退错误 %q 不含 %q", err.Error(), want)
		}
	}
}

func TestFreePort(t *testing.T) {
	port := freePort(t)
	if port <= 0 {
		t.Fatalf("freePort = %d; want >0", port)
	}
	// 返回的端口必须真的已释放（可再绑定）。
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("bind freePort %d: %v", port, err)
	}
	l.Close()
}
