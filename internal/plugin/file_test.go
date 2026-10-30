package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileManager_Tools(t *testing.T) {
	fm := NewFileManager()
	if len(fm.Tools()) != 5 {
		t.Fatalf("Tools() count = %d, want 5", len(fm.Tools()))
	}
	got := map[string]int{}
	for _, tool := range fm.Tools() {
		got[tool.Name] = int(tool.RiskLevel)
		if !tool.Targeting {
			t.Errorf("%s Targeting = false, want true", tool.Name)
		}
	}
	wantSafe := []string{"read_file", "list_directory", "find_files", "grep_files"}
	for _, name := range wantSafe {
		if _, ok := got[name]; !ok {
			t.Errorf("missing tool: %s", name)
		}
		if got[name] != 0 {
			t.Errorf("%s RiskLevel = %d, want 0 (Safe)", name, got[name])
		}
	}
	if got["write_file"] != 1 {
		t.Errorf("write_file RiskLevel = %d, want 1 (Risky)", got["write_file"])
	}
}

func TestFileManager_ReadFile(t *testing.T) {
	fm := NewFileManager()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("file content here"), nil
	}}
	result, err := fm.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"/etc/hosts"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "cat" {
		t.Errorf("expected cat, got %q", capturedName)
	}
	if len(capturedArgs) != 1 || capturedArgs[0] != "/etc/hosts" {
		t.Errorf("expected single arg /etc/hosts, got %v", capturedArgs)
	}
}

func TestFileManager_ReadFileInjectionSafe(t *testing.T) {
	fm := NewFileManager()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := fm.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"/tmp/x; rm -rf /"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(capturedArgs) != 1 || capturedArgs[0] != "/tmp/x; rm -rf /" {
		t.Errorf("malicious path should be one literal arg, got %v", capturedArgs)
	}
}

func TestFileManager_WriteFile(t *testing.T) {
	fm := NewFileManager()
	var mkdirCalled bool
	var mkdirArgs []string
	fake := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "mkdir" {
				mkdirCalled = true
				mkdirArgs = args
			}
			return []byte(""), nil
		},
	}
	var writeFileCalled bool
	var writtenPath string
	var writtenContent []byte
	fake.writeFn = func(_ context.Context, path string, content []byte) error {
		writeFileCalled = true
		writtenPath = path
		writtenContent = content
		return nil
	}
	result, err := fm.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"/tmp/test.txt","content":"hello"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if !mkdirCalled {
		t.Error("write_file should invoke mkdir -p before writing")
	}
	if !hasArgvPair(mkdirArgs, "-p", filepath.Dir("/tmp/test.txt")) {
		t.Errorf("expected 'mkdir -p %s', got args %v", filepath.Dir("/tmp/test.txt"), mkdirArgs)
	}
	if !writeFileCalled {
		t.Error("write_file should invoke WriteFile")
	}
	if writtenPath != "/tmp/test.txt" {
		t.Errorf("writtenPath = %q, want /tmp/test.txt", writtenPath)
	}
	if string(writtenContent) != "hello" {
		t.Errorf("writtenContent = %q, want hello", writtenContent)
	}
}

func TestFileManager_WriteFileMkdirFails(t *testing.T) {
	fm := NewFileManager()
	fake := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			// mkdir 失败
			return []byte("permission denied"), fmt.Errorf("exit status 1")
		},
	}
	writeFileCalled := false
	fake.writeFn = func(_ context.Context, path string, content []byte) error {
		writeFileCalled = true
		return nil
	}
	result, err := fm.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"/forbidden/test.txt","content":"hello"}`), fake)
	if err != nil {
		t.Fatalf("Execute should not return Go error: %v", err)
	}
	if !result.IsError {
		t.Error("IsError should be true when mkdir fails")
	}
	if writeFileCalled {
		t.Error("WriteFile must NOT be called after mkdir failure")
	}
}

func TestFileManager_WriteFileWriteFails(t *testing.T) {
	fm := NewFileManager()
	fake := &fakeExec{
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			// mkdir 成功
			return []byte(""), nil
		},
		writeFn: func(_ context.Context, path string, content []byte) error {
			return fmt.Errorf("disk full")
		},
	}
	result, err := fm.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"/tmp/test.txt","content":"hello"}`), fake)
	if err != nil {
		t.Fatalf("Execute should not return Go error: %v", err)
	}
	if !result.IsError {
		t.Error("IsError should be true when WriteFile fails")
	}
}

