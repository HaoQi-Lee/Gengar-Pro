package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalRunArgvEcho(t *testing.T) {
	l := NewLocal(5 * time.Second)
	out, err := l.RunArgv(context.Background(), "echo", "hello", "world")
	if err != nil || !strings.Contains(string(out), "hello world") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestLocalArgvNoShellInterp(t *testing.T) {
	// argv 形式不经过 shell：真正的 shell 会把 $(echo MIDDLE) 替换成
	// MIDDLE；argv exec 把整串作为单个 literal 参数原样打印。
	// 注意字面量本身含 "MIDDLE"，故断言必须针对展开后的形态。
	l := NewLocal(5 * time.Second)
	out, err := l.RunArgv(context.Background(), "echo", "before $(echo MIDDLE) after")
	got := string(out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 字面 $(...) 必须保留 —— 若被 shell 展开则会消失
	if !strings.Contains(got, "$(echo MIDDLE)") {
		t.Fatalf("expected literal $(echo MIDDLE) to survive argv exec: out=%q", got)
	}
	// 确保命令替换没有发生：输出不应是 "before MIDDLE after"
	if strings.Contains(got, "before MIDDLE after") {
		t.Fatalf("command substitution was performed — shell injection detected: out=%q", got)
	}
}

func TestLocalRunShell(t *testing.T) {
	l := NewLocal(5 * time.Second)
	out, err := l.RunShell(context.Background(), "echo a && echo b")
	if err != nil || !strings.Contains(string(out), "a\nb") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestLocalWriteFile(t *testing.T) {
	l := NewLocal(5 * time.Second)
	path := t.TempDir() + "/w.txt"
	if err := l.WriteFile(context.Background(), path, []byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != "hi" {
		t.Fatalf("content = %q, want %q", raw, "hi")
	}
}

func TestLocalTimeout(t *testing.T) {
	l := NewLocal(100 * time.Millisecond)
	start := time.Now()
	_, err := l.RunArgv(context.Background(), "sleep", "5")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// 兜底超时必须及时生效：允许调度抖动，但不允许等到远端命令自然结束
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took %v, want well under 2s", elapsed)
	}
}

func TestLocalArgvMissingBinary(t *testing.T) {
	l := NewLocal(5 * time.Second)
	out, err := l.RunArgv(context.Background(), "definitely_not_a_real_binary_xyz")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if len(out) != 0 {
		t.Fatalf("expected empty output for missing binary, got %q", out)
	}
}

func TestLocalZeroTimeout(t *testing.T) {
	// timeout 零值 = 不设兜底超时（由调用方 ctx 全权决定）：
	// 普通命令必须照常完成；若误用 WithTimeout(0) 会立即过期杀死命令。
	l := NewLocal(0)
	out, err := l.RunShell(context.Background(), "echo ok")
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
