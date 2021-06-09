package connpool

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// The idleDuration controls how long a dialer for a specific proto+from-to address combination remains alive without
// reading or writing any messages. The dialer is normally closed by one of the peers.
const connTTL = time.Minute
const dialTimeout = 30 * time.Second

const (
	notConnected = int32(iota)
	halfConnected
	connected
)

type TunnelStream interface {
	Send(*rpc.ConnMessage) error
	Recv() (*rpc.ConnMessage, error)
}

// The dialer takes care of dispatching messages between gRPC and UDP connections
type dialer struct {
	id         ConnID
	release    func()
	bidiStream TunnelStream
	incoming   chan Message
	conn       net.Conn
	idleTimer  *time.Timer
	idleLock   sync.Mutex
	connected  int32
}

// NewDialer creates a new handler that dispatches messages in both directions between the given gRPC server
// and the destination identified by the given connID.
//
// The handler remains active until it's been idle for idleDuration, at which time it will automatically close
// and call the release function it got from the connpool.Pool to ensure that it gets properly released.
func NewDialer(connID ConnID, bidiStream TunnelStream, release func()) Handler {
	return &dialer{
		id:         connID,
		bidiStream: bidiStream,
		release:    release,
		incoming:   make(chan Message, 10),
		connected:  notConnected,
	}
}

// HandlerFromConn is like NewHandler but initializes the handler with an already existing connection.
func HandlerFromConn(connID ConnID, bidiStream TunnelStream, release func(), conn net.Conn) Handler {
	return &dialer{
		id:         connID,
		bidiStream: bidiStream,
		release:    release,
		incoming:   make(chan Message, 10),
		connected:  halfConnected,
		conn:       conn,
	}
}

func (h *dialer) Start(ctx context.Context) {
	// Set up the idle timer to close and release this handler when it's been idle for a while.
	h.idleTimer = time.NewTimer(connTTL)

	switch h.connected {
	case notConnected:
		if h.id.Protocol() == unix.IPPROTO_UDP {
			h.open(ctx)
		}
	case halfConnected:
		// Connection is created by listener on this side. Establish other
		// side using a control message and start the loops
		h.connected = connected
		h.sendTCD(ctx, Connect)
	}
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
	go h.writeLoop(ctx)
	go h.readLoop(ctx)
	return ConnectOK
}

func (h *dialer) handleControl(ctx context.Context, cm Control) {
	dlog.Debugf(ctx, "<- GRPC %s", cm)
	switch cm.Code() {
	case Connect:
		h.sendTCD(ctx, h.open(ctx))
	case ConnectOK:
		go h.writeLoop(ctx)
		go h.readLoop(ctx)
	case Disconnect:
		h.Close(ctx)
		h.sendTCD(ctx, DisconnectOK)
	case ReadClosed, WriteClosed:
		h.Close(ctx)
	case KeepAlive:
		h.resetIdle()
	default:
		dlog.Errorf(ctx, "%s: unhandled connection control message: %s", h.id, cm)
	}
}

// HandleMessage sends a package to the underlying TCP/UDP connection
func (h *dialer) HandleMessage(ctx context.Context, dg Message) {
	if ctrl, ok := dg.(Control); ok {
		h.handleControl(ctx, ctrl)
		return
	}
	select {
	case <-ctx.Done():
		return
	case h.incoming <- dg:
	}
}

// Close will close the underlying TCP/UDP connection
func (h *dialer) Close(_ context.Context) {
	if atomic.CompareAndSwapInt32(&h.connected, connected, notConnected) {
		h.release()
		if h.conn != nil {
			_ = h.conn.Close()
		}
	}
}

func (h *dialer) sendTCD(ctx context.Context, code ControlCode) {
	ctrl := NewControl(h.id, code, nil)
	dlog.Debugf(ctx, "<- GRPC %s", ctrl)
	err := h.bidiStream.Send(ctrl.TunnelMessage())
	if err != nil {
		dlog.Errorf(ctx, "failed to send control message: %v", err)
	}
}

func (h *dialer) readLoop(ctx context.Context) {
	defer func() {
		// allow write to drain and perform the close of the connection
		dtime.SleepWithContext(ctx, 200*time.Millisecond)
		close(h.incoming)
	}()
	b := make([]byte, 0x8000)
	for ctx.Err() == nil {
		n, err := h.conn.Read(b)
		if err != nil {
			if atomic.LoadInt32(&h.connected) > 0 && ctx.Err() == nil {
				if err != io.EOF {
					dlog.Errorf(ctx, "!! CONN %s, conn read: %v", h.id, err)
				}
			}
			return
		}
		if !h.resetIdle() {
			return
		}
		if n > 0 {
			dlog.Debugf(ctx, "<- CONN %s, len %d", h.id, n)
			if err = h.bidiStream.Send(NewMessage(h.id, b[:n]).TunnelMessage()); err != nil {
				if ctx.Err() == nil {
					dlog.Errorf(ctx, "!! GRPC %s, send: %v", h.id, err)
				}
				return
			}
			dlog.Debugf(ctx, "-> GRPC %s, len %d", h.id, n)
		}
	}
}

func (h *dialer) writeLoop(ctx context.Context) {
	defer h.Close(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.idleTimer.C:
			return
		case dg := <-h.incoming:
			if dg == nil {
				// h.incoming was closed by the reader and is now drained.
				if h.id.Protocol() == unix.IPPROTO_TCP {
					h.sendTCD(ctx, ReadClosed)
				}
				return
			}
			if !h.resetIdle() {
				return
			}
			payload := dg.Payload()
			pn := len(payload)
			dlog.Debugf(ctx, "<- GRPC %s, len %d", h.id, pn)
			for n := 0; n < pn; {
				wn, err := h.conn.Write(payload[n:])
				if err != nil {
					if atomic.LoadInt32(&h.connected) > 0 && ctx.Err() == nil {
						if h.id.Protocol() == unix.IPPROTO_TCP {
							h.sendTCD(ctx, WriteClosed)
						}
						dlog.Errorf(ctx, "!! CONN %s, write: %v", h.id, err)
					}
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
		h.idleTimer.Reset(connTTL)
	}
	h.idleLock.Unlock()
	return stopped
}