func TestFileManager_ListDirectory(t *testing.T) {
	fm := NewFileManager()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("total 0\ndrwxr-xr-x  3 root root  4096 Jan  1 00:00 ."), nil
	}}
	result, err := fm.Execute(context.Background(), "list_directory", json.RawMessage(`{"path":"/var/log"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedName != "ls" {
		t.Errorf("expected ls, got %q", capturedName)
	}
	if !argvContains(capturedArgs, "/var/log") {
		t.Errorf("expected /var/log as literal arg, got %v", capturedArgs)
	}
	if !strings.Contains(result.Content, "total 0") {
		t.Errorf("Content should contain ls output, got: %q", result.Content)
	}
}

func TestFileManager_FindFiles(t *testing.T) {
	fm := NewFileManager()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("/etc/nginx/nginx.conf\n/etc/nginx/conf.d/default.conf"), nil
	}}
	result, err := fm.Execute(context.Background(), "find_files", json.RawMessage(`{"path":"/etc/nginx","name":"*.conf","max_depth":3}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "find" {
		t.Errorf("expected find, got %q", capturedName)
	}
	if !hasArgvPair(capturedArgs, "-maxdepth", "3") {
		t.Errorf("expected '-maxdepth' '3' pair in args, got %v", capturedArgs)
	}
	if !hasArgvPair(capturedArgs, "-name", "*.conf") {
		t.Errorf("expected '-name' '*.conf' pair in args, got %v", capturedArgs)
	}
	if capturedArgs[0] != "/etc/nginx" {
		t.Errorf("first arg should be path, got %q", capturedArgs[0])
	}
}

func TestFileManager_FindFilesDefaultDepth(t *testing.T) {
	fm := NewFileManager()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := fm.Execute(context.Background(), "find_files", json.RawMessage(`{"path":"/etc","name":"*.conf"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !hasArgvPair(capturedArgs, "-maxdepth", "3") {
		t.Errorf("default max_depth should produce '-maxdepth' '3' pair, got %v", capturedArgs)
	}
}

func TestFileManager_GrepFiles(t *testing.T) {
	fm := NewFileManager()
	var capturedName string
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedName = name
		capturedArgs = args
		return []byte("/etc/hosts:3:127.0.0.1\tlocalhost"), nil
	}}
	result, err := fm.Execute(context.Background(), "grep_files", json.RawMessage(`{"pattern":"localhost","path":"/etc/hosts","recursive":false,"ignore_case":false}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
	if capturedName != "grep" {
		t.Errorf("expected grep, got %q", capturedName)
	}
	if !argvContains(capturedArgs, "-nE") {
		t.Errorf("expected '-nE' as literal arg, got %v", capturedArgs)
	}
	if argvContains(capturedArgs, "-r") {
		t.Errorf("recursive=false but -r appeared in args: %v", capturedArgs)
	}
	if argvContains(capturedArgs, "-i") {
		t.Errorf("ignore_case=false but -i appeared in args: %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "localhost") {
		t.Errorf("expected pattern 'localhost' as literal arg, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "/etc/hosts") {
		t.Errorf("expected path '/etc/hosts' as literal arg, got %v", capturedArgs)
	}
}

func TestFileManager_GrepFilesRecursiveIgnoreCase(t *testing.T) {
	fm := NewFileManager()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := fm.Execute(context.Background(), "grep_files", json.RawMessage(`{"pattern":"ERROR","path":"/var/log","recursive":true,"ignore_case":true}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !argvContains(capturedArgs, "-r") {
		t.Errorf("expected '-r' as literal arg, got %v", capturedArgs)
	}
	if !argvContains(capturedArgs, "-i") {
		t.Errorf("expected '-i' as literal arg, got %v", capturedArgs)
	}
}

func TestFileManager_GrepFilesPatternIsLiteralArg(t *testing.T) {
	// pattern 含 shell 元字符也不能被解释（grep 自己会解析正则）
	fm := NewFileManager()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := fm.Execute(context.Background(), "grep_files", json.RawMessage(`{"pattern":"foo; rm -rf /","path":"/tmp/file"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !argvContains(capturedArgs, "foo; rm -rf /") {
		t.Errorf("pattern should be one literal arg, got %v", capturedArgs)
	}
}

func TestFileManager_GrepFilesPathIsLiteralArg(t *testing.T) {
	// path 含 shell 元字符也不能被解释
	fm := NewFileManager()
	var capturedArgs []string
	fake := &fakeExec{argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		capturedArgs = args
		return []byte(""), nil
	}}
	_, err := fm.Execute(context.Background(), "grep_files", json.RawMessage(`{"pattern":"localhost","path":"/tmp/x; rm -rf /"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !argvContains(capturedArgs, "/tmp/x; rm -rf /") {
		t.Errorf("path should be one literal arg, got %v", capturedArgs)
	}
}

func TestFileManager_UnknownTool(t *testing.T) {
	fm := NewFileManager()
	_, err := fm.Execute(context.Background(), "delete_file", json.RawMessage(`{}`), &fakeExec{})
	if err == nil {
		t.Error("should return error for unknown tool")
	}
}
