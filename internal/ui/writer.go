package ui

import (
	"bytes"
	"io"
)

// NewLineWriter returns an io.Writer that buffers bytes and calls emit once
// per complete line (the text up to and excluding '\n'). It lets line-oriented
// writers — slash commands printing via fmt.Fprintln/Fprintf — flow through a
// ui.Sink as one message line each, instead of dumping to raw stdout where the
// bytes collide with the TUI's alt-screen rendering and land in the wrong place.
//
// Bytes after the final newline stay buffered until more input arrives; every
// current caller terminates each line with '\n', so nothing is lost.
func NewLineWriter(emit func(line string)) io.Writer {
	return &lineWriter{emit: emit}
}

type lineWriter struct {
	emit func(line string)
	buf  bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		data := w.buf.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		w.emit(string(data[:i]))
		w.buf.Next(i + 1) // drop the emitted line and its newline
	}
	return len(p), nil
}
