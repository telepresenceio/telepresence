package edgectl

import (
	"encoding/json"
	"fmt"
	"io"
)

// Emitter provides a convenient way to send data to the client and
// handle errors in one spot.
type Emitter struct {
	w    io.Writer
	err  error
	kv   bool
	enc  *json.Encoder
	keys map[string]struct{}
}

// exists is the value for tracking keys in the keys map-as-set
var exists = struct{}{}

// NewEmitter returns a new Emitter to write to conn
func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{w: w, keys: make(map[string]struct{})}
}

func (out *Emitter) SetKV() {
	out.kv = true
	out.enc = json.NewEncoder(out.w)
}

// Printf formats according to a format specifier and writes to the
// client. Errors are collected and returned by Err().
func (out *Emitter) Printf(format string, a ...interface{}) {
	if out.err == nil && !out.kv {
		_, out.err = fmt.Fprintf(out.w, format, a...)
	}
}

// Print formats using the default formats for its operands and writes
// to the client. Errors are collected and returned by Err().
func (out *Emitter) Print(a ...interface{}) {
	if out.err == nil && !out.kv {
		_, out.err = fmt.Fprint(out.w, a...)
	}
}

// Println formats using the default formats for its operands and
// writes to the client, ending output with a newline. Errors are
// collected and returned by Err().
func (out *Emitter) Println(a ...interface{}) {
	if out.err == nil && !out.kv {
		_, out.err = fmt.Fprintln(out.w, a...)
	}
}

// SendExit tells the client to exit with the specified return code.
// Errors are collected and returned by Err().
func (out *Emitter) SendExit(code int) {
	if out.err == nil {
		_, out.err = fmt.Fprintf(out.w, "%s%d\n", ExitPrefix, code)
	}
	if out.err == nil {
		out.err = fmt.Errorf("exit code %d sent", code)
	}
}

// Send a key/value pair to the client if the Emitter is in key/value mode.
// Errors are collected and returned by Err().
func (out *Emitter) Send(key string, value interface{}) {
	// Enforce not ever repeating a key in a connection
	if _, ok := out.keys[key]; ok {
		panic(key)
	}
	out.keys[key] = exists
	if out.err == nil && out.kv {
		out.err = out.enc.Encode(map[string]interface{}{key: value})
	}
}

// Err returns the first non-EOF error that was encountered by the
// Emitter.
func (out *Emitter) Err() error {
	return out.err
}
