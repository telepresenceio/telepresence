package fwd

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/telepresenceio/clog"
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

func (f *udp) IsHTTP() bool {
	return false
}

func (f *udp) Serve(_ context.Context, initCh chan<- netip.AddrPort) error {
	return f.ServeTo(f.lCtx, initCh, f.Forward)
}

func (f *udp) Forward(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	f.mu.Lock()
	intercept, err := f.intercepts.global()
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if intercept != nil {
		return f.interceptConn(conn, intercept)
	}
	return f.Forwarder.Forward(ctx, conn)
}

func (f *udp) interceptConn(conn net.Conn, ic *interceptController) error {
	spec := ic.Spec
	ip, err := iputil.ParseAddr(spec.TargetHost)
	if err != nil {
		return err
	}
	dest := netip.AddrPortFrom(ip, uint16(spec.TargetPort))
	ctx := ic.ctx
	clog.Infof(ctx, "Forwarding udp from %s to %s %s", conn.LocalAddr(), spec.Client, dest)
	defer clog.Infof(ctx, "Done forwarding udp from %s to %s %s", conn.LocalAddr(), spec.Client, dest)
	d := tunnel.NewUDPListener(conn.(*net.UDPConn), tunnel.AgentToClient, dest, func(ctx context.Context, id tunnel.ConnID) (tunnel.Stream, error) {
		f.mu.Lock()
		sp := f.streamProvider
		f.mu.Unlock()
		return sp.CreateClientStream(
			ctx, tunnel.AgentToClient, tunnel.SessionID(ic.ClientSession.SessionId), id, time.Duration(spec.RoundtripLatency), time.Duration(spec.DialTimeout))
	})
	d.Start(ctx)
	<-d.Done()
	return nil
}
