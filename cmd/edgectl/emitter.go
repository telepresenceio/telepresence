package main

import (
	"fmt"
	"io"
)

// Emitter provides a convenient way to send data to the client and
// handle errors in one spot.
type Emitter struct {
	w   io.Writer
	err error
}

// NewEmitter returns a new Emitter to write to conn
func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{w: w}
}

// Printf formats according to a format specifier and writes to the
// client. Errors are collected and returned by Err().
func (out *Emitter) Printf(format string, a ...interface{}) {
	if out.err == nil {
		_, out.err = fmt.Fprintf(out.w, format, a...)
	}
}

// Print formats using the default formats for its operands and writes
// to the client. Errors are collected and returned by Err().
func (out *Emitter) Print(a ...interface{}) {
	if out.err == nil {
		_, out.err = fmt.Fprint(out.w, a...)
	}
}

// Println formats using the default formats for its operands and
// writes to the client, ending output with a newline. Errors are
// collected and returned by Err().
func (out *Emitter) Println(a ...interface{}) {
	if out.err == nil {
		_, out.err = fmt.Fprintln(out.w, a...)
	}
}

// SendExit tells the client to exit with the specified return code.
// Errors are collected and returned by Err().
func (out *Emitter) SendExit(code int) {
	if out.err == nil {
		_, out.err = fmt.Fprintf(out.w, "%s%d", ExitPrefix, code)
	}
}

// Err returns the first non-EOF error that was encountered by the
// Emitter.
func (out *Emitter) Err() error {
	return out.err
}
