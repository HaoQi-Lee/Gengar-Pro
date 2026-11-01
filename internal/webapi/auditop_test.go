package webapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gengar-pro/internal/audit"
)

// openTestAudit 开临时目录审计 logger 并注入 s.audit（沿用 chat_test
// TestUserAuditLog 的 Open+读回套路；读回用 CurrentShard 免去手拼日期）。
func openTestAudit(t *testing.T, s *Server) *audit.Logger {
	t.Helper()
	l, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s.audit = l
	return l
}

// readAuditEntries 读回当前分片全部条目（JSONL → []audit.Entry）。
func readAuditEntries(t *testing.T, l *audit.Logger) []audit.Entry {
	t.Helper()
	raw, err := os.ReadFile(l.CurrentShard())
	if err != nil {
		t.Fatalf("读审计分片: %v", err)
	}
	var es []audit.Entry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e audit.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("审计行解析失败 %q: %v", line, err)
		}
		es = append(es, e)
	}
	return es
}

func TestAuditOp(t *testing.T) {
	s := &Server{}
	l := openTestAudit(t, s)

	// 1) 带 map input：User/Tool/Output/Input/TS 各就各位
	s.auditOp("alice", "login_ok", "user=alice ip=1.2.3.4", map[string]string{"k": "v"})
	es := readAuditEntries(t, l)
	if len(es) != 1 {
		t.Fatalf("条数 = %d; want 1", len(es))
	}
	e := es[0]
	if e.User != "alice" || e.Tool != "login_ok" || e.Output != "user=alice ip=1.2.3.4" {
		t.Fatalf("字段不符: %+v", e)
	}
	if got := string(e.Input); got != `{"k":"v"}` {
		t.Fatalf("Input = %s; want %s", got, `{"k":"v"}`)
	}
	if e.TS == "" {
		t.Fatal("TS 为空")
	}

	// 2) input=nil → Input 序列化为 JSON null，条数仍 1（追加）
	s.auditOp("bob", "term_open", "user=bob", nil)
	es = readAuditEntries(t, l)
	if len(es) != 2 {
		t.Fatalf("条数 = %d; want 2", len(es))
	}
	if got := string(es[1].Input); got != "null" {
		t.Fatalf("nil Input 读回 = %s; want null", got)
	}
}

func TestAuditOpNilLogger(t *testing.T) {
	s := &Server{audit: nil}
	s.auditOp("a", "b", "c", nil) // 不得 panic
}

// assertAuditNoSecrets 凭据红线反向断言（hosts/settings 等审计测试共用）：
// 全部条目的 Input/Output 全文不得含 marks 中任一标记。
func assertAuditNoSecrets(t *testing.T, es []audit.Entry, marks ...string) {
	t.Helper()
	for _, e := range es {
		blob := e.Output + string(e.Input)
		for _, mark := range marks {
			if strings.Contains(blob, mark) {
				t.Fatalf("审计条目泄凭据 %q: %+v", mark, e)
			}
		}
	}
}

// TestTruncRunes 截断语义四情形：短串/恰好边界原样返回；超限按 rune
// 截断（多字节不切半）并以省略号收尾。
func TestTruncRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},    // 短串原样
		{"abcde", 5, "abcde"},     // 恰好边界原样（不误加省略号）
		{"abcdef", 5, "abcde…"},   // ASCII 超限：截断 + 省略号
		{"中文按字截断不切半", 4, "中文按字…"}, // 多字节：截在字边界 + 省略号
	}
	for _, c := range cases {
		if got := truncRunes(c.in, c.n); got != c.want {
			t.Fatalf("truncRunes(%q, %d) = %q; want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/login", nil)
	cases := []struct{ remote, want string }{
		{"1.2.3.4:5678", "1.2.3.4"},
		{"[::1]:80", "::1"},
		{"no-port", "no-port"}, // 解析失败原串返回
	}
	for _, c := range cases {
		r.RemoteAddr = c.remote
		if got := clientIP(r); got != c.want {
			t.Fatalf("clientIP(%q) = %q; want %q", c.remote, got, c.want)
		}
	}
}
