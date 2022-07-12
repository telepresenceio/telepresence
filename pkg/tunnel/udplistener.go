package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
)

// The dialer takes care of dispatching messages between gRPC and UDP connections
type udpListener struct {
	TimedHandler
	conn       *net.UDPConn
	connected  int32
	done       chan struct{}
	targetAddr *net.UDPAddr
	targets    *Pool
	creator    func(context.Context, ConnID) (Stream, error)
}

func NewUDPListener(conn *net.UDPConn, targetAddr *net.UDPAddr, creator func(context.Context, ConnID) (Stream, error)) Endpoint {
	state := notConnected
	if conn != nil {
		state = connecting
	}
	return &udpListener{
		TimedHandler: NewTimedHandler("", udpConnTTL, nil),
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
	go UdpReader(ctx, h.conn, ch)
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
			id := ConnIDFromUDP(rr.Addr, h.targetAddr)
			target, _, err := h.targets.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (Handler, error) {
				s, err := h.creator(ctx, id)
				if err != nil {
					return nil, err
				}
				dlog.Debugf(ctx, "   LIS %s conn-to-stream loop started", id)
				return &udpStream{
					TimedHandler: NewTimedHandler(id, udpConnTTL, release),
					udpListener:  h,
					stream:       s,
				}, nil
			})
			if err != nil {
				dlog.Errorf(ctx, "!! MGR udp %s get target: %v", id, err)
				return
			}
			ps := target.(*udpStream)
			dlog.Tracef(ctx, "-> MGR %s, len %d", id, len(rr.Payload))
			err = ps.stream.Send(ctx, NewMessage(Normal, rr.Payload))
			if err != nil {
				dlog.Errorf(ctx, "!! MGR udp %s write: %v", id, err)
				return
			}
		}
	}
}

func (h *udpListener) Done() <-chan struct{} {
	return h.done
}

func (h *udpListener) getTTL() time.Duration {
	return udpConnTTL
}

type udpStream struct {
	TimedHandler
	*udpListener
	stream Stream
}

func (h *udpStream) handleControl(ctx context.Context, cm Message) {
	switch cm.Code() {
	case Disconnect: // Peer responded to our disconnect or wants to hard-close. No more messages will arrive
		h.Stop(ctx)
	case KeepAlive:
		h.ResetIdle()
	case DialOK:
	default:
		dlog.Errorf(ctx, "!! LIS %s: unhandled connection control message: %s", h.stream.ID(), cm)
	}
}

func (p *udpStream) Stop(ctx context.Context) {
	_ = p.stream.CloseSend(ctx)
}

func (p *udpStream) Start(ctx context.Context) {
	p.TimedHandler.Start(ctx)
	go p.readLoop(ctx)
}

func (p *udpStream) readLoop(ctx context.Context) {
	id := p.stream.ID()
	var endReason string
	endLevel := dlog.LogLevelDebug
	defer func() {
		dlog.Logf(ctx, endLevel, "   LIS %s stream-to-conn loop ended because %s", id, endReason)
	}()
	msgCh, errCh := ReadLoop(ctx, p.stream)
	dlog.Debugf(ctx, "   LIS %s stream-to-conn loop started", id)
	for {
		select {
		case <-ctx.Done():
			endReason = ctx.Err().Error()
			return
		case <-p.Idle():
			endReason = "it was idle for too long"
			return
		case err, ok := <-errCh:
			if ok {
				dlog.Error(ctx, err)
			}
		case dg, ok := <-msgCh:
			if !ok {
				// h.incoming was closed by the reader and is now drained.
				endReason = "there was no more input"
				return
			}
			if !p.ResetIdle() {
				endReason = "it was idle for too long"
				return
			}
			if dg.Code() != Normal {
				p.handleControl(ctx, dg)
				continue
			}
			payload := dg.Payload()
			pn := len(payload)
			for n := 0; n < pn; {
				wn, err := p.conn.WriteTo(payload[n:], id.SourceAddr())
				if err != nil {
					dlog.Errorf(ctx, "!! LIS %s write error %v", id, wn)
					endReason = "a write error occurred"
					endLevel = dlog.LogLevelError
					return
				}
				dlog.Tracef(ctx, "-> LIS %s, len %d", id, wn)
				n += wn
			}
		}
	}
}

type UdpReadResult struct {
	Payload []byte
	Addr    *net.UDPAddr
}

// IsTimeout returns true if the given error is a network timeout error
func IsTimeout(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Timeout()
}

// UdpReader continuously reads from a net.PacketConn and writes the resulting payload and
// reply address to a channel. The loop is cancelled when the connection is closed or when
// the context is done, at which time the channel is closed.
func UdpReader(ctx context.Context, conn net.PacketConn, ch chan<- UdpReadResult) {
	defer close(ch)
	var endReason string
	endLevel := dlog.LogLevelDebug
	defer func() {
		dlog.Logf(ctx, endLevel, "   LIS %s UDP read loop ended because %s", conn.LocalAddr(), endReason)
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
			ch <- UdpReadResult{pl, addr.(*net.UDPAddr)}
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
