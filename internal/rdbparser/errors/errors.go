// Package errors is a minimal local stand-in for github.com/juju/errors.
//
// 目的：让 vendored 的 rdb 解码器（internal/rdbparser/rdb）零改动地编译，
// 同时保持 go.mod 零外部依赖。
//
// The vendored rdb decoder (internal/rdbparser/rdb) calls juju/errors'
// New, Errorf, Trace and Wrap at ~110 call sites. To honor this repo's decision
// not to add module dependencies for the RDB parser, the import path was
// rewritten to this package, which implements just that API subset on top
// of the standard library. Trace returns its argument unchanged (juju
// attaches a stack trace; we don't need it).
package errors

import (
	stderrors "errors"
	"fmt"
)

// New returns an error that formats as the given text.
func New(message string) error {
	return stderrors.New(message)
}

// Errorf formats according to a format specifier and returns the string
// as an error value.
func Errorf(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}

// Trace returns err unchanged.
func Trace(err error) error {
	return err
}

// Wrap annotates err with the given message, preserving err as the cause.
//
// 与 juju/errors 的分歧：juju 的 Wrap(err, wrapper error) 只显示 wrapper 的
// 消息（原始错误被吞掉）；本 shim 渲染为 "message: cause"，并用 %w 保留
// cause，errors.Is/As 可正常穿透。签名相应收窄为 message ...interface{}。
func Wrap(err error, message ...interface{}) error {
	// err 为 nil 时原样返回 nil，避免把 nil 包成非 nil error；
	// 当前调用点均传非 nil，此处防未来 vendored 变更踩坑。
	if err == nil {
		return err
	}
	if len(message) == 0 {
		return err
	}
	return fmt.Errorf("%s: %w", fmt.Sprint(message...), err)
}
