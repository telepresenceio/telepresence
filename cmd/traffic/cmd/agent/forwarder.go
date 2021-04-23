package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/datawire/dlib/dlog"
)

type Forwarder struct {
	mu sync.Mutex

	lCtx       context.Context
	lCancel    context.CancelFunc
	listenAddr *net.TCPAddr

	tCtx       context.Context
	tCancel    context.CancelFunc
	targetHost string
	targetPort int32
}

func NewForwarder(listen *net.TCPAddr, targetHost string, targetPort int32) *Forwarder {
	return &Forwarder{
		listenAddr: listen,
		targetHost: targetHost,
		targetPort: targetPort,
	}
}

func (f *Forwarder) Serve(ctx context.Context) error {
	f.mu.Lock()

	// Set up listener lifetime (same as the overall forwarder lifetime)
	f.lCtx, f.lCancel = context.WithCancel(ctx)
	f.lCtx = dlog.WithField(f.lCtx, "lis", f.listenAddr.String())

	// Set up target lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)

	ctx = f.lCtx
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

func (f *Forwarder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lCancel()
	return nil
}

func (f *Forwarder) Target() (string, int32) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.targetHost, f.targetPort
}

func (f *Forwarder) SetTarget(targetHost string, targetPort int32) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if targetHost != f.targetHost || targetPort != f.targetPort {
		dlog.Debugf(f.lCtx, "Forward target changed from %s:%d to %s:%d", f.targetHost, f.targetPort, targetHost, targetPort)

		// Drop existing connections
		f.tCancel()

		// Set up new target and lifetime
		f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
		f.targetHost = targetHost
		f.targetPort = targetPort
	}
}

func (f *Forwarder) ForwardConn(clientConn *net.TCPConn) {
	f.mu.Lock()
	ctx := f.tCtx
	targetHost := f.targetHost
	targetPort := f.targetPort
	f.mu.Unlock()

	targetAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", targetHost, targetPort))
	if err != nil {
		dlog.Infof(ctx, "Error on resolve(%s:%d): %+v", targetHost, targetPort, err)
		return
	}

	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())
	ctx = dlog.WithField(ctx, "target", targetAddr.String())

	dlog.Debug(ctx, "Forwarding...")
	defer dlog.Debug(ctx, "Done forwarding")

	defer clientConn.Close()

	targetConn, err := net.DialTCP("tcp", nil, targetAddr)
	if err != nil {
		dlog.Infof(ctx, "Error on dial: %+v", err)
		return
	}
	defer targetConn.Close()

	done := make(chan struct{})

	go func() {
		if _, err := io.Copy(targetConn, clientConn); err != nil {
			dlog.Debugf(ctx, "Error clientConn->targetConn: %+v", err)
		}
		_ = targetConn.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(clientConn, targetConn); err != nil {
			dlog.Debugf(ctx, "Error targetConn->clientConn: %+v", err)
		}
		_ = clientConn.CloseWrite()
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
