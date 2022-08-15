package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

// The idleDuration controls how long a dialer for a specific proto+from-to address combination remains alive without
// reading or writing any messages. The dialer is normally closed by one of the peers.
const tcpConnTTL = 2 * time.Hour // Default tcp_keepalive_time on Linux
const udpConnTTL = 1 * time.Minute

const (
	notConnected = int32(iota)
	connecting
	connected
)

// The dialer takes care of dispatching messages between gRPC and UDP connections
type dialer struct {
	TimedHandler
	stream    Stream
	conn      net.Conn
	connected int32
	done      chan struct{}
}

// NewDialer creates a new handler that dispatches messages in both directions between the given gRPC stream
// and the given connection.
//
// The handler remains active until it's been idle for idleDuration, at which time it will automatically close
// and call the release function it got from the tunnel.Pool to ensure that it gets properly released.
func NewDialer(stream Stream) Endpoint {
	return NewConnEndpoint(stream, nil)
}

func NewConnEndpoint(stream Stream, conn net.Conn) Endpoint {
	ttl := tcpConnTTL
	if stream.ID().Protocol() == ipproto.UDP {
		ttl = udpConnTTL
	}
	state := notConnected
	if conn != nil {
		state = connecting
	}
	return &dialer{
		TimedHandler: NewTimedHandler(stream.ID(), ttl, nil),
		stream:       stream,
		conn:         conn,
		connected:    state,
		done:         make(chan struct{}),
	}
}

func (h *dialer) Start(ctx context.Context) {
	go func() {
		ctx, span := otel.Tracer("").Start(ctx, "dialer")
		defer span.End()
		defer close(h.done)

		id := h.stream.ID()
		tracing.RecordConnID(span, id.String())

		switch h.connected {
		case notConnected:
			// Set up the idle timer to close and release this handler when it's been idle for a while.
			h.connected = connecting

			dlog.Debugf(ctx, "   CONN %s, dialing", id)
			d := net.Dialer{Timeout: h.stream.DialTimeout()}
			conn, err := d.DialContext(ctx, id.ProtocolString(), id.DestinationAddr().String())
			if err != nil {
				dlog.Errorf(ctx, "!! CONN %s, failed to establish connection: %v", id, err)
				span.SetStatus(codes.Error, err.Error())
				if err = h.stream.Send(ctx, NewMessage(DialReject, nil)); err != nil {
					dlog.Errorf(ctx, "!! CONN %s, failed to send DialReject: %v", id, err)
				}
				h.connected = notConnected
				return
			}
			if err = h.stream.Send(ctx, NewMessage(DialOK, nil)); err != nil {
				_ = conn.Close()
				dlog.Errorf(ctx, "!! CONN %s, failed to send DialOK: %v", id, err)
				span.SetStatus(codes.Error, err.Error())
				return
			}
			dlog.Debugf(ctx, "   CONN %s, dial answered", id)
			h.conn = conn

		case connecting:
		default:
			dlog.Errorf(ctx, "!! CONN %s, start called in invalid state", id)
			return
		}

		// Set up the idle timer to close and release this endpoint when it's been idle for a while.
		h.TimedHandler.Start(ctx)
		h.connected = connected

		wg := sync.WaitGroup{}
		wg.Add(2)
		go h.connToStreamLoop(ctx, &wg)
		go h.streamToConnLoop(ctx, &wg)
		wg.Wait()
		h.Stop(ctx)
	}()
}

func (h *dialer) Done() <-chan struct{} {
	return h.done
}

func (h *dialer) handleControl(ctx context.Context, cm Message) {
	switch cm.Code() {
	case Disconnect: // Peer wants to hard-close. No more messages will arrive
		h.Stop(ctx)
	case KeepAlive:
		h.ResetIdle()
	case DialOK:
		// So how can a dialer get a DialOK from a peer? Surely, there cannot be a dialer at both ends?
		// Well, the story goes like this:
		// 1. A request to the service is made on the workstation.
		// 2. This agent's listener receives a connection.
		// 3. Since an intercept is active, the agent creates a tunnel to the workstation
		// 4. A new dialer is attached to that tunnel (reused as a tunnel endpoint)
		// 5. The dialer at the workstation dials and responds with DialOK, and here we are.
	default:
		dlog.Errorf(ctx, "!! CONN %s: unhandled connection control message: %s", h.stream.ID(), cm)
	}
}

// Stop will close the underlying TCP/UDP connection
func (h *dialer) Stop(ctx context.Context) {
	h.startDisconnect(ctx, "explicit close")
}

func (h *dialer) startDisconnect(ctx context.Context, reason string) {
	if !atomic.CompareAndSwapInt32(&h.connected, connected, notConnected) {
		return
	}
	id := h.stream.ID()
	dlog.Debugf(ctx, "   CONN %s closing connection: %s", id, reason)
	if err := h.conn.Close(); err != nil {
		dlog.Debugf(ctx, "!! CONN %s, Close failed: %v", id, err)
	}
}

