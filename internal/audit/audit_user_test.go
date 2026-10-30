package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEntryUserField(t *testing.T) {
	base := filepath.Join(t.TempDir(), "audit")
	l, err := Open(base + ".jsonl")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := l.Log(Entry{TS: "t", User: "alice", Tool: "term_open"}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	shard := base + "-" + time.Now().Format("2006-01-02") + ".jsonl"
	raw, err := os.ReadFile(shard)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.User != "alice" {
		t.Fatalf("User = %q; want alice", e.User)
	}
	// headless/旧行为：User 为空时 omitempty 不出现在 JSON
	l2, _ := Open(base + ".jsonl")
	_ = l2.Log(Entry{TS: "t", Tool: "x"})
	_ = l2.Close()
	raw, _ = os.ReadFile(shard)
	lines := string(raw)
	if count := strings.Count(lines, `"user"`); count != 1 {
		t.Fatalf(`"user" 出现 %d 次; want 1（仅第一条）`, count)
	}
}
