package connpool

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
)

// The idleDuration controls how long a dialer for a specific proto+from-to address combination remains alive without
// reading or writing any messages. The dialer is normally closed by one of the peers.
const tcpConnTTL = 2 * time.Hour // Default tcp_keepalive_time on Linux
const udpConnTTL = 1 * time.Minute
const partlyClosedDuration = 5 * time.Second
const dialTimeout = 30 * time.Second

// handlerBufferSize is the total number of messages that a handler can have in its buffer before it
// starts blocking on the HandleMessage function (and hence, generate backpressure on the tunnel)
const handlerBufferSize = 100

const (
	notConnected = int32(iota)
	halfConnected
	connected
	disconnecting
)

// The dialer takes care of dispatching messages between gRPC and UDP connections
type dialer struct {
	id        ConnID
	release   func()
	tunnel    Tunnel
	incoming  chan Message
	conn      net.Conn
	idleTimer *time.Timer
	idleLock  sync.Mutex
	ttl       int64
	connected int32
}

// NewDialer creates a new handler that dispatches messages in both directions between the given gRPC server
// and the destination identified by the given connID.
//
// The handler remains active until it's been idle for idleDuration, at which time it will automatically close
// and call the release function it got from the connpool.Pool to ensure that it gets properly released.
func NewDialer(connID ConnID, tunnel Tunnel, release func()) Handler {
	ttl := tcpConnTTL
	if connID.Protocol() == ipproto.UDP {
		ttl = udpConnTTL
	}
	return &dialer{
		id:        connID,
		tunnel:    tunnel,
		release:   release,
		incoming:  make(chan Message, handlerBufferSize),
		connected: notConnected,
		ttl:       int64(ttl),
	}
}

// HandlerFromConn is like NewHandler but initializes the handler with an already existing connection.
func HandlerFromConn(connID ConnID, tunnel Tunnel, release func(), conn net.Conn) Handler {
	ttl := tcpConnTTL
	if connID.Protocol() == ipproto.UDP {
		ttl = udpConnTTL
	}
	return &dialer{
		id:        connID,
		tunnel:    tunnel,
		release:   release,
		incoming:  make(chan Message, handlerBufferSize),
		connected: halfConnected,
		conn:      conn,
		ttl:       int64(ttl),
	}
}

func (h *dialer) Start(ctx context.Context) {
	// Set up the idle timer to close and release this handler when it's been idle for a while.
	h.idleTimer = time.NewTimer(h.getTTL())

	switch h.connected {
	case notConnected:
		if h.id.Protocol() == ipproto.UDP {
			h.open(ctx)
		}
	case halfConnected:
		// Connection is created by listener on this side. Establish other
		// side using a control message and start the loops
		h.connected = connected
		h.sendTCD(ctx, Connect)
	}

	// Start writeLoop so that initial control packages can be handled
	go h.writeLoop(ctx)
}

func (h *dialer) getTTL() time.Duration {
	return time.Duration(atomic.LoadInt64(&h.ttl))
}

func (h *dialer) open(ctx context.Context) ControlCode {
	if !atomic.CompareAndSwapInt32(&h.connected, notConnected, connected) {
		// already connected
		return ConnectOK
	}
	dlog.Debugf(ctx, "   CONN %s, dialing", h.id)
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, h.id.ProtocolString(), h.id.DestinationAddr().String())
	if err != nil {
		dlog.Errorf(ctx, "%s: failed to establish connection: %v", h.id, err)
		return ConnectReject
	}
	h.conn = conn
	dlog.Debugf(ctx, "   CONN %s, dial answered", h.id)
	go h.readLoop(ctx)
	return ConnectOK
}

func (h *dialer) handleControl(ctx context.Context, cm Control) {
	dlog.Debugf(ctx, "<- GRPC %s", cm)
	switch cm.Code() {
	case Connect:
		h.sendTCD(ctx, h.open(ctx))
	case ConnectOK:
		go h.readLoop(ctx)
	case Disconnect: // Peer requests a disconnect. No more messages will arrive
		h.Close(ctx)
		h.sendTCD(ctx, DisconnectOK)
	case DisconnectOK: // Peer responded to our disconnect or wants to hard-close. No more messages will arrive
		h.Close(ctx)
	case ConnectReject:
		h.Close(ctx)
	case KeepAlive:
		h.resetIdle()
	default:
		dlog.Errorf(ctx, "%s: unhandled connection control message: %s", h.id, cm)
	}
}

