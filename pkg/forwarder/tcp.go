package forwarder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type tcp struct{ basic }

// NewTCP creates a new TCP forwarder that will forward connections from the given port to the given target.
func NewTCP(from uint16, tag tunnel.Tag, target netip.AddrPort, opts ...Option) Forwarder {
	f := &tcp{basic{
		tag:        tag,
		target:     target,
		listenPort: int32(from),
		listener:   defaultListenerFactory{},
	}}
	for _, opt := range opts {
		opt(&f.basic)
	}
	return f
}

func (f *tcp) Forward(ctx context.Context, clientConn net.Conn) error {
	defer clog.Debug(ctx, "Done forwarding")
	defer clientConn.Close()

	if f.target.Port() == 0 {
		clog.Debug(ctx, "Forwarding to /dev/null")
		_, _ = io.Copy(io.Discard, clientConn)
		return nil
	}

	ctx = clog.With(ctx, "target", f.target)

	clog.Debug(ctx, "Forwarding...")
	var (
		targetConn net.Conn
		err        error
	)
	if f.dialer != nil {
		targetConn, err = f.dialer.DialContext(ctx, "tcp", f.target.String())
	} else {
		targetConn, err = net.DialTCP("tcp", nil, net.TCPAddrFromAddrPort(f.target))
	}
	if err != nil {
		return fmt.Errorf("error on dial: %w", err)
	}
	defer targetConn.Close()

	done := make(chan struct{}, 2)

	go func() {
		if _, err := io.Copy(targetConn, clientConn); err != nil && ctx.Err() == nil {
			clog.Debugf(ctx, "Error clientConn->targetConn: %+v", err)
		}
		if cw, ok := targetConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(clientConn, targetConn); err != nil && ctx.Err() == nil {
			clog.Debugf(ctx, "Error targetConn->clientConn: %+v", err)
		}
		if hwCloser, ok := clientConn.(interface{ CloseWrite() error }); ok {
			_ = hwCloser.CloseWrite()
		}
		done <- struct{}{}
	}()

	// Wait for both sides to close the connection
	for numClosed := 0; numClosed < 2; {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			numClosed++
		}
	}
	return nil
}

func (f *tcp) Listen(ctx context.Context) (net.Listener, error) {
	listener, err := f.listener.Listen(ctx, "tcp", f.bindAddr(atomic.LoadInt32(&f.listenPort)))
	if err != nil {
		return nil, err
	}
	atomic.StoreInt32(&f.listenPort, int32(listener.Addr().(*net.TCPAddr).AddrPort().Port()))
	return listener, nil
}

// ListenPort returns the port that this forwarder will listen to. This port will be updated
// by a call to Serve if the port was initially zero.
func (f *basic) ListenPort() types.PortAndProto {
	return types.PortAndProto{Proto: types.ProtoTCP, Port: uint16(atomic.LoadInt32(&f.listenPort))}
}

func (f *tcp) Serve(ctx context.Context, port chan<- netip.AddrPort) error {
	return f.ServeTo(ctx, port, f.Forward)
}

func (f *tcp) ServeTo(ctx context.Context, initCh chan<- netip.AddrPort, fw func(context.Context, net.Conn) error) error {
	listener, err := f.Listen(ctx)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	if initCh != nil {
		initCh <- listener.Addr().(*net.TCPAddr).AddrPort()
		close(initCh)
	}
	AcceptLoop(ctx, listener, fw)
	return nil
}

const (
	minAcceptDelay = 5 * time.Millisecond
	maxAcceptDelay = time.Second
)

// NextAcceptDelay returns the pause to use after a failed Accept, given the previous
// pause (zero after a successful accept). The pause doubles up to one second.
func NextAcceptDelay(prev time.Duration) time.Duration {
	if prev < minAcceptDelay {
		return minAcceptDelay
	}
	return min(prev*2, maxAcceptDelay)
}

// IsTemporaryAcceptError reports whether err from a listener's Accept is expected to clear
// up on its own, such as running out of file descriptors.
func IsTemporaryAcceptError(err error) bool {
	var te interface{ Temporary() bool }
	return errors.As(err, &te) && te.Temporary()
}

// AcceptLoop accepts connections on the listener and calls f in a separate go routine for each connection.
// It returns when the listener is closed or the context is done.
func AcceptLoop(ctx context.Context, listener net.Listener, fw func(context.Context, net.Conn) error) {
	var delay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			if delay == 0 {
				clog.Warnf(ctx, "listener.Accept failed, retrying until it succeeds: %v", err)
			}
			delay = NextAcceptDelay(delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		go func() {
			if err := fw(ctx, conn); err != nil {
				clog.Error(ctx, err)
			}
		}()
	}
}
