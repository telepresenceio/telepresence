package agent

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/datawire/ambassador/pkg/dlog"
)

type Forwarder struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc

	listenAddr *net.TCPAddr
	targetAddr *net.TCPAddr
}

func NewForwarder(ctx context.Context, listen, target *net.TCPAddr) *Forwarder {
	ctx, cancel := context.WithCancel(ctx)
	ctx = dlog.WithField(ctx, "lis", listen.String())

	return &Forwarder{
		ctx:        ctx,
		cancel:     cancel,
		listenAddr: listen,
		targetAddr: target,
	}
}

func (f *Forwarder) Target() *net.TCPAddr {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.targetAddr
}

func (f *Forwarder) SetTarget(target *net.TCPAddr) {
	f.mu.Lock()
	defer f.mu.Unlock()

	dlog.Debugf(f.ctx, "Forward target changed from %s to %s", f.targetAddr, target)
	f.targetAddr = target
}

func (f *Forwarder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cancel()
	return nil
}

func (f *Forwarder) Start() error {
	f.mu.Lock()
	ctx := f.ctx
	listenAddr := f.listenAddr
	f.mu.Unlock()

	listener, err := net.ListenTCP("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer listener.Close()

	dlog.Debugf(ctx, "Forwarding from %s", listenAddr.String())
	defer dlog.Debugf(ctx, "Done forwarding from %s", listenAddr.String())

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		conn, err := listener.AcceptTCP()
		if err != nil {
			dlog.Infof(ctx, "Error on accept: %+v", err)
			continue
		}
		go f.ForwardConn(conn)
	}
}

func (f *Forwarder) ForwardConn(src *net.TCPConn) {
	f.mu.Lock()
	ctx := f.ctx
	targetAddr := f.targetAddr
	f.mu.Unlock()

	ctx = dlog.WithField(ctx, "src", src.RemoteAddr().String())
	ctx = dlog.WithField(ctx, "dst", targetAddr.String())

	dlog.Debugf(ctx, "Forwarding...")
	defer dlog.Debugf(ctx, "Done forwarding")

	defer src.Close()

	dst, err := net.DialTCP("tcp", nil, targetAddr)
	if err != nil {
		dlog.Infof(ctx, "Error on dial: %+v", err)
		return
	}
	defer dst.Close()

	done := make(chan struct{})

	go func() {
		if _, err := io.Copy(dst, src); err != nil {
			dlog.Debugf(ctx, "Error src->dst: %+v", err)
		}
		_ = dst.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(src, dst); err != nil {
			dlog.Debugf(ctx, "Error dst->src: %+v", err)
		}
		_ = src.CloseWrite()
		done <- struct{}{}
	}()

	// Wait for both sides to close the connection
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return
		case <-done:
		}
	}
}
