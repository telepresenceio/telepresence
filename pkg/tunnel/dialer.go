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
)

// The idleDuration controls how long a dialer for a specific proto+from-to address combination remains alive without
// reading or writing any messages. The dialer is normally closed by one of the peers.
const (
	tcpConnTTL = 2 * time.Hour // Default tcp_keepalive_time on Linux
	udpConnTTL = 1 * time.Minute
)

const (
	notConnected = int32(iota)
	connecting
	connected
)

// streamReader is implemented by the dialer and udpListener so that they can share the
// readLoop function.
type streamReader interface {
	Idle() <-chan time.Time
	ResetIdle() bool
	Stop(context.Context)
	getStream() Stream
	reply([]byte) (int, error)
	startDisconnect(context.Context, string)
}

// The dialer takes care of dispatching messages between gRPC and UDP connections.
type dialer struct {
	TimedHandler
	stream    Stream
	cancel    context.CancelFunc
	conn      net.Conn
	connected int32
	done      chan struct{}

	ingressBytesProbe *CounterProbe
	egressBytesProbe  *CounterProbe
}

// NewDialer creates a new handler that dispatches messages in both directions between the given gRPC stream
// and the given connection.
func NewDialer(
	stream Stream,
	cancel context.CancelFunc,
	ingressBytesProbe, egressBytesProbe *CounterProbe,
) Endpoint {
	return NewConnEndpoint(stream, nil, cancel, ingressBytesProbe, egressBytesProbe)
}

// NewDialerTTL creates a new handler that dispatches messages in both directions between the given gRPC stream
// and the given connection. The TTL decides how long the connection can be idle before it's closed.
//
// The handler remains active until it's been idle for the ttl duration, at which time it will automatically close
// and call the release function it got from the tunnel.Pool to ensure that it gets properly released.
func NewDialerTTL(stream Stream, cancel context.CancelFunc, ttl time.Duration, ingressBytesProbe, egressBytesProbe *CounterProbe) Endpoint {
	return NewConnEndpointTTL(stream, nil, cancel, ttl, ingressBytesProbe, egressBytesProbe)
}

func NewConnEndpoint(stream Stream, conn net.Conn, cancel context.CancelFunc, ingressBytesProbe, egressBytesProbe *CounterProbe) Endpoint {
	ttl := tcpConnTTL
	if stream.ID().Protocol() == ipproto.UDP {
		ttl = udpConnTTL
	}
	return NewConnEndpointTTL(stream, conn, cancel, ttl, ingressBytesProbe, egressBytesProbe)
}

func NewConnEndpointTTL(
	stream Stream,
	conn net.Conn,
	cancel context.CancelFunc,
	ttl time.Duration,
	ingressBytesProbe, egressBytesProbe *CounterProbe,
) Endpoint {
	state := notConnected
	if conn != nil {
		state = connecting
	}
	return &dialer{
		TimedHandler: NewTimedHandler(stream.ID(), ttl, nil),
		stream:       stream,
		cancel:       cancel,
		conn:         conn,
		connected:    state,
		done:         make(chan struct{}),

		ingressBytesProbe: ingressBytesProbe,
		egressBytesProbe:  egressBytesProbe,
	}
}

