package ui

import (
	"encoding/json"
	"testing"
)

func TestTruncateMiddle(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 10, "abc"},       // 不超长：原样返回
		{"abc", 3, "abc"},        // 恰好等于 max：原样返回
		{"abcdef", 5, "ab…ef"},   // 超长：head=2, tail=2
		{"abcdefgh", 5, "ab…gh"}, // head=(5-1)/2=2, tail=5-1-2=2
		{"abcdefg", 4, "a…fg"},   // head=1, tail=2
		{"abc", 1, "…"},          // max==1 退化
		{"abc", 0, ""},           // max<=0 返回空
		{"abc", -1, ""},
		{"你好世界你好", 5, "你好…你好"}, // CJK 按 rune 计数
	}
	for _, c := range cases {
		got := TruncateMiddle(c.in, c.max)
		if got != c.want {
			t.Errorf("TruncateMiddle(%q,%d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestFormatConfirmSummary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"execute_command", `{"command":"systemctl restart nginx"}`, "systemctl restart nginx"},
		{"execute_command", `{"command":""}`, `execute_command({"command":""})`},
		{"read_file", `{"path":"/etc/hosts"}`, "/etc/hosts"},
		{"write_file", `{"path":"/tmp/x"}`, "/tmp/x"},
		{"edit_file", `{"path":"/tmp/y"}`, "/tmp/y"},
		{"unknown_tool", `{"foo":"bar"}`, `unknown_tool({"foo":"bar"})`},
	}
	for _, c := range cases {
		got := FormatConfirmSummary(c.name, json.RawMessage(c.in))
		if got != c.want {
			t.Errorf("FormatConfirmSummary(%q,%s) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
