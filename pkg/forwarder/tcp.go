package forwarder

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type tcp struct{ basic }

// NewTCP creates a new TCP forwarder that will forward connections from the given port to the given target.
func NewTCP(from uint16, tag tunnel.Tag, target netip.AddrPort) Forwarder {
	return &tcp{basic{
		tag:        tag,
		target:     target,
		listenPort: int32(from),
	}}
}

func (f *tcp) Forward(ctx context.Context, clientConn net.Conn) error {
	defer dlog.Debug(ctx, "Done forwarding")
	defer clientConn.Close()

	if f.target.Port() == 0 {
		dlog.Debug(ctx, "Forwarding to /dev/null")
		_, _ = io.Copy(io.Discard, clientConn)
		return nil
	}

	ctx = dlog.WithField(ctx, "target", f.target.String())

	dlog.Debug(ctx, "Forwarding...")
	targetConn, err := net.DialTCP("tcp", nil, net.TCPAddrFromAddrPort(f.target))
	if err != nil {
		return fmt.Errorf("error on dial: %w", err)
	}
	defer targetConn.Close()

	done := make(chan struct{})

	go func() {
		if _, err := io.Copy(targetConn, clientConn); err != nil && ctx.Err() == nil {
			dlog.Debugf(ctx, "Error clientConn->targetConn: %+v", err)
		}
		_ = targetConn.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(clientConn, targetConn); err != nil && ctx.Err() == nil {
			dlog.Debugf(ctx, "Error targetConn->clientConn: %+v", err)
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
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", atomic.LoadInt32(&f.listenPort)))
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

// AcceptLoop accepts connections on the listener and calls f in a separate go routine for each connection.
func AcceptLoop(ctx context.Context, listener net.Listener, fw func(context.Context, net.Conn) error) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				dlog.Infof(ctx, "listener.Accept ended with error: %v", err)
			}
		}
		go func() {
			if err := fw(ctx, conn); err != nil {
				dlog.Error(ctx, err)
			}
		}()
	}
}
