package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// shardPathOf 返回测试用的当日分片路径（base 形如 <dir>/audit）。
func shardPathOf(base string) string {
	return base + "-" + time.Now().Format("2006-01-02") + ".jsonl"
}

func TestAuditWritesJSONL(t *testing.T) {
	base := filepath.Join(t.TempDir(), "audit")
	l, err := Open(base + ".jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := l.Log(Entry{TS: "2026-10-20T00:00:00Z", Session: "s1", Tool: "execute_command",
		Input: []byte(`{"command":"df -h"}`), Hosts: []string{"web-01", "web-02"}, Approved: true}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(shardPathOf(base))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got struct {
		TS       string          `json:"ts"`
		Session  string          `json:"session"`
		Tool     string          `json:"tool"`
		Input    json.RawMessage `json:"input"`
		Hosts    []string        `json:"hosts"`
		Approved bool            `json:"approved"`
		IsError  bool            `json:"is_error"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, raw)
	}
	if got.TS != "2026-10-20T00:00:00Z" {
		t.Errorf("ts = %q, want %q", got.TS, "2026-10-20T00:00:00Z")
	}
	if got.Session != "s1" {
		t.Errorf("session = %q, want %q", got.Session, "s1")
	}
	if got.Tool != "execute_command" {
		t.Errorf("tool = %q, want %q", got.Tool, "execute_command")
	}
	if string(got.Input) != `{"command":"df -h"}` {
		t.Errorf("input = %s, want %s", got.Input, `{"command":"df -h"}`)
	}
	if want := []string{"web-01", "web-02"}; !slices.Equal(got.Hosts, want) {
		t.Errorf("hosts = %v, want %v", got.Hosts, want)
	}
	if !got.Approved {
		t.Error("approved = false, want true")
	}
	if got.IsError {
		t.Error("is_error = true, want false")
	}
}

func TestAuditAppendAcrossReopen(t *testing.T) {
	base := filepath.Join(t.TempDir(), "audit")
	l1, _ := Open(base + ".jsonl")
	_ = l1.Log(Entry{TS: "t1", Session: "s1", Tool: "a"})
	_ = l1.Close()
	l2, _ := Open(base + ".jsonl")
	_ = l2.Log(Entry{TS: "t2", Session: "s1", Tool: "b"})
	_ = l2.Close()
	raw, _ := os.ReadFile(shardPathOf(base))
	if got := strings.Count(string(raw), "\n"); got != 2 {
		t.Fatalf("expected 2 lines after reopen append, got %d", got)
	}
}

func TestAuditConcurrentLog(t *testing.T) {
	base := filepath.Join(t.TempDir(), "audit")
	l, _ := Open(base + ".jsonl")
	defer l.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_ = l.Log(Entry{TS: "t", Session: "s", Tool: "x"})
			}
		}(i)
	}
	wg.Wait()
	f, _ := os.Open(shardPathOf(base))
	defer f.Close()
	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		count++
	}
	if count != 200 {
		t.Fatalf("expected 200 entries, got %d", count)
	}
}

// TestAuditPrunesOldShards: 评审 ③-3 轮转——超过保留期的分片被清理，
// 当日分片保留。
func TestAuditPrunesOldShards(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "audit")
	today := time.Now().Format("2006-01-02")
	old := time.Now().AddDate(0, 0, -40).Format("2006-01-02") // 超 30 天保留期
	recent := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	for _, d := range []string{old, recent, today} {
		if err := os.WriteFile(base+"-"+d+".jsonl", []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Open(base + ".jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer l.Close()
	if _, err := os.Stat(base + "-" + old + ".jsonl"); !os.IsNotExist(err) {
		t.Fatalf("40 天前的分片应被清理, stat err = %v", err)
	}
	if _, err := os.Stat(base + "-" + recent + ".jsonl"); err != nil {
		t.Fatalf("3 天前的分片应保留: %v", err)
	}
	if _, err := os.Stat(base + "-" + today + ".jsonl"); err != nil {
		t.Fatalf("当日分片应保留: %v", err)
	}
}
