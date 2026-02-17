package fwd

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/tls"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// interceptSnapshot holds an interceptController with its InterceptInfo captured at a point in time.
// This prevents race conditions where the InterceptInfo pointer could be updated by reconcile()
// while using it, leading to inconsistent TargetPort values and other data races.
type interceptSnapshot struct {
	ic   *interceptController
	info *manager.InterceptInfo
}

type tcp struct {
	*interceptor
	tlsManager     tls.Manager
	listenerSwitch ListenerSwitch
}

func NewTCPInterceptor(ctx context.Context, listenPort types.PortAndProto, tag tunnel.Tag, tlsManager tls.Manager, target netip.AddrPort) Interceptor {
	return &tcp{
		interceptor: newInterceptor(ctx, listenPort, tag, target),
		tlsManager:  tlsManager,
	}
}

func (f *tcp) IsHTTP() bool {
	return f.intercepts.isHTTP() || f.wiretaps.isHTTP()
}

// SetIntercepting overrides the base implementation to handle HTTP intercepts.
func (f *tcp) SetIntercepting(intercepts []*manager.InterceptInfo) {
	f.interceptor.SetIntercepting(intercepts)
	f.setListenerSwitch()
}

// SetWiretapping overrides the base implementation to handle HTTP intercepts.
func (f *tcp) SetWiretapping(intercepts []*manager.InterceptInfo) {
	f.interceptor.SetWiretapping(intercepts)
	f.setListenerSwitch()
}

// Configure the listener switch based on the intercepts. The switch will be on (HTTP) if there is at least
// one intercept with a header or path filter.
func (f *tcp) setListenerSwitch() {
	f.mu.Lock()
	if f.listenerSwitch != nil {
		f.listenerSwitch.Switch(f.IsHTTP())
	}
	f.mu.Unlock()
}

func (f *tcp) Serve(_ context.Context, initCh chan<- netip.AddrPort) error {
	ctx := f.lCtx
	listener, err := f.Listen(ctx)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		clog.Debugf(ctx, "Listener closed")
		listener.Close()
	}()

	la := listener.Addr().(*net.TCPAddr)
	if initCh != nil {
		initCh <- la.AddrPort()
		close(initCh)
	}

	clog.Debugf(ctx, "Forwarding from %s", la)
	defer clog.Debugf(ctx, "Done forwarding from %s", la)

	// The listener switch is used to switch between the primary listener (for TCP) and the secondary listener (for HTTP).
	// The switch is initially on the primary listener and will be switched to the secondary listener when there is at least
	// one intercept with a header or path filter.
	f.listenerSwitch = NewListenerSwitch(listener, func(l net.Listener) {
		f.acceptHTTPLoop(ctx, l)
	})
	f.setListenerSwitch()

	// Dispatch to the primary and secondary listeners in separate go routines.
	go forwarder.AcceptLoop(ctx, f.listenerSwitch.Primary(), f.Forward)

	return f.listenerSwitch.Serve()
}

// Number of []byte chunks that can be cached by a wiretap connection before it discards data.
const wiretapCacheSize = 0x100

