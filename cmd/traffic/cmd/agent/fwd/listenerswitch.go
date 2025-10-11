package fwd

import (
	"net"
	"sync/atomic"
)

type ListenerSwitch interface {
	// Serve starts the accept-loop that forwards connections to the on or off listener.
	Serve() error

	// Primary returns the listener that accepts connections after Switch(true) has been called.
	Primary() net.Listener

	// Secondary returns the listener that accepts connections after Switch(false) has been called.
	Secondary() net.Listener

	Switch(primary bool)
}

type connOrErr struct {
	conn net.Conn
	err  error
}

type chanListener struct {
	addr net.Addr
	ch   chan connOrErr
}

type listenerSwitch struct {
	listener    net.Listener
	offListener chanListener
	onListener  chanListener
	on          atomic.Bool
}

func (c chanListener) Accept() (net.Conn, error) {
	ce := <-c.ch
	return ce.conn, ce.err
}

// Close is a no-op. The real close must be made on the listener passed to NewListenerSwitch.
func (c chanListener) Close() error {
	return nil
}

func (c chanListener) Addr() net.Addr {
	return c.addr
}

func (l *listenerSwitch) Primary() net.Listener {
	return l.offListener
}

func (l *listenerSwitch) Secondary() net.Listener {
	return l.onListener
}

func (l *listenerSwitch) Switch(onOrOff bool) {
	l.on.Store(onOrOff)
}

func NewListenerSwitch(listener net.Listener) ListenerSwitch {
	addr := listener.Addr()
	return &listenerSwitch{
		listener:    listener,
		offListener: chanListener{ch: make(chan connOrErr), addr: addr},
		onListener:  chanListener{ch: make(chan connOrErr), addr: addr},
	}
}

func (l *listenerSwitch) Serve() error {
	offCh := l.offListener.ch
	onCh := l.onListener.ch
	for {
		conn, err := l.listener.Accept()
		ce := connOrErr{conn: conn, err: err}
		if err != nil {
			// Both the on and off listeners will get the same error.
			onCh <- ce
			offCh <- ce
			return err
		}
		if l.on.Load() {
			onCh <- ce
		} else {
			offCh <- ce
		}
	}
}
