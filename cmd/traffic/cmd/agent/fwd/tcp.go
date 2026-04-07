package fwd

import (
	"context"
	"errors"
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

var errClientStream = errors.New("failed to create client stream")

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
	f.mu.Unlock()
	if err != nil {
		return err
	}

	ctx = clog.With(ctx, "client", clientConn.RemoteAddr().String())
	if f.Target().Port() > 0 {
		if tapCount := len(wtIntercepts); tapCount > 0 {
			var taps []net.Conn
			clog.Debugf(ctx, "forwarding to %d wiretaps", tapCount)
			clientConn, taps = addConnectionTaps(ctx, clientConn, tapCount, wiretapCacheSize)
			wg := sync.WaitGroup{}
			wg.Add(tapCount)
			defer wg.Wait()
			for i, ii := range wtIntercepts {
				go func(conn net.Conn, intercept *interceptController) {
					defer wg.Done()
					clog.Debugf(ctx, "wiretap to %d", ii.Spec.TargetPort)
					err := f.interceptConn(conn, intercept)
					if err != nil {
						clog.Errorf(ctx, "wiretap ended with error: %v", err)
					}
				}(taps[i], ii)
			}
		}
	}
	if intercept != nil {
		defer clientConn.Close()
		return f.interceptConn(clientConn, intercept)
	}
	return f.Forwarder.Forward(ctx, clientConn)
}

func (f *tcp) interceptConn(conn net.Conn, ic *interceptController) error {
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
	s, err := f.createStream(ctx, src, ic.InterceptInfo)
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
			ClientSessionId: ic.ClientSession.SessionId,
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
		return nil, fmt.Errorf("%w: %w", errClientStream, err)
	}
	return s, nil
}
