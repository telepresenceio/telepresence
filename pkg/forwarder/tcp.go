package forwarder

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type tcp struct {
	interceptor
	httpIntercepts []*manager.InterceptInfo // Store multiple HTTP intercepts with filters
}

func newTCP(listenPort uint16, tag tunnel.Tag, target netip.AddrPort) Interceptor {
	return &tcp{
		interceptor: interceptor{
			tag:        tag,
			listenPort: listenPort,
			target:     target,
			lCancel:    func() {},
		},
	}
}

func (f *tcp) InterceptInfos() (infos []*manager.InterceptInfo) {
	f.mu.Lock()
	if len(f.httpIntercepts) > 0 {
		infos = slices.Clone(f.httpIntercepts)
	} else if f.intercept != nil {
		infos = []*manager.InterceptInfo{f.intercept}
	}
	f.mu.Unlock()
	return infos
}

// SetInterceptingMultiple overrides the base implementation to handle HTTP intercepts.
func (f *tcp) SetInterceptingMultiple(ctx context.Context, intercepts []*manager.InterceptInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Separate HTTP intercepts (with filters) from global/TCP intercepts
	var httpIntercepts []*manager.InterceptInfo
	var globalIntercept *manager.InterceptInfo

	for _, intercept := range intercepts {
		if intercept.Spec.Wiretap {
			continue
		}
		if len(intercept.Spec.HeaderFilters) > 0 || len(intercept.Spec.PathFilters) > 0 {
			httpIntercepts = append(httpIntercepts, intercept)
		} else if globalIntercept == nil {
			globalIntercept = intercept
		}
	}

	// Store HTTP intercepts for DispatchByMechanism
	f.httpIntercepts = httpIntercepts

	// Set global/TCP intercept using base implementation
	f.intercept = globalIntercept
	if f.lCtx != nil {
		f.tCancel()
		f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	}
}

// SetIntercepting overrides the base implementation to clear HTTP intercepts.
func (f *tcp) SetIntercepting(ctx context.Context, intercept *manager.InterceptInfo) {
	f.mu.Lock()
	f.httpIntercepts = nil // Clear HTTP intercepts when setting single intercept
	f.mu.Unlock()
	f.interceptor.SetIntercepting(ctx, intercept)
}

func (f *tcp) Serve(ctx context.Context, initCh chan<- netip.AddrPort) error {
	listener, err := f.listen(ctx)
	if err != nil {
		return err
	}
	defer listener.Close()

	la := listener.Addr().(*net.TCPAddr)
	if initCh != nil {
		initCh <- la.AddrPort()
		close(initCh)
	}

	dlog.Debugf(ctx, "Forwarding from %s", la)
	defer dlog.Debugf(ctx, "Done forwarding from %s", la)

	go f.acceptLoop(listener)
	<-ctx.Done()
	return nil
}

func (f *tcp) listen(ctx context.Context) (*net.TCPListener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Set up listener lifetime (same as the overall forwarder lifetime)
	f.lCtx, f.lCancel = context.WithCancel(ctx)

	// Set up a target lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	listenPort := f.listenPort

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: int(listenPort)})
	if err != nil {
		return nil, err
	}
	addr := listener.Addr().(*net.TCPAddr).AddrPort()
	f.lCtx = dlog.WithField(f.lCtx, "listen", addr.String())
	f.listenPort = addr.Port()
	return listener, nil
}

func (f *tcp) acceptLoop(listener *net.TCPListener) {
	for {
		select {
		case <-f.lCtx.Done():
			return
		default:
		}

		conn, err := listener.AcceptTCP()
		if err != nil {
			if f.lCtx.Err() != nil {
				return
			}
			dlog.Infof(f.lCtx, "Error on accept: %+v", err)
			continue
		}
		go func() {
			if err := f.forwardConn(conn); err != nil {
				dlog.Error(f.lCtx, err)
			}
		}()
	}
}

// Number of []byte chunks that can be cached by a wiretap connection before it discards data.
const wiretapCacheSize = 0x100

func (f *tcp) forwardConn(clientConn net.Conn) error {
	var wtIntercepts []*manager.InterceptInfo
	f.mu.Lock()
	ctx := f.tCtx
	targetAddr := f.target
	intercept := f.intercept
	tapCount := len(f.wiretaps)
	if tapCount > 0 {
		wtIntercepts = make([]*manager.InterceptInfo, tapCount)
		i := 0
		for _, wt := range f.wiretaps {
			wtIntercepts[i] = wt
			i++
		}
	}
	f.mu.Unlock()

	// Give mechanism-specific handling a chance first (e.g., HTTP-aware routing)
	if handled, err := f.DispatchByMechanism(ctx, clientConn, intercept); handled || err != nil {
		return err
	}

	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())

	if targetAddr.Port() > 0 {
		if len(wtIntercepts) > 0 {
			var taps []net.Conn
			dlog.Debugf(ctx, "forwarding to %d wiretaps", tapCount)
			clientConn, taps = AddWiretaps(ctx, clientConn, tapCount, wiretapCacheSize)
			wg := sync.WaitGroup{}
			wg.Add(tapCount)
			defer wg.Wait()
			for i, ii := range wtIntercepts {
				go func(conn net.Conn, intercept *manager.InterceptInfo) {
					defer wg.Done()
					dlog.Debugf(ctx, "wiretap to %d", ii.Spec.TargetPort)
					err := f.interceptConn(ctx, conn, intercept)
					if err != nil {
						dlog.Errorf(ctx, "wiretap ended with error: %v", err)
					}
				}(taps[i], ii)
			}
		}
	}
	if intercept != nil {
		return f.interceptConn(ctx, clientConn, intercept)
	}

	defer dlog.Debug(ctx, "Done forwarding")
	defer clientConn.Close()

	if targetAddr.Port() == 0 {
		dlog.Debug(ctx, "Forwarding to /dev/null")
		_, _ = io.Copy(io.Discard, clientConn)
		return nil
	}

	ctx = dlog.WithField(ctx, "target", targetAddr.String())

	dlog.Debug(ctx, "Forwarding...")

	targetConn, err := net.DialTCP("tcp", nil, net.TCPAddrFromAddrPort(targetAddr))
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

