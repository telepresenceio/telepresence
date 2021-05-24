package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
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

	manager     manager.ManagerClient
	sessionInfo *manager.SessionInfo

	intercept *manager.InterceptInfo
	tunnel    manager.Manager_AgentTunnelClient
}

func NewForwarder(listen *net.TCPAddr, targetHost string, targetPort int32) *Forwarder {
	return &Forwarder{
		listenAddr: listen,
		targetHost: targetHost,
		targetPort: targetPort,
	}
}

func (f *Forwarder) SetManager(sessionInfo *manager.SessionInfo, manager manager.ManagerClient) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionInfo = sessionInfo
	f.manager = manager
	f.tunnel = nil // any existing tunnel is lost when a reconnect happens
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
		go func() {
			if err := f.forwardConn(conn); err != nil {
				dlog.Error(ctx, err)
			}
		}()
	}
}

func (f *Forwarder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.tunnel != nil {
		_ = f.tunnel.CloseSend()
	}
	f.lCancel()
	return nil
}

func (f *Forwarder) Target() (string, int32) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.targetHost, f.targetPort
}

func (f *Forwarder) Intercepting() bool {
	f.mu.Lock()
	intercepting := f.intercept != nil
	f.mu.Unlock()
	return intercepting
}

func (f *Forwarder) SetIntercepting(intercept *manager.InterceptInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()

	iceptInfo := func(ii *manager.InterceptInfo) string {
		is := ii.Spec
		return fmt.Sprintf("'%s' (%s:%d)", is.Name, is.Client, is.TargetPort)
	}
	if intercept == nil {
		if f.intercept == nil {
			return
		}
		dlog.Debugf(f.lCtx, "Forward target changed from intercept %s to %s:%d", iceptInfo(f.intercept), f.targetHost, f.targetPort)
	} else {
		if f.intercept == nil {
			dlog.Debugf(f.lCtx, "Forward target changed from %s:%d to intercept %s", f.targetHost, f.targetPort, iceptInfo(intercept))
		} else {
			if f.intercept.Id == intercept.Id {
				return
			}
			dlog.Debugf(f.lCtx, "Forward target changed from intercept %s to intercept %q", iceptInfo(f.intercept), iceptInfo(intercept))
		}
	}

	// Drop existing connections
	if f.tunnel != nil {
		_ = f.tunnel.CloseSend()
		f.tunnel = nil
	}
	f.tCancel()

	// Set up new target and lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	if intercept != nil {
		if f.manager != nil {
			tunnel, err := f.startManagerTunnel(f.tCtx, intercept.ClientSession)
			if err != nil {
				dlog.Error(f.tCtx, err)
				return
			}
			f.tunnel = tunnel
		}
	}
	f.intercept = intercept
}

func (f *Forwarder) forwardConn(clientConn *net.TCPConn) error {
	f.mu.Lock()
	ctx := f.tCtx
	targetHost := f.targetHost
	targetPort := f.targetPort
	intercept := f.intercept
	tunnel := f.tunnel
	f.mu.Unlock()
	if tunnel != nil {
		return f.interceptConn(ctx, clientConn, intercept, tunnel)
	}

	targetAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", targetHost, targetPort))
	if err != nil {
		return fmt.Errorf("error on resolve(%s:%d): %+v", targetHost, targetPort, err)
	}

	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())
	ctx = dlog.WithField(ctx, "target", targetAddr.String())

	dlog.Debug(ctx, "Forwarding...")
	defer dlog.Debug(ctx, "Done forwarding")

	defer clientConn.Close()

	targetConn, err := net.DialTCP("tcp", nil, targetAddr)
	if err != nil {
		return fmt.Errorf("error on dial: %+v", err)
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
		case <-done:
		}
	}
	return nil
}

func (f *Forwarder) startManagerTunnel(ctx context.Context, clientSession *manager.SessionInfo) (manager.Manager_AgentTunnelClient, error) {
	tunnel, err := f.manager.AgentTunnel(ctx)
	if err != nil {
		err = fmt.Errorf("call to AgentTunnel() failed: %v", err)
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tunnel.CloseSend()
		}
	}()

	if err = tunnel.Send(connpool.SessionInfoControl(f.sessionInfo).TunnelMessage()); err != nil {
		err = fmt.Errorf("failed to send agent sessionID: %s", err)
		return nil, err
	}
	if err = tunnel.Send(connpool.SessionInfoControl(clientSession).TunnelMessage()); err != nil {
		err = fmt.Errorf("failed to send client sessionID: %s", err)
		return nil, err
	}

	go func() {
		pool := connpool.GetPool(ctx)
		closing := int32(0)
		msgCh, errCh := connpool.NewStream(tunnel).ReadLoop(ctx, &closing)
		for {
			select {
			case <-ctx.Done():
				atomic.StoreInt32(&closing, 2)
				return
			case err := <-errCh:
				dlog.Error(ctx, err)
				return
			case msg := <-msgCh:
				if msg == nil {
					return
				}
				id := msg.ID()
				handler, _, err := pool.Get(ctx, id, func(ctx context.Context, release func()) (connpool.Handler, error) {
					return connpool.NewDialer(id, tunnel, release), nil
				})
				if err != nil {
					dlog.Error(ctx, err)
					return
				}
				handler.HandleMessage(ctx, msg)
			}
		}
	}()
	return tunnel, nil
}

func (f *Forwarder) interceptConn(ctx context.Context, conn net.Conn, iCept *manager.InterceptInfo, tunnel connpool.TunnelStream) error {
	dlog.Infof(ctx, "Accept got connection from %s", conn.RemoteAddr())

	srcIp, srcPort, err := iputil.SplitToIPPort(conn.RemoteAddr())
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s", conn.RemoteAddr())
	}

	destIp := iputil.Parse(iCept.Spec.TargetHost)
	id := connpool.NewConnID(connpool.IPProto(conn.RemoteAddr().Network()), srcIp, destIp, srcPort, uint16(iCept.Spec.TargetPort))
	_, found, err := connpool.GetPool(ctx).Get(ctx, id, func(ctx context.Context, release func()) (connpool.Handler, error) {
		return connpool.HandlerFromConn(id, tunnel, release, conn), nil
	})
	if err != nil {
		return fmt.Errorf("failed to create intercept tunnel connection for %s: %v", id, err)
	}
	if found {
		// This should really never happen. It indicates that there are two connections originating from the same port.
		return fmt.Errorf("multiple connections for %s", id)
	}
	return nil
}
