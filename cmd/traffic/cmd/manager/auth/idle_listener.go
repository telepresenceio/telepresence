package auth

import (
	"net"
	"sync"
	"time"
)

// externalUnauthenticatedIdleTimeout bounds how long an accepted external-listener
// connection may sit without a successfully authenticated RPC before it is closed.
const externalUnauthenticatedIdleTimeout = 30 * time.Second

// idleTimeoutListener wraps a net.Listener so that every accepted connection is closed
// unless MarkAuthenticated is called on it within timeout of being accepted.
type idleTimeoutListener struct {
	net.Listener
	timeout time.Duration
}

// NewIdleTimeoutListener wraps ln with the unauthenticated-idle deadline.
func NewIdleTimeoutListener(ln net.Listener) net.Listener {
	return &idleTimeoutListener{Listener: ln, timeout: externalUnauthenticatedIdleTimeout}
}

func (l *idleTimeoutListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	ic := &idleConn{Conn: conn}
	ic.timer = time.AfterFunc(l.timeout, func() {
		_ = conn.Close()
	})
	return ic, nil
}

// idleConn is a net.Conn that closes itself unless MarkAuthenticated is called before
// its idle timer fires.
type idleConn struct {
	net.Conn

	mu        sync.Mutex
	timer     *time.Timer
	cancelled bool
}

// MarkAuthenticated cancels the idle timer. Safe to call more than once, and safe to
// call after the connection has already been closed.
func (c *idleConn) MarkAuthenticated() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cancelled {
		c.cancelled = true
		c.timer.Stop()
	}
}

// Close cancels the idle timer -- a closed connection must not be closed again by a
// later timer fire -- and closes the underlying connection.
func (c *idleConn) Close() error {
	c.MarkAuthenticated()
	return c.Conn.Close()
}
