package forwarder

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type udp struct{ basic }

// NewUDP creates a new UDP forwarder that will forward connections from the given port to the given target.
func NewUDP(from uint16, tag tunnel.Tag, target netip.AddrPort) Forwarder {
	return &udp{basic{
		tag:        tag,
		target:     target,
		listenPort: int32(from),
	}}
}

func (f *udp) Listen(ctx context.Context) (net.Listener, error) {
	return nil, fmt.Errorf("listen is not implemented for UDP")
}

// ListenPort returns the port that this forwarder will listen to. This port will be updated
// by a call to Serve if the port was initially zero.
func (f *udp) ListenPort() types.PortAndProto {
	return types.PortAndProto{Proto: types.ProtoUDP, Port: uint16(atomic.LoadInt32(&f.listenPort))}
}

func (f *udp) Serve(ctx context.Context, initCh chan<- netip.AddrPort) error {
	return f.ServeTo(ctx, initCh, f.Forward)
}

func (f *udp) Forward(ctx context.Context, conn net.Conn) error {
	if udpConn, ok := conn.(*net.UDPConn); ok {
		return ForwardUDP(ctx, f.tag, udpConn, f.target)
	}
	return fmt.Errorf("not a UDP connection")
}

func (f *udp) ServeTo(ctx context.Context, initCh chan<- netip.AddrPort, fw func(context.Context, net.Conn) error) error {
	// Set up listener lifetime (same as the overall forwarder lifetime)
	lp := uint16(atomic.LoadInt32(&f.listenPort))
	defer func() {
		if initCh != nil {
			close(initCh)
		}
		clog.Infof(ctx, "Done forwarding udp from :%d", lp)
	}()

	for first := true; ; first = false {
		select {
		case <-ctx.Done():
			return nil
		default:
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
			atomic.StoreInt32(&f.listenPort, int32(la.Port))
			clog.Infof(ctx, "Forwarding udp from %s", la)
			if initCh != nil {
				initCh <- la.AddrPort()
				close(initCh)
				initCh = nil
			}
		}
		err = fw(ctx, pc.(*net.UDPConn))
		if err != nil {
			return err
		}
	}
}

// ForwardUDP reads packets from the given connection and writes the packages to the
// target host:port of this forwarder using a connection that will use the reply address
// from the read as the destination for packages going in the other direction.
func ForwardUDP(ctx context.Context, tag tunnel.Tag, conn *net.UDPConn, targetAddr netip.AddrPort) error {
	targets := tunnel.NewPool()
	la := conn.LocalAddr()
	clog.Infof(ctx, "Forwarding udp from %s to %s", la, targetAddr)
	defer func() {
		targets.CloseAll(ctx)
		_ = conn.Close()
		clog.Infof(ctx, "Done forwarding udp from %s to %s", la, targetAddr)
	}()
	if targetAddr.Port() == 0 {
		clog.Debug(ctx, "Forwarding to /dev/null")
		return nil
	}

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
			clog.Tracef(ctx, "<- %s udp %s, len %d", tag, id, len(rr.Payload))
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
					clog.Errorf(ctx, "!> %s udp %s write: %v", tag, id, err)
					return err
				}
				clog.Tracef(ctx, "-> %s udp %s, len %d", tag, id, wn)
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
					clog.Errorf(ctx, "!> %s udp %s write: %v", tag, u.id, err)
					return
				}
				clog.Tracef(ctx, "-> %s udp %s, len %d", tag, u.id, wn)
				n += wn
			}
		}
	}
}
