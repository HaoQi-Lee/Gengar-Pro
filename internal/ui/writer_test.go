package ui

import (
	"fmt"
	"testing"
)

func TestLineWriterEmitsOneInfoPerLine(t *testing.T) {
	var got []string
	w := NewLineWriter(func(s string) { got = append(got, s) })

	fmt.Fprintln(w, "可用命令:")
	fmt.Fprintf(w, "  %-12s %s\n", "/help", "显示此帮助信息")
	fmt.Fprintf(w, "  %-12s %s\n", "/quit", "退出 Gengar")

	want := []string{
		"可用命令:",
		fmt.Sprintf("  %-12s %s", "/help", "显示此帮助信息"),
		fmt.Sprintf("  %-12s %s", "/quit", "退出 Gengar"),
	}
	if len(got) != len(want) {
		t.Fatalf("emit calls = %d, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestLineWriterReassemblesSplitLines: a single logical line fragmented across
// multiple Write calls must still emit as one line — the writer buffers until
// it sees a newline.
func TestLineWriterReassemblesSplitLines(t *testing.T) {
	var got []string
	w := NewLineWriter(func(s string) { got = append(got, s) })

	w.Write([]byte("partial"))
	if len(got) != 0 {
		t.Fatalf("should not emit before newline, got %q", got)
	}
	w.Write([]byte(" line\n"))

	if len(got) != 1 || got[0] != "partial line" {
		t.Fatalf("want single reassembled line %q, got %q", "partial line", got)
	}
}
