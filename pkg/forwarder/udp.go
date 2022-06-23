package forwarder

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type udp struct {
	interceptor
	targets *tunnel.Pool
}

func newUDP(listen *net.UDPAddr, targetHost string, targetPort uint16) Interceptor {
	return &udp{
		interceptor: interceptor{
			listenAddr: listen,
			targetHost: targetHost,
			targetPort: targetPort,
		},
		targets: tunnel.NewPool(),
	}
}

func (f *udp) Serve(ctx context.Context, initCh chan<- net.Addr) error {
	// Set up listener lifetime (same as the overall forwarder lifetime)
	f.mu.Lock()
	la := f.listenAddr.(*net.UDPAddr)
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
		f.targets.CloseAll(ctx)
		dlog.Infof(ctx, "Done forwarding udp from %s", la)
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
		pc, err := lc.ListenPacket(ctx, la.Network(), la.String())
		if err != nil {
			return err
		}
		if first {
			// The address to listen to is likely to change the first time around, because it may
			// be ":0", so let's ensure that the same address is used next time
			la = pc.LocalAddr().(*net.UDPAddr)
			f.listenAddr = la
			dlog.Infof(ctx, "Forwarding udp from %s", la)
			if initCh != nil {
				initCh <- la
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
	var err error
	if intercept != nil {
		err = f.interceptConn(ctx, conn, intercept)
	} else {
		err = f.forwardConn(ctx, conn)
	}
	return err
}

// forwardConn reads packets from the given connection and writes the packages to the
// target host:port of this forwarder using a connection that will use the reply address
// from the read as the destination for packages going in the other direction.
func (f *udp) forwardConn(ctx context.Context, conn *net.UDPConn) error {
	targetAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", f.targetHost, f.targetPort))
	if err != nil {
		return fmt.Errorf("error on resolve(%s:%d): %w", f.targetHost, f.targetPort, err)
	}

	la := conn.LocalAddr()
	dlog.Infof(ctx, "Forwarding udp from %s to %s", la, targetAddr)
	defer func() {
		f.targets.CloseAll(ctx)
		dlog.Infof(ctx, "Done forwarding udp from %s to %s", la, targetAddr)
	}()

	ch := make(chan tunnel.UdpReadResult)
	go tunnel.UdpReader(ctx, conn, ch)
	for {
		select {
		case <-ctx.Done():
			return nil
		case rr, ok := <-ch:
			if !ok {
				return nil
			}
			id := tunnel.ConnIDFromUDP(rr.Addr, targetAddr)
			dlog.Tracef(ctx, "<- SRC udp %s, len %d", id, len(rr.Payload))
			h, _, err := f.targets.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (tunnel.Handler, error) {
				tc, err := net.DialUDP("udp", nil, id.DestinationAddr().(*net.UDPAddr))
				if err != nil {
					return nil, err
				}
				return &udpHandler{
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
					dlog.Errorf(ctx, "!! TRG udp %s write: %v", id, err)
					return err
				}
				dlog.Tracef(ctx, "-> TRG udp %s, len %d", id, wn)
				n += wn
			}
		}
	}
}

type udpHandler struct {
	*net.UDPConn
	id        tunnel.ConnID
	replyWith net.PacketConn
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
	go u.forward(ctx)
}

func (u *udpHandler) forward(ctx context.Context) {
	ch := make(chan tunnel.UdpReadResult)
	go tunnel.UdpReader(ctx, u, ch)
	for {
		select {
		case <-ctx.Done():
			return
		case rr, ok := <-ch:
			if !ok {
				return
			}
			dlog.Tracef(ctx, "<- TRG udp %s, len %d", u.id, len(rr.Payload))
			pn := len(rr.Payload)
			for n := 0; n < pn; {
				wn, err := u.replyWith.WriteTo(rr.Payload[n:], u.id.SourceAddr())
				if err != nil {
					dlog.Errorf(ctx, "!! SRC udp %s write: %v", u.id, err)
					return
				}
				dlog.Tracef(ctx, "-> SRC udp %s, len %d", u.id, wn)
				n += wn
			}
		}
	}
}

func (f *udp) interceptConn(ctx context.Context, conn *net.UDPConn, iCept *manager.InterceptInfo) error {
	spec := iCept.Spec
	dest := &net.UDPAddr{IP: iputil.Parse(spec.TargetHost), Port: int(spec.TargetPort)}

	dlog.Infof(ctx, "Forwarding udp from %s to %s %s", conn.LocalAddr(), spec.Client, dest)
	defer dlog.Infof(ctx, "Done forwarding udp from %s to %s %s", conn.LocalAddr(), spec.Client, dest)
	d := tunnel.NewUDPListener(conn, dest, func(ctx context.Context, id tunnel.ConnID) (tunnel.Stream, error) {
		ms, err := f.manager.Tunnel(ctx)
		if err != nil {
			return nil, fmt.Errorf("call to manager.Tunnel() failed. Id %s: %v", id, err)
		}
		s, err := tunnel.NewClientStream(ctx, ms, id, f.sessionInfo.SessionId, time.Duration(spec.RoundtripLatency), time.Duration(spec.DialTimeout))
		if err != nil {
			return nil, err
		}
		if err = s.Send(ctx, tunnel.SessionMessage(iCept.ClientSession.SessionId)); err != nil {
			return nil, fmt.Errorf("unable to send client session id. Id %s: %v", id, err)
		}
		return s, nil
	})
	d.Start(ctx)
	<-d.Done()
	return nil
}
