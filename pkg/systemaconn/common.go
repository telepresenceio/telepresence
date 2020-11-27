package systemaconn

import (
	"net"
)

// Conn is a net.Conn, plus a Wait() method.
type Conn interface {
	net.Conn
	Wait() error
}

type addr struct {
	net  string
	addr string
}

func (a addr) Network() string { return a.net }
func (a addr) String() string  { return a.addr }