func (f *tcp) interceptConn(ctx context.Context, conn net.Conn, iCept *manager.InterceptInfo) error {
	spec := iCept.Spec
	ip, err := iputil.ParseAddr(spec.TargetHost)
	if err != nil {
		return err
	}
	return f.rerouteConn(
		ctx,
		conn,
		tunnel.SessionID(iCept.ClientSession.SessionId),
		netip.AddrPortFrom(ip, uint16(spec.TargetPort)),
		time.Duration(spec.RoundtripLatency),
		time.Duration(spec.DialTimeout))
}

func (f *tcp) rerouteConn(ctx context.Context, conn net.Conn, clientSession tunnel.SessionID, dst netip.AddrPort, latency, timeout time.Duration) error {
	srcAddr := conn.RemoteAddr()
	dlog.Debugf(ctx, "Accept got connection from %s", srcAddr)
	defer dlog.Debugf(ctx, "Done serving connection from %s", srcAddr)

	src, err := iputil.SplitToIPPort(conn.RemoteAddr())
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s: %w", srcAddr, err)
	}

	proto, err := types.ParseProto(srcAddr.Network())
	if err != nil {
		return fmt.Errorf("failed to parse intercept protocol %s: %w", srcAddr, err)
	}
	id := tunnel.NewConnID(proto, src, dst)
	ctx, cancel := context.WithCancel(ctx)
	f.mu.Lock()
	sp := f.streamProvider
	f.mu.Unlock()
	s, err := sp.CreateClientStream(ctx, tunnel.AgentToClient, clientSession, id, latency, timeout)
	if err != nil {
		cancel()
		return err
	}

	ingressBytes := tunnel.NewCounterProbe("FromClientBytes")
	egressBytes := tunnel.NewCounterProbe("ToClientBytes")

	// Ingress and egress swap places here, because this endpoint reflects a connection
	// where the stream is attached to a connection *to* the client, not *from* the client.
	d := tunnel.NewConnEndpoint(s, conn, cancel, egressBytes, ingressBytes)
	d.Start(ctx)
	<-d.Done()

	sp.ReportMetrics(ctx, &manager.TunnelMetrics{
		ClientSessionId: string(clientSession),
		IngressBytes:    ingressBytes.GetValue(),
		EgressBytes:     egressBytes.GetValue(),
	})
	return nil
}

// forwardHTTPConn handles HTTP-aware connection forwarding with header/path filtering.
func (f *tcp) forwardHTTPConn(
	ctx context.Context,
	clientConn net.Conn,
	httpIntercepts []*manager.InterceptInfo,
	target netip.AddrPort,
	wtIntercepts []*manager.InterceptInfo,
) error {
	// Create a temporary HTTP interceptor to handle this connection
	httpInterceptor := &httpInterceptor{
		interceptor: interceptor{
			tag:    f.tag,
			target: target,
			tCtx:   ctx,
		},
		originalTarget: target,
	}

	// Configure the HTTP interceptor with stream provider and all HTTP intercepts
	httpInterceptor.SetStreamProvider(f.streamProvider)
	httpInterceptor.SetInterceptingMultiple(ctx, httpIntercepts)

	// Add wiretaps if any
	for _, wt := range wtIntercepts {
		httpInterceptor.AddWiretap(wt)
	}

	// Handle the connection using HTTP logic
	return httpInterceptor.handleHTTPConn(clientConn)
}

// DispatchByMechanism implements mechanism-specific per-connection dispatch for TCP.
// It routes HTTP intercepts (with filters) to the HTTP interceptor.
func (f *tcp) DispatchByMechanism(ctx context.Context, clientConn net.Conn, intercept *manager.InterceptInfo) (bool, error) {
	f.mu.Lock()
	httpIntercepts := f.httpIntercepts
	target := f.target
	tapCount := len(f.wiretaps)
	var wtIntercepts []*manager.InterceptInfo
	if tapCount > 0 {
		wtIntercepts = make([]*manager.InterceptInfo, tapCount)
		i := 0
		for _, wt := range f.wiretaps {
			wtIntercepts[i] = wt
			i++
		}
	}
	f.mu.Unlock()

	httpMechanism := len(httpIntercepts) > 0

	switch {
	case httpMechanism:
		err := f.forwardHTTPConn(ctx, clientConn, httpIntercepts, target, wtIntercepts)
		return true, err
	default:
		return false, nil
	}
}
