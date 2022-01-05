package forwarder

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/blang/semver"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
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

	intercept  *manager.InterceptInfo
	muxTunnel  connpool.MuxTunnel
	mgrVersion semver.Version
}

func NewForwarder(listen *net.TCPAddr, targetHost string, targetPort int32) *Forwarder {
	return &Forwarder{
		listenAddr: listen,
		targetHost: targetHost,
		targetPort: targetPort,
	}
}

func (f *Forwarder) SetManager(sessionInfo *manager.SessionInfo, manager manager.ManagerClient, version semver.Version) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionInfo = sessionInfo
	f.manager = manager
	f.muxTunnel = nil // any existing tunnel is lost when a reconnect happens
	f.mgrVersion = version
}

func (f *Forwarder) Serve(ctx context.Context) error {
	listener, err := f.Listen(ctx)
	if err != nil {
		return err
	}
	return f.ServeListener(ctx, listener)
}

func (f *Forwarder) ServeListener(ctx context.Context, listener *net.TCPListener) error {
	defer listener.Close()

	dlog.Debugf(ctx, "Forwarding from %s", f.listenAddr.String())
	defer dlog.Debugf(ctx, "Done forwarding from %s", f.listenAddr.String())

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
			if ctx.Err() != nil {
				return nil
			}
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

func (f *Forwarder) Listen(ctx context.Context) (*net.TCPListener, error) {
	f.mu.Lock()

	// Set up listener lifetime (same as the overall forwarder lifetime)
	f.lCtx, f.lCancel = context.WithCancel(ctx)
	f.lCtx = dlog.WithField(f.lCtx, "lis", f.listenAddr.String())

	// Set up target lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	listenAddr := f.listenAddr

	f.mu.Unlock()
	return net.ListenTCP("tcp", listenAddr)
}

func (f *Forwarder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.muxTunnel != nil {
		_ = f.muxTunnel.CloseSend()
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
	if f.muxTunnel != nil {
		_ = f.muxTunnel.CloseSend()
		f.muxTunnel = nil
	}
	f.tCancel()

	// Set up new target and lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	if intercept != nil {
		if f.manager != nil {
			muxTunnel, err := f.startManagerTunnel(f.tCtx, intercept.ClientSession)
			if err != nil {
				dlog.Error(f.tCtx, err)
				return
			}
			f.muxTunnel = muxTunnel
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
	muxTunnel := f.muxTunnel
	f.mu.Unlock()
	if intercept != nil {
		return f.interceptConn(ctx, clientConn, intercept, muxTunnel)
	}

	targetAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", targetHost, targetPort))
	if err != nil {
		return fmt.Errorf("error on resolve(%s:%d): %w", targetHost, targetPort, err)
	}

	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())
	ctx = dlog.WithField(ctx, "target", targetAddr.String())

	dlog.Debug(ctx, "Forwarding...")
	defer dlog.Debug(ctx, "Done forwarding")

	defer clientConn.Close()

	targetConn, err := net.DialTCP("tcp", nil, targetAddr)
	if err != nil {
		return fmt.Errorf("error on dial: %w", err)
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

func (f *Forwarder) startManagerTunnel(ctx context.Context, clientSession *manager.SessionInfo) (connpool.MuxTunnel, error) {
	agentTunnel, err := f.manager.AgentTunnel(ctx)
	if err != nil {
		err = fmt.Errorf("call to AgentTunnel() failed: %v", err)
		return nil, err
	}
	muxTunnel := connpool.NewMuxTunnel(agentTunnel)
	defer func() {
		if err != nil {
			_ = muxTunnel.CloseSend()
		}
	}()

	if err = muxTunnel.Send(ctx, connpool.SessionInfoControl(f.sessionInfo)); err != nil {
		err = fmt.Errorf("failed to send agent sessionID: %s", err)
		return nil, err
	}
	if err = muxTunnel.Send(ctx, connpool.SessionInfoControl(clientSession)); err != nil {
		err = fmt.Errorf("failed to send client sessionID: %s", err)
		return nil, err
	}
	var peerVersion uint16
	if f.mgrVersion.LE(semver.MustParse("2.4.2")) {
		peerVersion = 0
	} else {
		if err = muxTunnel.Send(ctx, connpool.VersionControl()); err != nil {
			err = fmt.Errorf("failed to send agent tunnel version: %s", err)
			return nil, err
		}
		peerVersion, err = muxTunnel.ReadPeerVersion(ctx)
		if err != nil {
			return nil, err
		}
	}
	if peerVersion >= 2 {
		// Versions >= 2 no longer use the multiplexing tunnel. Instead, each connection gets its own tunnel.Stream
		_ = muxTunnel.CloseSend()
		return nil, nil
	}

	go func() {
		pool := tunnel.GetPool(ctx)
		msgCh, errCh := muxTunnel.ReadLoop(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errCh:
				dlog.Error(ctx, err)
				return
			case msg := <-msgCh:
				if msg == nil {
					return
				}
				id := msg.ID()
				var handler tunnel.Handler
				if ctrl, ok := msg.(connpool.Control); ok {
					switch ctrl.Code() {
					// Don't establish a new Dialer just to say goodbye
					case connpool.ReadClosed, connpool.WriteClosed, connpool.Disconnect, connpool.DisconnectOK:
						if handler = pool.Get(id); handler == nil {
							continue
						}
					}
				}
				if handler == nil {
					handler, _, err = pool.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (tunnel.Handler, error) {
						return connpool.NewDialer(id, muxTunnel, release), nil
					})
					if err != nil {
						dlog.Error(ctx, err)
						return
					}
				}
				handler.(connpool.Handler).HandleMessage(ctx, msg)
			}
		}
	}()
	return muxTunnel, nil
}

func (f *Forwarder) interceptConn(ctx context.Context, conn net.Conn, iCept *manager.InterceptInfo, muxTunnel connpool.MuxTunnel) error {
	dlog.Infof(ctx, "Accept got connection from %s", conn.RemoteAddr())

	srcIp, srcPort, err := iputil.SplitToIPPort(conn.RemoteAddr())
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s", conn.RemoteAddr())
	}

	spec := iCept.Spec
	destIp := iputil.Parse(spec.TargetHost)
	id := tunnel.NewConnID(tunnel.IPProto(conn.RemoteAddr().Network()), srcIp, destIp, srcPort, uint16(spec.TargetPort))

	if muxTunnel != nil {
		_, found, err := tunnel.GetPool(ctx).GetOrCreate(ctx, id, func(ctx context.Context, release func()) (tunnel.Handler, error) {
			return connpool.HandlerFromConn(id, muxTunnel, release, conn), nil
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

	ms, err := f.manager.Tunnel(ctx)
	if err != nil {
		return fmt.Errorf("call to manager.Tunnel() failed. Id %s: %v", id, err)
	}

	s, err := tunnel.NewClientStream(ctx, ms, id, f.sessionInfo.SessionId, time.Duration(spec.RoundtripLatency), time.Duration(spec.DialTimeout))
	if err != nil {
		return err
	}
	if err = s.Send(ctx, tunnel.SessionMessage(iCept.ClientSession.SessionId)); err != nil {
		return fmt.Errorf("unable to send client session id. Id %s: %v", id, err)
	}
	d := tunnel.NewConnEndpoint(s, conn)
	d.Start(ctx)
	<-d.Done()
	return nil
}