func (h *dialer) Start(ctx context.Context) {
	go func() {
		ctx, span := otel.Tracer("").Start(ctx, "dialer")
		defer span.End()
		defer close(h.done)

		id := h.stream.ID()
		id.SpanRecord(span)

		switch h.connected {
		case notConnected:
			// Set up the idle timer to close and release this handler when it's been idle for a while.
			h.connected = connecting

			dlog.Tracef(ctx, "   CONN %s, dialing", id)
			d := net.Dialer{Timeout: h.stream.DialTimeout()}
			conn, err := d.DialContext(ctx, id.DestinationProtocolString(), id.DestinationAddr().String())
			if err != nil {
				dlog.Errorf(ctx, "!! CONN %s, failed to establish connection: %v", id, err)
				span.SetStatus(codes.Error, err.Error())
				if err = h.stream.Send(ctx, NewMessage(DialReject, nil)); err != nil {
					dlog.Errorf(ctx, "!! CONN %s, failed to send DialReject: %v", id, err)
				}
				if err = h.stream.CloseSend(ctx); err != nil {
					dlog.Errorf(ctx, "!! CONN %s, stream.CloseSend failed: %v", id, err)
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
			dlog.Tracef(ctx, "   CONN %s, dial answered", id)
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

// Stop will close the underlying TCP/UDP connection.
func (h *dialer) Stop(ctx context.Context) {
	h.startDisconnect(ctx, "explicit close")
	h.cancel()
}

func (h *dialer) startDisconnect(ctx context.Context, reason string) {
	if !atomic.CompareAndSwapInt32(&h.connected, connected, notConnected) {
		return
	}
	id := h.stream.ID()
	dlog.Tracef(ctx, "   CONN %s closing connection: %s", id, reason)
	if err := h.conn.Close(); err != nil {
		dlog.Tracef(ctx, "!! CONN %s, Close failed: %v", id, err)
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
	WriteLoop(ctx, h.stream, outgoing, wg, h.egressBytesProbe)

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

func (h *dialer) getStream() Stream {
	return h.stream
}

func (h *dialer) reply(data []byte) (int, error) {
	return h.conn.Write(data)
}

func (h *dialer) streamToConnLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()
	readLoop(ctx, h, h.ingressBytesProbe)
}

func handleControl(ctx context.Context, h streamReader, cm Message) {
	switch cm.Code() {
	case DialReject, Disconnect: // Peer wants to hard-close. No more messages will arrive
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
		dlog.Errorf(ctx, "!! CONN %s: unhandled connection control message: %s", h.getStream().ID(), cm)
	}
}

func readLoop(ctx context.Context, h streamReader, trafficProbe *CounterProbe) {
	var endReason string
	endLevel := dlog.LogLevelTrace
	id := h.getStream().ID()
	defer func() {
		h.startDisconnect(ctx, endReason)
		dlog.Logf(ctx, endLevel, "   CONN %s stream-to-conn loop ended because %s", id, endReason)
	}()

	incoming, errCh := ReadLoop(ctx, h.getStream(), trafficProbe)
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
				handleControl(ctx, h, dg)
				continue
			}
			payload := dg.Payload()
			pn := len(payload)
			for n := 0; n < pn; {
				wn, err := h.reply(payload[n:])
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
func DialWaitLoop(
	ctx context.Context,
	tunnelProvider Provider,
	dialStream rpc.Manager_WatchDialClient,
	sessionID string,
) error {
	// create ctx to cleanup leftover dialRespond if waitloop dies
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for ctx.Err() == nil {
		dr, err := dialStream.Recv()
		if err != nil {
			if ctx.Err() == nil && !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
				return fmt.Errorf("dial request stream recv: %w", err) // May be io.EOF
			}
			return nil
		}
		go dialRespond(ctx, tunnelProvider, dr, sessionID)
	}
	return nil
}

func dialRespond(ctx context.Context, tunnelProvider Provider, dr *rpc.DialRequest, sessionID string) {
	if tc := dr.GetTraceContext(); tc != nil {
		carrier := propagation.MapCarrier(tc)
		propagator := otel.GetTextMapPropagator()
		ctx = propagator.Extract(ctx, carrier)
	}
	ctx, span := otel.Tracer("").Start(ctx, "dialRespond")
	defer span.End()
	id := ConnID(dr.ConnId)
	id.SpanRecord(span)
	mt, err := tunnelProvider.Tunnel(ctx)
	if err != nil {
		dlog.Errorf(ctx, "!! CONN %s, call to manager Tunnel failed: %v", id, err)
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s, err := NewClientStream(ctx, mt, id, sessionID, time.Duration(dr.RoundtripLatency), time.Duration(dr.DialTimeout))
	if err != nil {
		dlog.Error(ctx, err)
		cancel()
		return
	}
	d := NewDialer(s, cancel, nil, nil)
	d.Start(ctx)
	<-d.Done()
}
