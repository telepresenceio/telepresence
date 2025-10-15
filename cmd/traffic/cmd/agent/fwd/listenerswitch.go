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
	listener            net.Listener
	primaryListener     chanListener
	secondaryListener   chanListener
	secondaryAcceptLoop func(listener net.Listener)
	secondary           atomic.Bool
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
	return l.primaryListener
}

func (l *listenerSwitch) Switch(secondary bool) {
	l.secondary.Store(secondary)
}

func NewListenerSwitch(listener net.Listener, secondaryAcceptLoop func(net.Listener)) ListenerSwitch {
	addr := listener.Addr()
	return &listenerSwitch{
		listener:            listener,
		secondaryAcceptLoop: secondaryAcceptLoop,
		primaryListener:     chanListener{ch: make(chan connOrErr), addr: addr},
		secondaryListener:   chanListener{ch: make(chan connOrErr), addr: addr},
	}
}

func (l *listenerSwitch) Serve() error {
	primaryCh := l.primaryListener.ch
	secondaryCh := l.secondaryListener.ch
	for {
		conn, err := l.listener.Accept()
		ce := connOrErr{conn: conn, err: err}
		if err != nil {
			// Both the on and off listeners will get the same error.
			primaryCh <- ce
			secondaryCh <- ce
			return err
		}
		if l.secondary.Load() {
			// Start the secondary accept-loop if it wasn't already running.
			if sal := l.secondaryAcceptLoop; sal != nil {
				l.secondaryAcceptLoop = nil
				go sal(l.secondaryListener)
			}
			secondaryCh <- ce
		} else {
			primaryCh <- ce
		}
	}
}
