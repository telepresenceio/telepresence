package forwarder

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type udp struct {
	interceptor
}

func newUDP(listenPort uint16, tag tunnel.Tag, target netip.AddrPort) Interceptor {
	return &udp{
		interceptor: interceptor{
			tag:        tag,
			listenPort: listenPort,
			target:     target,
			lCancel:    func() {},
		},
	}
}

func (f *udp) Serve(ctx context.Context, initCh chan<- netip.AddrPort) error {
	// Set up listener lifetime (same as the overall forwarder lifetime)
	f.mu.Lock()
	lp := f.listenPort
	ctx, f.lCancel = context.WithCancel(ctx)
	f.lCtx = ctx

	// Set up target lifetime
	f.tCtx, f.tCancel = context.WithCancel(ctx)
	f.mu.Unlock()

	defer func() {
		if initCh != nil {
			close(initCh)
		}
		f.lCancel()
		dlog.Infof(ctx, "Done forwarding udp from :%d", lp)
	}()

	for first := true; ; first = false {
		f.mu.Lock()
		ctx = f.tCtx
		intercept := f.intercept
		f.mu.Unlock()
		if ctx.Err() != nil {
			return nil
		}
		lc := net.ListenConfig{}
		pc, err := lc.ListenPacket(ctx, "udp", fmt.Sprintf(":%d", lp))
		if err != nil {
			return err
		}
		if first {
			// The address to listen to is likely to change the first time around, because it may
			// be ":0", so let's ensure that the same address is used next time
			la := pc.LocalAddr().(*net.UDPAddr)
			lp = uint16(la.Port)
			f.listenPort = lp
			dlog.Infof(ctx, "Forwarding udp from %s", la)
			if initCh != nil {
				initCh <- la.AddrPort()
				close(initCh)
				initCh = nil
			}
		}
		if err := f.forward(ctx, pc.(*net.UDPConn), intercept); err != nil {
			return err
		}
	}
}

func (f *udp) forward(ctx context.Context, conn *net.UDPConn, intercept *manager.InterceptInfo) error {
	defer conn.Close()
	if intercept != nil {
		return f.interceptConn(ctx, conn, intercept)
	}

	if f.target.Port() == 0 {
		dlog.Debug(ctx, "Forwarding to /dev/null")
		return nil
	}
	return f.forwardConn(ctx, conn)
}

// DispatchByMechanism implements the Interceptor hook for UDP. Currently, HTTP-aware
// mechanisms are not supported on UDP; we log and proceed with normal UDP forwarding.
func (f *udp) DispatchByMechanism(ctx context.Context, _ net.Conn, intercept *manager.InterceptInfo) (bool, error) {
	if intercept != nil && intercept.Spec != nil && intercept.Spec.HttpMechanism {
		dlog.Warn(ctx, "HTTP mechanism requested on UDP listener; not supported. Proceeding with plain UDP forwarding.")
	}
	return false, nil
}

// forwardConn reads packets from the given connection and writes the packages to the
// target host:port of this forwarder using a connection that will use the reply address
// from the read as the destination for packages going in the other direction.
func (f *udp) forwardConn(ctx context.Context, conn *net.UDPConn) error {
	return ForwardUDP(ctx, f.tag, conn, f.target)
}

func ForwardUDP(ctx context.Context, tag tunnel.Tag, conn *net.UDPConn, targetAddr netip.AddrPort) error {
	targets := tunnel.NewPool()
	la := conn.LocalAddr()
	dlog.Infof(ctx, "Forwarding udp from %s to %s", la, targetAddr)
	defer func() {
		targets.CloseAll(ctx)
		dlog.Infof(ctx, "Done forwarding udp from %s to %s", la, targetAddr)
	}()

	ch := make(chan tunnel.UdpReadResult)
	go tunnel.UdpReader(ctx, tag, conn, ch)
	for {
		select {
		case <-ctx.Done():
			return nil
		case rr, ok := <-ch:
			if !ok {
				return nil
			}
			id := tunnel.ConnIDFromUDP(rr.Address, targetAddr)
			dlog.Tracef(ctx, "<- %s udp %s, len %d", tag, id, len(rr.Payload))
			h, _, err := targets.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (tunnel.Handler, error) {
				tc, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(id.Destination()))
				if err != nil {
					return nil, err
				}
				return &udpHandler{
					tag:       tag,
					UDPConn:   tc,
					id:        id,
					replyWith: conn,
					release:   release,
				}, nil
			})
			if err != nil {
				return err
			}
			uh := h.(*udpHandler)
			pn := len(rr.Payload)
			for n := 0; n < pn; {
				wn, err := uh.Write(rr.Payload[n:])
				if err != nil {
					dlog.Errorf(ctx, "!> %s udp %s write: %v", tag, id, err)
					return err
				}
				dlog.Tracef(ctx, "-> %s udp %s, len %d", tag, id, wn)
				n += wn
			}
		}
	}
}

type udpHandler struct {
	*net.UDPConn
	id        tunnel.ConnID
	replyWith net.PacketConn
	tag       tunnel.Tag
	release   func()
}

func (u *udpHandler) Close() error {
	u.release()
	return u.UDPConn.Close()
}

func (u *udpHandler) Stop(_ context.Context) {
	_ = u.Close()
}

func (u *udpHandler) Start(ctx context.Context) {
	go u.forward(ctx, u.tag)
}

func (u *udpHandler) forward(ctx context.Context, tag tunnel.Tag) {
	ch := make(chan tunnel.UdpReadResult)
	go tunnel.UdpReader(ctx, tag, u, ch)
	for {
		select {
		case <-ctx.Done():
			return
		case rr, ok := <-ch:
			if !ok {
				return
			}
			pn := len(rr.Payload)
			for n := 0; n < pn; {
				wn, err := u.replyWith.WriteTo(rr.Payload[n:], net.UDPAddrFromAddrPort(u.id.Source()))
				if err != nil {
					dlog.Errorf(ctx, "!> %s udp %s write: %v", tag, u.id, err)
					return
				}
				dlog.Tracef(ctx, "-> %s udp %s, len %d", tag, u.id, wn)
				n += wn
			}
		}
	}
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
