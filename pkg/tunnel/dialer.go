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

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
)

// The idleDuration controls how long a dialer for a specific proto+from-to address combination remains alive without
// reading or writing any messages. The dialer is normally closed by one of the peers.
const tcpConnTTL = 2 * time.Hour // Default tcp_keepalive_time on Linux
const udpConnTTL = 1 * time.Minute
const partlyClosedDuration = 5 * time.Second

const (
	notConnected = int32(iota)
	connecting
	connected
	disconnecting
)

// The dialer takes care of dispatching messages between gRPC and UDP connections
type dialer struct {
	stream    Stream
	conn      net.Conn
	idleTimer *time.Timer
	idleLock  sync.Mutex
	ttl       int64
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
		stream:    stream,
		conn:      conn,
		connected: state,
		ttl:       int64(ttl),
		done:      make(chan struct{}),
	}
}

func (h *dialer) Start(ctx context.Context) {
	go func() {
		defer close(h.done)

		id := h.stream.ID()
		switch h.connected {
		case notConnected:
			// Set up the idle timer to close and release this handler when it's been idle for a while.
			h.connected = connecting

			dlog.Debugf(ctx, "   CONN %s, dialing", id)
			d := net.Dialer{Timeout: h.stream.DialTimeout()}
			conn, err := d.DialContext(ctx, id.ProtocolString(), id.DestinationAddr().String())
			if err != nil {
				dlog.Errorf(ctx, "!! CONN %s, failed to establish connection: %v", id, err)
				if err = h.stream.Send(ctx, NewMessage(DialReject, nil)); err != nil {
					dlog.Errorf(ctx, "!! CONN %s, failed to send DialReject: %v", id, err)
				}
				h.connected = notConnected
				return
			}
			if err = h.stream.Send(ctx, NewMessage(DialOK, nil)); err != nil {
				_ = conn.Close()
				dlog.Errorf(ctx, "!! CONN %s, failed to send DialOK: %v", id, err)
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
		h.idleTimer = time.NewTimer(h.getTTL())
		h.connected = connected

		wg := sync.WaitGroup{}
		wg.Add(2)
		go h.connToStreamLoop(ctx, &wg)
		go h.streamToConnLoop(ctx, &wg)
		wg.Wait()
		h.Close(ctx)
	}()
}

func (h *dialer) Done() <-chan struct{} {
	return h.done
}

func (h *dialer) getTTL() time.Duration {
	return time.Duration(atomic.LoadInt64(&h.ttl))
}

func (h *dialer) handleControl(ctx context.Context, cm Message) {
	switch cm.Code() {
	case Disconnect: // Peer responded to our disconnect or wants to hard-close. No more messages will arrive
		h.Close(ctx)
	case KeepAlive:
		h.resetIdle()
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

// Close will close the underlying TCP/UDP connection
func (h *dialer) Close(ctx context.Context) {
	if atomic.CompareAndSwapInt32(&h.connected, connected, notConnected) {
		dlog.Debugf(ctx, "   CONN %s explicitly closing connection", h.stream.ID())
		_ = h.conn.Close()
	}
}

func (h *dialer) startDisconnect(ctx context.Context) {
	if atomic.CompareAndSwapInt32(&h.connected, connected, disconnecting) {
		id := h.stream.ID()
		dlog.Debugf(ctx, "   CONN %s disconnecting", id)
		atomic.StoreInt64(&h.ttl, int64(partlyClosedDuration))
		if err := h.conn.Close(); err != nil {
			dlog.Debugf(ctx, "!! CONN %s, Close failed: %v", id, err)
		}
	}
}

func (h *dialer) connToStreamLoop(ctx context.Context, wg *sync.WaitGroup) {
	endReason := ""
	endLevel := dlog.LogLevelError
	id := h.stream.ID()

	outgoing := make(chan Message, 5)
	defer func() {
		if !h.resetIdle() {
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

	WriteLoop(ctx, h.stream, outgoing)

	buf := make([]byte, 0x100000)
	dlog.Debugf(ctx, "   CONN %s conn-to-stream loop started", id)
	for atomic.LoadInt32(&h.connected) == connected {
		n, err := h.conn.Read(buf)
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				endReason = "EOF was encountered"
				endLevel = dlog.LogLevelDebug
			case errors.Is(err, net.ErrClosed):
				endReason = "the connection was closed"
				endLevel = dlog.LogLevelDebug
			default:
				endReason = fmt.Sprintf("a read error occurred: %v", err)
			}
			h.startDisconnect(ctx)
			return
		}

		dlog.Tracef(ctx, "<- CONN %s, len %d", id, n)
		switch {
		case !h.resetIdle():
			endReason = "it was idle for too long"
			return
		case n > 0:
			select {
			case <-ctx.Done():
				endReason = ctx.Err().Error()
				return
			case outgoing <- NewMessage(Normal, buf[:n]):
			}
		}
	}
}

func (h *dialer) streamToConnLoop(ctx context.Context, wg *sync.WaitGroup) {
	endReason := ""
	endLevel := dlog.LogLevelError
	id := h.stream.ID()
	defer func() {
		wg.Done()
		h.startDisconnect(ctx)
		dlog.Logf(ctx, endLevel, "   CONN %s stream-to-conn loop ended because %s", id, endReason)
	}()

	incoming, errCh := ReadLoop(ctx, h.stream)

	dlog.Debugf(ctx, "   CONN %s stream-to-conn loop started", id)
	for atomic.LoadInt32(&h.connected) != notConnected {
		select {
		case <-ctx.Done():
			endReason = ctx.Err().Error()
			return
		case <-h.idleTimer.C:
			endReason = "it was idle for too long"
			return
		case err := <-errCh:
			dlog.Error(ctx, err)
		case dg := <-incoming:
			if dg == nil {
				// h.incoming was closed by the reader and is now drained.
				endReason = "there was no more input"
				endLevel = dlog.LogLevelDebug
				return
			}
			if !h.resetIdle() {
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
					h.startDisconnect(ctx)
					endReason = fmt.Sprintf("a write error occurred: %v", err)
					return
				}
				dlog.Tracef(ctx, "-> CONN %s, len %d", id, wn)
				n += wn
			}
		}
	}
}

func (h *dialer) resetIdle() bool {
	h.idleLock.Lock()
	stopped := h.idleTimer.Stop()
	if stopped {
		h.idleTimer.Reset(h.getTTL())
	}
	h.idleLock.Unlock()
	return stopped
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
	id := ConnID(dr.ConnId)
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
