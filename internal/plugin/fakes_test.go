package plugin

import (
	"context"
	"fmt"
)

// fakeExec 是注入插件 Execute 的假执行器。三个函数字段按需替换，
// 未设置的行为返回错误，保证测试意图显式。
type fakeExec struct {
	shellFn func(ctx context.Context, cmd string) ([]byte, error)
	argvFn  func(ctx context.Context, name string, args ...string) ([]byte, error)
	writeFn func(ctx context.Context, path string, content []byte) error
}

func (f *fakeExec) RunShell(ctx context.Context, cmd string) ([]byte, error) {
	if f.shellFn == nil {
		return nil, fmt.Errorf("fakeExec: unexpected RunShell %q", cmd)
	}
	return f.shellFn(ctx, cmd)
}

func (f *fakeExec) RunArgv(ctx context.Context, name string, args ...string) ([]byte, error) {
	if f.argvFn == nil {
		return nil, fmt.Errorf("fakeExec: unexpected RunArgv %s %v", name, args)
	}
	return f.argvFn(ctx, name, args...)
}

func (f *fakeExec) WriteFile(ctx context.Context, path string, content []byte) error {
	if f.writeFn == nil {
		return fmt.Errorf("fakeExec: unexpected WriteFile %s", path)
	}
	return f.writeFn(ctx, path, content)
}
