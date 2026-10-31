// Package errors is a minimal local stand-in for github.com/juju/errors.
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
func Wrap(err error, message ...interface{}) error {
	if len(message) == 0 {
		return err
	}
	return fmt.Errorf("%s: %w", fmt.Sprint(message...), err)
}
