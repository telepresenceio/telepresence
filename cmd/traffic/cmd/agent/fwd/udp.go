package fwd

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type udp struct {
	*interceptor
}

func newUDP(ctx context.Context, listenPort types.PortAndProto, tag tunnel.Tag, target netip.AddrPort) Interceptor {
	return &udp{interceptor: newInterceptor(ctx, listenPort, tag, target)}
}

// DispatchByMechanism implements the Interceptor hook for UDP. Currently, HTTP-aware
// mechanisms are not supported on UDP; all UDP traffic is forwarded unconditionally.
func (f *udp) DispatchByMechanism(ctx context.Context, _ net.Conn, intercept *manager.InterceptInfo) (bool, error) {
	return false, nil
}

func (f *udp) Serve(_ context.Context, initCh chan<- netip.AddrPort) error {
	return f.ServeTo(f.lCtx, initCh, f.Forward)
}

func (f *udp) Forward(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	f.mu.Lock()
	intercept := f.intercept
	f.mu.Unlock()
	if intercept != nil {
		return f.interceptConn(ctx, conn.(*net.UDPConn), intercept)
	}
	return f.Forwarder.Forward(ctx, conn)
}

func (f *udp) interceptConn(ctx context.Context, conn *net.UDPConn, iCept *manager.InterceptInfo) error {
	spec := iCept.Spec
	ip, err := iputil.ParseAddr(spec.TargetHost)
	if err != nil {
		return err
	}
	dest := netip.AddrPortFrom(ip, uint16(spec.TargetPort))
	dlog.Infof(ctx, "Forwarding udp from %s to %s %s", conn.LocalAddr(), spec.Client, dest)
	defer dlog.Infof(ctx, "Done forwarding udp from %s to %s %s", conn.LocalAddr(), spec.Client, dest)
	d := tunnel.NewUDPListener(conn, tunnel.AgentToClient, dest, func(ctx context.Context, id tunnel.ConnID) (tunnel.Stream, error) {
		f.mu.Lock()
		sp := f.streamProvider
		f.mu.Unlock()
		return sp.CreateClientStream(
			ctx, tunnel.AgentToClient, tunnel.SessionID(iCept.ClientSession.SessionId), id, time.Duration(spec.RoundtripLatency), time.Duration(spec.DialTimeout))
	})
	d.Start(ctx)
	<-d.Done()
	return nil
}
