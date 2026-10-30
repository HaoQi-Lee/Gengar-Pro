// Package executor 统一 gengar-pro 的命令执行接缝。插件层只依赖本接口；
// 目标是本地时绑定 Local，是远程主机时绑定 SSH（由 Manager 供给）。
package executor

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// Executor 是一次目标绑定的执行器：同一插件的同一工具调用，会对每台
// 目标主机各拿到一个 Executor 实例并执行一次。
type Executor interface {
	// RunShell 经目标机 shell 执行命令串（bash -c 语义）。
	RunShell(ctx context.Context, cmd string) ([]byte, error)
	// RunArgv 以 argv 语义执行，参数不经 shell 重新解释（注入安全）。
	// Local 是真正的 argv exec；SSH 实现把参数以 POSIX 单引号安全引用
	// 后交远端 shell 解析（假设远端是 POSIX 兼容 shell），语义等价——
	// 但参数中的 NUL 字节会被远端 shell 截断，不可携带。
	RunArgv(ctx context.Context, name string, args ...string) ([]byte, error)
	// WriteFile 写文件（0644）。父目录由调用方（FileManager）保证存在。
	WriteFile(ctx context.Context, path string, content []byte) error
}

// Local 在本机执行。timeout 在调用方 ctx 无 deadline 时兜底；零值表示
// 不设兜底超时（由调用方 ctx 全权决定）。
//
// 平台提示（迁移自旧 plugin helpers）：
//   - RunShell 依赖 bash 在 PATH 中（原生 Windows 环境会失败，
//     开发机需 Git Bash/WSL）；
//   - RunArgv 的 argv 形式在 Windows 上执行 .bat/.cmd 时仍经 cmd.exe
//     解析（Go 标准库行为，无法绕过）。
type Local struct {
	timeout time.Duration
}

var _ Executor = (*Local)(nil)

func NewLocal(timeout time.Duration) *Local { return &Local{timeout: timeout} }

func (l *Local) RunShell(ctx context.Context, cmd string) ([]byte, error) {
	ctx = l.withTimeout(ctx)
	execCmd := exec.CommandContext(ctx, "bash", "-c", cmd)
	return run(execCmd)
}

func (l *Local) RunArgv(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx = l.withTimeout(ctx)
	return run(exec.CommandContext(ctx, name, args...))
}

func (l *Local) WriteFile(_ context.Context, path string, content []byte) error {
	return os.WriteFile(path, content, 0644)
}

func (l *Local) withTimeout(ctx context.Context) context.Context {
	if l.timeout <= 0 {
		return ctx // 零值 = 不设兜底超时（由调用方 ctx 全权决定）
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx
	}
	c, cancel := context.WithTimeout(ctx, l.timeout)
	// 故意不提前 cancel：返回的 ctx 由调用链持有（与旧 plugin.helpers
	// 在函数内 defer cancel 不同），命令结束时 timer 仍存活至 deadline，
	// 泄漏窗口 = timeout，可接受。显式丢弃 cancel 以表明有意为之；
	// 单独语句的 `_ = cancel` 不触发 go vet 的 lostcancel 检查。
	_ = cancel
	return c
}

// run 执行命令并合并 stdout+stderr（与旧 plugin.helpers 输出语义一致，
// 保证插件返回内容与历史行为逐字节兼容）。
func run(execCmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr
	err := execCmd.Run()
	output := stdout.Bytes()
	if stderr.Len() > 0 {
		output = append(output, stderr.Bytes()...)
	}
	return output, err
}