func (f *tcp) Forward(ctx context.Context, clientConn net.Conn) error {
	f.mu.Lock()
	intercept, err := f.intercepts.global()
	wtIntercepts := f.wiretaps.sorted()

	// Create snapshots with captured InterceptInfo to avoid race condition.
	// reconcile() can update the InterceptInfo pointer concurrently, so we capture
	// it while holding the mutex to ensure consistent values throughout the connection.
	var interceptSnap *interceptSnapshot
	if intercept != nil {
		interceptSnap = &interceptSnapshot{ic: intercept, info: intercept.InterceptInfo}
	}
	wtSnapshots := make([]interceptSnapshot, len(wtIntercepts))
	for i, wt := range wtIntercepts {
		wtSnapshots[i] = interceptSnapshot{ic: wt, info: wt.InterceptInfo}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}

	ctx = clog.With(ctx, "client", clientConn.RemoteAddr().String())
	if f.Target().Port() > 0 {
		if tapCount := len(wtSnapshots); tapCount > 0 {
			var taps []net.Conn
			clog.Debugf(ctx, "forwarding to %d wiretaps", tapCount)
			clientConn, taps = addConnectionTaps(ctx, clientConn, tapCount, wiretapCacheSize)
			wg := sync.WaitGroup{}
			wg.Add(tapCount)
			defer wg.Wait()
			for i, snap := range wtSnapshots {
				go func(conn net.Conn, snap interceptSnapshot) {
					defer wg.Done()
					clog.Debugf(ctx, "wiretap to %d", snap.info.Spec.TargetPort)
					err := f.interceptConnWithInfo(conn, snap.ic, snap.info)
					if err != nil {
						clog.Errorf(ctx, "wiretap ended with error: %v", err)
					}
				}(taps[i], snap)
			}
		}
	}
	if interceptSnap != nil {
		defer clientConn.Close()
		return f.interceptConnWithInfo(clientConn, interceptSnap.ic, interceptSnap.info)
	}
	return f.Forwarder.Forward(ctx, clientConn)
}

// interceptConnWithInfo handles the intercept connection with a pre-captured InterceptInfo.
// This ensures that the InterceptInfo used is consistent throughout the connection handling,
// avoiding race conditions where reconcile() might update the pointer concurrently.
func (f *tcp) interceptConnWithInfo(conn net.Conn, ic *interceptController, ii *manager.InterceptInfo) error {
	ctx := ic.ctx
	srcAddr := conn.RemoteAddr()
	clog.Debugf(ctx, "Accept got connection from %s", srcAddr)
	defer clog.Debugf(ctx, "Done serving connection from %s", srcAddr)

	src, err := iputil.SplitToIPPort(conn.RemoteAddr())
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s: %w", srcAddr, err)
	}

	f.mu.Lock()
	sp := f.streamProvider
	f.mu.Unlock()
	s, err := f.createStream(ctx, src, ii)
	if err != nil {
		ic.cancel()
		return err
	}

	metricsEnabled := sp.MetricsEnabled()
	var ingressBytes, egressBytes *tunnel.CounterProbe
	if metricsEnabled {
		ingressBytes = tunnel.NewCounterProbe("FromClientBytes")
		egressBytes = tunnel.NewCounterProbe("ToClientBytes")
	}

	// Ingress and egress swap places here because this endpoint reflects a connection
	// where the stream is attached to a connection *to* the client, not *from* the client.
	d := tunnel.NewConnEndpoint(s, conn, func() {}, egressBytes, ingressBytes)
	d.Start(ctx)
	<-d.Done()

	if metricsEnabled {
		sp.ReportMetrics(ctx, &manager.TunnelMetrics{
			ClientSessionId: ii.ClientSession.SessionId,
			IngressBytes:    ingressBytes.GetValue(),
			EgressBytes:     egressBytes.GetValue(),
		})
	}
	return nil
}

func (f *tcp) createStream(ctx context.Context, src netip.AddrPort, ii *manager.InterceptInfo) (tunnel.Stream, error) {
	spec := ii.Spec
	ip, err := iputil.ParseAddr(spec.TargetHost)
	if err != nil {
		return nil, fmt.Errorf("failed to parse intercept target address %s: %w", spec.TargetHost, err)
	}
	dst := netip.AddrPortFrom(ip, uint16(spec.TargetPort))
	id := tunnel.NewConnID(types.ProtoTCP, src, dst)
	clientSession := tunnel.SessionID(ii.ClientSession.SessionId)
	latency := time.Duration(spec.RoundtripLatency)
	timeout := time.Duration(spec.DialTimeout)
	f.mu.Lock()
	sp := f.streamProvider
	f.mu.Unlock()
	s, err := sp.CreateClientStream(ctx, tunnel.AgentToClient, clientSession, id, latency, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create client stream: %w", err)
	}
	return s, nil
}
