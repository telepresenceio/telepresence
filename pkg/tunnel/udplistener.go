package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/dlib/v2/dlog"
)

// The dialer takes care of dispatching messages between gRPC and UDP connections.
type udpListener struct {
	TimedHandler
	tag        Tag
	conn       *net.UDPConn
	connected  int32
	done       chan struct{}
	targetAddr netip.AddrPort
	targets    *Pool
	creator    func(context.Context, ConnID) (Stream, error)
}

func NewUDPListener(conn *net.UDPConn, tag Tag, targetAddr netip.AddrPort, creator func(context.Context, ConnID) (Stream, error)) Endpoint {
	state := notConnected
	if conn != nil {
		state = connecting
	}
	return &udpListener{
		TimedHandler: NewTimedHandler("", udpConnTTL, nil),
		tag:          tag,
		conn:         conn,
		connected:    state,
		done:         make(chan struct{}),
		targetAddr:   targetAddr,
		targets:      NewPool(),
		creator:      creator,
	}
}

func (h *udpListener) Start(ctx context.Context) {
	h.TimedHandler.Start(ctx)
	go func() {
		defer close(h.done)

		h.connected = connected
		h.connToStreamLoop(ctx)
		h.Stop(ctx)
	}()
}

func (h *udpListener) connToStreamLoop(ctx context.Context) {
	ch := make(chan UdpReadResult)
	go UdpReader(ctx, h.tag, h.conn, ch)
	for atomic.LoadInt32(&h.connected) == connected {
		select {
		case <-ctx.Done():
			return
		case <-h.Idle():
			return
		case rr, ok := <-ch:
			if !ok {
				return
			}
			h.ResetIdle()
			id := ConnIDFromUDP(rr.Address, h.targetAddr)
			target, _, err := h.targets.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (Handler, error) {
				s, err := h.creator(ctx, id)
				if err != nil {
					return nil, err
				}
				dlog.Debugf(ctx, "-> %s %s conn-to-stream loop started", h.tag, id)
				return &udpStream{
					TimedHandler: NewTimedHandler(id, udpConnTTL, release),
					udpListener:  h,
					stream:       s,
				}, nil
			})
			if err != nil {
				dlog.Errorf(ctx, ">! %s udp %s get target: %v", h.tag, id, err)
				return
			}
			ps := target.(*udpStream)
			dlog.Tracef(ctx, "-> %s %s, len %d", h.tag, id, len(rr.Payload))
			err = ps.stream.Send(ctx, NewMessage(Normal, rr.Payload))
			if err != nil {
				dlog.Errorf(ctx, ">! %s udp %s write: %v", h.tag, id, err)
				return
			}
		}
	}
}

func (h *udpListener) Done() <-chan struct{} {
	return h.done
}

type udpStream struct {
	TimedHandler
	*udpListener
	stream Stream
}

func (p *udpStream) getStream() Stream {
	return p.stream
}

func (p *udpStream) reply(data []byte) (int, error) {
	return p.conn.WriteTo(data, net.UDPAddrFromAddrPort(p.ID.Source()))
}

func (p *udpStream) startDisconnect(context.Context, string, bool) {
}

func (p *udpStream) Stop(ctx context.Context) {
	_ = p.stream.CloseSend(ctx)
}

func (p *udpStream) Start(ctx context.Context) {
	p.TimedHandler.Start(ctx)
	go readLoop(ctx, p.stream.Tag(), p, nil)
}

type UdpReadResult struct {
	Payload []byte
	Address netip.AddrPort
}

// IsTimeout returns true if the given error is a network timeout error.
func IsTimeout(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Timeout()
}

// UdpReader continuously reads from a net.PacketConn and writes the resulting payload and
// reply address to a channel. The loop is canceled when the connection is closed or when
// the context is done, at which time the channel is closed.
func UdpReader(ctx context.Context, tag Tag, conn net.PacketConn, ch chan<- UdpReadResult) {
	defer close(ch)
	var endReason string
	endLevel := dlog.LogLevelTrace
	defer func() {
		dlog.Logf(ctx, endLevel, "<- %s %s UDP read loop ended because %s", tag, conn.LocalAddr(), endReason)
	}()
	buf := [0x10000]byte{}
	for {
		if err := ctx.Err(); err != nil {
			endReason = err.Error()
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := conn.ReadFrom(buf[:])
		if n > 0 {
			pl := make([]byte, n)
			copy(pl, buf[:n])
			ch <- UdpReadResult{pl, addr.(*net.UDPAddr).AddrPort()}
		}
		switch {
		case err == nil, IsTimeout(err):
			continue
		case errors.Is(err, io.EOF):
			endReason = "EOF was encountered"
		case errors.Is(err, net.ErrClosed):
			endReason = "the connection was closed"
		default:
			endReason = fmt.Sprintf("a read error occurred: %v", err)
			endLevel = dlog.LogLevelError
		}
		break
	}
}