func (h *dialer) connToStreamLoop(ctx context.Context, wg *sync.WaitGroup) {
	var endReason string
	endLevel := dlog.LogLevelTrace
	id := h.stream.ID()

	outgoing := make(chan Message, 50)
	defer func() {
		if !h.ResetIdle() {
			// Hard close of peer. We don't want any more data
			select {
			case outgoing <- NewMessage(Disconnect, nil):
			default:
			}
		}
		close(outgoing)
		dlog.Logf(ctx, endLevel, "   CONN %s conn-to-stream loop ended because %s", id, endReason)
		wg.Done()
	}()

	wg.Add(1)
	WriteLoop(ctx, h.stream, outgoing, wg)

	buf := make([]byte, 0x100000)
	dlog.Tracef(ctx, "   CONN %s conn-to-stream loop started", id)
	for {
		n, err := h.conn.Read(buf)
		if n > 0 {
			dlog.Tracef(ctx, "<- CONN %s, len %d", id, n)
			select {
			case <-ctx.Done():
				endReason = ctx.Err().Error()
				return
			case outgoing <- NewMessage(Normal, buf[:n]):
			}
		}

		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				endReason = "EOF was encountered"
			case errors.Is(err, net.ErrClosed):
				endReason = "the connection was closed"
				h.startDisconnect(ctx, endReason)
			default:
				endReason = fmt.Sprintf("a read error occurred: %v", err)
				endLevel = dlog.LogLevelError
			}
			return
		}

		if !h.ResetIdle() {
			endReason = "it was idle for too long"
			return
		}
	}
}

func (h *dialer) streamToConnLoop(ctx context.Context, wg *sync.WaitGroup) {
	var endReason string
	endLevel := dlog.LogLevelTrace
	id := h.stream.ID()
	defer func() {
		h.startDisconnect(ctx, endReason)
		wg.Done()
		dlog.Logf(ctx, endLevel, "   CONN %s stream-to-conn loop ended because %s", id, endReason)
	}()

	incoming, errCh := ReadLoop(ctx, h.stream)

	dlog.Tracef(ctx, "   CONN %s stream-to-conn loop started", id)
	for {
		select {
		case <-ctx.Done():
			endReason = ctx.Err().Error()
			return
		case <-h.Idle():
			endReason = "it was idle for too long"
			return
		case err, ok := <-errCh:
			if ok {
				dlog.Error(ctx, err)
			}
		case dg, ok := <-incoming:
			if !ok {
				// h.incoming was closed by the reader and is now drained.
				endReason = "there was no more input"
				return
			}
			if !h.ResetIdle() {
				endReason = "it was idle for too long"
				return
			}
			if dg.Code() != Normal {
				h.handleControl(ctx, dg)
				continue
			}
			payload := dg.Payload()
			pn := len(payload)
			for n := 0; n < pn; {
				wn, err := h.conn.Write(payload[n:])
				if err != nil {
					endReason = fmt.Sprintf("a write error occurred: %v", err)
					endLevel = dlog.LogLevelError
					return
				}
				dlog.Tracef(ctx, "-> CONN %s, len %d", id, wn)
				n += wn
			}
		}
	}
}

// DialWaitLoop reads from the given dialStream. A new goroutine that creates a Tunnel to the manager and then
// attaches a dialer Endpoint to that tunnel is spawned for each request that arrives. The method blocks until
// the dialStream is closed.
func DialWaitLoop(ctx context.Context, manager rpc.ManagerClient, dialStream rpc.Manager_WatchDialClient, sessionID string) error {
	for ctx.Err() == nil {
		dr, err := dialStream.Recv()
		if err != nil {
			if ctx.Err() == nil && !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
				return fmt.Errorf("dial request stream recv: %w", err) // May be io.EOF
			}
			return nil
		}
		go dialRespond(ctx, manager, dr, sessionID)
	}
	return nil
}

func dialRespond(ctx context.Context, manager rpc.ManagerClient, dr *rpc.DialRequest, sessionID string) {
	if tc := dr.GetTraceContext(); tc != nil {
		carrier := propagation.MapCarrier(tc)
		propagator := otel.GetTextMapPropagator()
		ctx = propagator.Extract(ctx, carrier)
	}
	ctx, span := otel.Tracer("").Start(ctx, "dialRespond")
	defer span.End()
	id := ConnID(dr.ConnId)
	tracing.RecordConnID(span, id.String())
	mt, err := manager.Tunnel(ctx)
	if err != nil {
		dlog.Errorf(ctx, "!! CONN %s, call to manager Tunnel failed: %v", id, err)
		return
	}
	s, err := NewClientStream(ctx, mt, id, sessionID, time.Duration(dr.RoundtripLatency), time.Duration(dr.DialTimeout))
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	d := NewDialer(s)
	d.Start(ctx)
	<-d.Done()
}
