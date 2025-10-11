package fwd

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type tcp struct {
	*interceptor
	httpIntercepts []*manager.InterceptInfo // Store multiple HTTP intercepts with filters
}

func newTCP(ctx context.Context, listenPort types.PortAndProto, tag tunnel.Tag, target netip.AddrPort) Interceptor {
	return &tcp{
		interceptor: newInterceptor(ctx, listenPort, tag, target),
	}
}

func (f *tcp) IsHTTP() bool {
	return f.intercepts.isHTTP() || f.wiretaps.isHTTP()
}

func (f *tcp) Serve(_ context.Context, initCh chan<- netip.AddrPort) error {
	ctx := f.lCtx
	listener, err := f.Listen(ctx)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		dlog.Debugf(ctx, "Listener closed")
		listener.Close()
	}()

	la := listener.Addr().(*net.TCPAddr)
	if initCh != nil {
		initCh <- la.AddrPort()
		close(initCh)
	}

	dlog.Debugf(ctx, "Forwarding from %s", la)
	defer dlog.Debugf(ctx, "Done forwarding from %s", la)

	go forwarder.AcceptLoop(ctx, listener, f.forwardConn)
	<-ctx.Done()
	return nil
}

// Number of []byte chunks that can be cached by a wiretap connection before it discards data.
const wiretapCacheSize = 0x100

func (f *tcp) forwardConn(ctx context.Context, clientConn net.Conn) error {
	// Give mechanism-specific handling a chance first (e.g., HTTP-aware routing)
	if f.IsHTTP() {
		return f.forwardHTTPConn(ctx, clientConn)
	}

	f.mu.Lock()
	intercept, err := f.intercepts.global()
	wtIntercepts := f.wiretaps.sorted()
	f.mu.Unlock()
	if err != nil {
		return err
	}

	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())

	if f.Target().Port() > 0 {
		if tapCount := len(wtIntercepts); tapCount > 0 {
			var taps []net.Conn
			dlog.Debugf(ctx, "forwarding to %d wiretaps", tapCount)
			clientConn, taps = AddWiretaps(ctx, clientConn, tapCount, wiretapCacheSize)
			wg := sync.WaitGroup{}
			wg.Add(tapCount)
			defer wg.Wait()
			for i, ii := range wtIntercepts {
				go func(conn net.Conn, intercept *interceptController) {
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
	return f.Forwarder.Forward(ctx, clientConn)
}

func (f *tcp) interceptConn(ctx context.Context, conn net.Conn, iCept *interceptController) error {
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
) error {
	// Create a temporary HTTP interceptor to handle this connection
	httpInterceptor := &httpInterceptor{
		interceptor: newInterceptor(ctx, f.ListenPort(), f.Tag(), f.Target()),
	}
	f.mu.Lock()
	httpInterceptor.wiretaps = f.wiretaps
	httpInterceptor.intercepts = f.intercepts
	f.mu.Unlock()

	// Configure the HTTP interceptor with stream provider and all HTTP intercepts
	httpInterceptor.SetStreamProvider(f.streamProvider)

	// Handle the connection using HTTP logic
	return httpInterceptor.handleHTTPConn(clientConn)
}
