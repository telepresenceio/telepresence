package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// The idleDuration controls how long a dialer for a specific proto+from-to address combination remains alive without
// reading or writing any messages. The dialer is normally closed by one of the peers.
const (
	tcpConnTTL       = 2 * time.Hour // Default tcp_keepalive_time on Linux
	udpConnTTL       = 2 * time.Second
	localDialTimeout = 2 * time.Second
)

// Limit selected-intercept dial responders so bursty workloads cannot create
// unbounded goroutines and gRPC tunnels in the client daemon.
const maxConcurrentDialResponders = 256

const (
	notConnected = int32(iota)
	connecting
	connected
	readClosed
	writeClosed
)

type Dialer interface {
	DialTCP(context.Context, netip.AddrPort) (conn net.Conn, err error)
	DialUDP(context.Context, netip.AddrPort, netip.AddrPort) (conn net.Conn, err error)
}

type halfReadCloser interface {
	CloseRead() error
}

type halfWriteCloser interface {
	CloseWrite() error
}

type halfCloser interface {
	halfReadCloser
	halfWriteCloser
}

// streamReader is implemented by the dialer and udpListener so that they can share the
// readLoop function.
type streamReader interface {
	Idle() <-chan time.Time
	ResetIdle() bool
	Stop(context.Context)
	getStream() Stream
	reply([]byte) (int, error)
	startDisconnect(context.Context, string, bool)
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
	if stream.ID().Protocol() == types.ProtoUDP {
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
	sr := GetSyntheticIPResolver(ctx)
	go func() {
		defer close(h.done)

		id := h.stream.ID()
		tag := h.stream.Tag()

		switch h.connected {
		case notConnected:
			// Set up the idle timer to close and release this handler when it's been idle for a while.
			h.connected = connecting

			dto := h.stream.DialTimeout()
			dst := id.Destination()
			dstAddr := dst.Addr()
			if dstAddr.Is6() {
				addr, err := sr.Resolve(dstAddr)
				if err != nil {
					clog.Errorf(ctx, "!> %s %s, failed to establish connection: %v", tag, id, err)
					h.connected = notConnected
					return
				}
				if addr != dstAddr {
					clog.Debugf(ctx, "-> %s synthetic destination resolved to %s", id, addr)
					id = NewConnID(id.Protocol(), id.Source(), netip.AddrPortFrom(addr, dst.Port()))
					if dto > localDialTimeout {
						dto = localDialTimeout
					}
				}
			}
			clog.Debugf(ctx, "   %s %s, dialing", tag, id)
			d := GetDialer(ctx)
			dtoCtx, cancel := context.WithTimeout(ctx, dto)
			defer cancel()
			var conn net.Conn

			// A retry is needed here because the attempt to establish a Tunnel might arrive before
			// the target IP is ready to receive requests. The target IP might well be intercepted
			// (or in progress of switching to become intercepted).
			err := backoff.Retry(func() error {
				var err error
				if id.Protocol() == types.ProtoUDP {
					conn, err = d.DialUDP(dtoCtx, netip.AddrPort{}, id.Destination())
				} else {
					conn, err = d.DialTCP(dtoCtx, id.Destination())
				}
				return err
			}, backoff.WithContext(backoff.NewConstantBackOff(time.Second), dtoCtx))
			if err != nil {
				clog.Errorf(ctx, "!> %s %s, failed to establish connection: %v", tag, id, err)
				if err = h.stream.Send(ctx, NewMessage(DialReject, nil)); err != nil {
					clog.Errorf(ctx, "!> %s %s, failed to send DialReject: %v", tag, id, err)
				}
				if err = h.stream.CloseSend(ctx); err != nil {
					clog.Errorf(ctx, "!> %s %s, stream.CloseSend failed: %v", tag, id, err)
				}
				h.connected = notConnected
				return
			}
			if err = h.stream.Send(ctx, NewMessage(DialOK, nil)); err != nil {
				_ = conn.Close()
				clog.Errorf(ctx, "!> %s %s, failed to send DialOK: %v", tag, id, err)
				return
			}
			clog.Debugf(ctx, "<- %s %s, dial answered", tag, id)
			h.conn = conn

		case connecting:
		default:
			clog.Errorf(ctx, "!! %s %s, start called in invalid state", tag, id)
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
	h.startDisconnect(ctx, "explicit close", true)
	h.startDisconnect(ctx, "explicit close", false)
	h.cancel()
}

func (h *dialer) startDisconnect(ctx context.Context, reason string, isReader bool) {
	if wrConn, ok := h.conn.(halfCloser); ok {
		if isReader {
			h.startReaderDisconnect(ctx, reason, wrConn)
		} else {
			h.startWriterDisconnect(ctx, reason, wrConn)
		}
	} else if atomic.CompareAndSwapInt32(&h.connected, connected, notConnected) {
		clog.Tracef(ctx, "<> %s %s closing connection: %s", h.stream.Tag(), h.stream.ID(), reason)
		if err := h.conn.Close(); err != nil {
			clog.Tracef(ctx, "!! %s %s, Close failed: %v", h.stream.Tag(), h.stream.ID(), err)
		}
	}
}

func (h *dialer) startReaderDisconnect(ctx context.Context, reason string, conn halfCloser) {
	if atomic.CompareAndSwapInt32(&h.connected, connected, readClosed) || atomic.CompareAndSwapInt32(&h.connected, writeClosed, notConnected) {
		clog.Tracef(ctx, "<- %s %s closing connection write: %s", h.stream.Tag(), h.stream.ID(), reason)
		if err := conn.CloseWrite(); err != nil && !strings.HasSuffix(err.Error(), "not connected") {
			clog.Debugf(ctx, "<! %s %s, CloseWrite failed: %v", h.stream.Tag(), h.stream.ID(), err)
		}
		return
	}
}

func (h *dialer) startWriterDisconnect(ctx context.Context, reason string, conn halfCloser) {
	if atomic.CompareAndSwapInt32(&h.connected, connected, writeClosed) || atomic.CompareAndSwapInt32(&h.connected, readClosed, notConnected) {
		clog.Tracef(ctx, "-> %s %s closing connection read: %s", h.stream.Tag(), h.stream.ID(), reason)
		err := conn.CloseRead()
		switch {
		case err == nil, err == io.EOF, strings.Contains(err.Error(), "not connected"):
		default:
			clog.Debugf(ctx, "!< %s %s, CloseRead failed: %v", h.stream.Tag(), h.stream.ID(), err)
		}
	}
}

func (h *dialer) connToStreamLoop(ctx context.Context, wg *sync.WaitGroup) {
	var endReason string
	endLevel := clog.LevelTrace
	id := h.stream.ID()
	tag := h.stream.Tag()

	// Outgoing must not be buffered. It's essential that messages that are read from the connection
	// are sent on the stream a.s.a.p. and that a delay when doing that causes back-pressure on the
	// connection.
	outgoing := make(chan Message)
	defer func() {
		if !h.ResetIdle() {
			// Hard close of peer. We don't want any more data
			select {
			case outgoing <- NewMessage(Disconnect, nil):
			default:
			}
		}
		close(outgoing)
		clog.Logf(ctx, endLevel, "<- %s %s conn-to-stream loop ended because %s", tag, id, endReason)
		wg.Done()
	}()

	wg.Add(1)
	WriteLoop(ctx, h.stream, outgoing, wg, h.egressBytesProbe)

	buf := make([]byte, 0x80000)
	clog.Tracef(ctx, "-> %s %s conn-to-stream loop started", tag, id)
	for {
		_ = h.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := h.conn.Read(buf)
		if n > 0 {
			clog.Tracef(ctx, "-> %s %s, read len %d from conn", tag, id, n)
			select {
			case <-ctx.Done():
				endReason = ctx.Err().Error()
				return
			case outgoing <- NewMessage(Normal, buf[:n]):
			}
		}

		if err != nil {
			var netErr *net.OpError
			switch {
			case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
				if ctx.Err() != nil {
					endReason = ctx.Err().Error()
					return
				}
				continue
			case errors.Is(err, io.EOF):
				endReason = "EOF was encountered"
			case errors.Is(err, net.ErrClosed):
				endReason = "the connection was closed"
			case strings.Contains(err.Error(), "aborted"):
				endReason = "the connection was aborted"
			default:
				endReason = fmt.Sprintf("a read error occurred: %T %v", err, err)
				endLevel = slog.LevelError
			}
			h.startDisconnect(ctx, endReason, false)
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
	readLoop(ctx, h.stream.Tag(), h, h.ingressBytesProbe)
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
		clog.Errorf(ctx, "!! CONN %s: unhandled connection control message: %s", h.getStream().ID(), cm)
	}
}

func readLoop(ctx context.Context, tag Tag, h streamReader, trafficProbe *CounterProbe) {
	var endReason string
	endLevel := clog.LevelTrace
	id := h.getStream().ID()
	defer func() {
		h.startDisconnect(ctx, endReason, true)
		clog.Logf(ctx, endLevel, "<- %s %s stream-to-conn loop ended because %s", tag, id, endReason)
	}()

	incoming, errCh := ReadLoop(ctx, h.getStream(), trafficProbe)
	clog.Tracef(ctx, "<- %s %s stream-to-conn loop started", tag, id)
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
				clog.Error(ctx, err)
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
					endLevel = slog.LevelError
					return
				}
				clog.Tracef(ctx, "<- %s %s, len %d", tag, id, wn)
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
	tag Tag,
	tunnelProvider Provider,
	dialStream agent.Agent_WatchDialClient,
	sessionID SessionID,
) error {
	// create ctx to clean up leftover dialRespond if waitloop dies
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dialResponders := make(chan struct{}, maxConcurrentDialResponders)
	for ctx.Err() == nil {
		dr, err := dialStream.Recv()
		if err == nil {
			select {
			case dialResponders <- struct{}{}:
			case <-ctx.Done():
				return nil
			}
			dr := dr
			go func() {
				defer func() {
					<-dialResponders
				}()
				dialRespond(ctx, tag, tunnelProvider, dr, sessionID)
			}()
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		switch status.Code(err) {
		case codes.Canceled, codes.NotFound, codes.Unavailable:
			return nil
		}
		return fmt.Errorf("dial request stream recv: %w", err)
	}
	return nil
}

func dialRespond(ctx context.Context, tag Tag, tunnelProvider Provider, dr *rpc.DialRequest, sessionID SessionID) {
	id := ConnID(dr.ConnId)
	ctx, cancel := context.WithCancel(ctx)
	mt, err := tunnelProvider.Tunnel(ctx)
	if err != nil {
		clog.Errorf(ctx, "!! %s %s, call to manager Tunnel failed: %v", tag, id, err)
		cancel()
		return
	}
	s, err := NewClientStream(ctx, tag, mt, id, sessionID, time.Duration(dr.RoundtripLatency), time.Duration(dr.DialTimeout))
	if err != nil {
		clog.Error(ctx, err)
		cancel()
		return
	}
	d := NewDialer(s, cancel, nil, nil)
	d.Start(ctx)
	<-d.Done()
}
