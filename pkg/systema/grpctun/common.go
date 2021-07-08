package grpctun

import (
	"net"
)

// Conn is a net.Conn, plus a Wait() method.
type Conn interface {
	net.Conn

	// Wait until either the connection is Close()d, or one of Read() or Write() encounters an
	// error (*not* counting errors caused by deadlines).  If this returns because Close() was
	// called, nil is returned; otherwise the triggering error is returned.
	//
	// Essentially: Wait until the connection is finished.
	Wait() error
}

type addr struct {
	net  string
	addr string
}

func (a addr) Network() string { return a.net }
func (a addr) String() string  { return a.addr }