// HandleMessage sends a package to the underlying TCP/UDP connection
func (h *dialer) HandleMessage(ctx context.Context, dg Message) {
	select {
	case <-ctx.Done():
	case h.incoming <- dg:
	}
}

// Close will close the underlying TCP/UDP connection
func (h *dialer) Close(_ context.Context) {
	if atomic.CompareAndSwapInt32(&h.connected, connected, notConnected) ||
		atomic.CompareAndSwapInt32(&h.connected, disconnecting, notConnected) {
		h.drop()
	}
}

func (h *dialer) drop() {
	h.release()
	if h.conn != nil {
		_ = h.conn.Close()
	}
}

func (h *dialer) sendTCD(ctx context.Context, code ControlCode) {
	ctrl := NewControl(h.id, code, nil)
	dlog.Debugf(ctx, "-> GRPC %s", ctrl)
	err := h.tunnel.Send(ctx, ctrl)
	if err != nil {
		dlog.Errorf(ctx, "failed to send control message: %v", err)
	}
}

func (h *dialer) startDisconnect(ctx context.Context, sendOK bool) {
	if atomic.CompareAndSwapInt32(&h.connected, connected, disconnecting) {
		dlog.Debugf(ctx, "-- GRPC %s disconnecting", h.id)
		atomic.StoreInt64(&h.ttl, int64(partlyClosedDuration))
		if sendOK && h.id.Protocol() == ipproto.TCP {
			h.sendTCD(ctx, Disconnect)
		}
	}
}

func (h *dialer) readLoop(ctx context.Context) {
	b := make([]byte, 0x10000)

	stateCheck := func() bool {
		state := atomic.LoadInt32(&h.connected)
		if state == disconnecting || state == notConnected {
			return false
		}
		if ctx.Err() != nil {
			// Hard close of peer. We don't want any more data
			h.sendTCD(ctx, DisconnectOK)
			h.Close(ctx)
			return false
		}
		return true
	}

	for {
		n, err := h.conn.Read(b)
		if !stateCheck() {
			return
		}

		if err != nil {
			h.startDisconnect(ctx, true)
			if err != io.EOF {
				dlog.Errorf(ctx, "!! CONN %s, conn read: %v", h.id, err)
			}
			return
		}
		if !h.resetIdle() {
			// Hard close of peer. We don't want any more data
			h.sendTCD(ctx, DisconnectOK)
			h.Close(ctx)
			return
		}
		if n > 0 {
			dlog.Debugf(ctx, "<- CONN %s, len %d", h.id, n)
			err = h.tunnel.Send(ctx, NewMessage(h.id, b[:n]))
			if err != nil {
				if ctx.Err() == nil {
					dlog.Errorf(ctx, "!! GRPC %s, send: %v", h.id, err)
				}
				h.startDisconnect(ctx, false)
				return
			}
			dlog.Debugf(ctx, "-> GRPC %s, len %d", h.id, n)
		}
	}
}

func (h *dialer) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.idleTimer.C:
			return
		case dg := <-h.incoming:
			if dg == nil {
				// h.incoming was closed by the reader and is now drained.
				h.startDisconnect(ctx, true)
				return
			}
			if ctrl, ok := dg.(Control); ok {
				h.handleControl(ctx, ctrl)
				continue
			}
			if !h.resetIdle() {
				// Hard close of peer. We don't want any more data
				h.sendTCD(ctx, DisconnectOK)
				h.Close(ctx)
				return
			}
			state := atomic.LoadInt32(&h.connected)
			if state == notConnected { // state == disconnecting will still be able to write
				return
			}
			payload := dg.Payload()
			pn := len(payload)
			dlog.Debugf(ctx, "<- GRPC %s, len %d", h.id, pn)
			for n := 0; n < pn; {
				wn, err := h.conn.Write(payload[n:])
				if err != nil {
					h.startDisconnect(ctx, true)
					dlog.Errorf(ctx, "!! CONN %s, write: %v", h.id, err)
					return
				}
				dlog.Debugf(ctx, "-> CONN %s, len %d", h.id, wn)
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
