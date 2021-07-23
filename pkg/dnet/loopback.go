package dnet

import (
	"errors"
	"net"
	"sync"
	"syscall"
)

// NewLoopbackListener returns a net.Listener that rather than
// listening on a real network interface, waits for you to add new
// connections to it with the returned AddConn function.
func NewLoopbackListener() (net.Listener, func(net.Conn) error) {
	l := &loopbackListener{
		conns: make(chan net.Conn),
	}
	return l, l.AddConn
}

type loopbackListener struct {
	mu     sync.RWMutex
	closed bool
	conns  chan net.Conn
}

func (l *loopbackListener) AddConn(conn net.Conn) error {
	l.mu.RLock()
	if l.closed {
		l.mu.RUnlock()
		return syscall.ECONNREFUSED
	}
	l.mu.RUnlock()

	l.conns <- conn
	return nil
}

// Accept implements net.Listner
func (l *loopbackListener) Accept() (net.Conn, error) {
	conn, ok := <-l.conns
	if !ok {
		return nil, errors.New("listener closed")
	}
	return conn, nil
}

// Close implements net.Listner
func (l *loopbackListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		close(l.conns)
		l.closed = true
	}
	return nil
}

// Addr implements net.Listner
func (l *loopbackListener) Addr() net.Addr {
	return loopbackAddr{}
}

type loopbackAddr struct{}

func (loopbackAddr) Network() string { return "loopback" }
func (loopbackAddr) String() string  { return "loopback" }
