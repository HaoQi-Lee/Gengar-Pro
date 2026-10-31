package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFetchFileCopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.rdb")
	dst := filepath.Join(dir, "dst.rdb")
	content := []byte("REDIS0011-fixture-bytes")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewLocal(0).FetchFile(context.Background(), src, dst); err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: %q != %q", got, content)
	}
}

func TestLocalFetchFileMissingSource(t *testing.T) {
	l := NewLocal(0)
	err := l.FetchFile(context.Background(),
		filepath.Join(t.TempDir(), "nope.rdb"), filepath.Join(t.TempDir(), "out.rdb"))
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}
