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
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
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
	targetPort uint16

	manager     manager.ManagerClient
	sessionInfo *manager.SessionInfo

	intercept  *manager.InterceptInfo
	mgrVersion semver.Version
}

func NewForwarder(listen *net.TCPAddr, targetHost string, targetPort uint16) *Forwarder {
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
	f.lCancel()
	return nil
}

func (f *Forwarder) Target() (string, uint16) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.targetHost, f.targetPort
}

func (f *Forwarder) InterceptInfo() *restapi.InterceptInfo {
	ii := &restapi.InterceptInfo{}
	f.mu.Lock()
	if f.intercept != nil {
		ii.Intercepted = true
		ii.Metadata = f.intercept.Metadata
	}
	f.mu.Unlock()
	return ii
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
	f.tCancel()

	// Set up new target and lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	f.intercept = intercept
}

func (f *Forwarder) forwardConn(clientConn *net.TCPConn) error {
	f.mu.Lock()
	ctx := f.tCtx
	targetHost := f.targetHost
	targetPort := f.targetPort
	intercept := f.intercept
	f.mu.Unlock()
	if intercept != nil {
		return f.interceptConn(ctx, clientConn, intercept)
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

func (f *Forwarder) interceptConn(ctx context.Context, conn net.Conn, iCept *manager.InterceptInfo) error {
	dlog.Infof(ctx, "Accept got connection from %s", conn.RemoteAddr())

	srcIp, srcPort, err := iputil.SplitToIPPort(conn.RemoteAddr())
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s", conn.RemoteAddr())
	}

	spec := iCept.Spec
	destIp := iputil.Parse(spec.TargetHost)
	id := tunnel.NewConnID(tunnel.IPProto(conn.RemoteAddr().Network()), srcIp, destIp, srcPort, uint16(spec.TargetPort))

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
