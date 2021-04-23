package connpool

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// The idleDuration controls how long a dialer for a specific proto+from-to address combination remains alive without
// reading or writing any messages. The dialer is normally closed by one of the peers.
//
// TODO: Make this configurable
const connTTL = 5 * time.Minute
const dialTimeout = 30 * time.Second

// The dialer takes care of dispatching messages between gRPC and TCP/UDP connections
type dialer struct {
	id        ConnID
	release   func()
	server    rpc.Manager_ConnTunnelServer
	incoming  chan *rpc.ConnMessage
	conn      net.Conn
	idleTimer *time.Timer
	idleLock  sync.Mutex
	connected int32 // != 0 == connected
}

// NewDialer creates a new handler that dispatches messages in both directions between the given gRPC server
// and the destination identified by the given connID.
//
// The handler remains active until it's been idle for idleDuration, at which time it will automatically close
// and call the release function it got from the connpool.Pool to ensure that it gets properly released.
func NewDialer(connID ConnID, server rpc.Manager_ConnTunnelServer, release func()) Handler {
	return &dialer{
		id:       connID,
		server:   server,
		release:  release,
		incoming: make(chan *rpc.ConnMessage, 10),
	}
}

func (h *dialer) Start(ctx context.Context) {
	// Set up the idle timer to close and release this handler when it's been idle for a while.
	if h.id.Protocol() == unix.IPPROTO_UDP {
		h.open(ctx)
	}
	h.idleTimer = time.NewTimer(connTTL)
}

func (h *dialer) open(ctx context.Context) ControlCode {
	if !atomic.CompareAndSwapInt32(&h.connected, 0, 1) {
		// already connected
		return ConnectOK
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, h.id.ProtocolString(), fmt.Sprintf("%s:%d", h.id.Destination(), h.id.DestinationPort()))
	if err != nil {
		dlog.Errorf(ctx, "%s: failed to establish connection: %v", h.id, err)
		return ConnectReject
	}
	h.conn = conn
	go h.writeLoop(ctx)
	go h.readLoop(ctx)
	return ConnectOK
}

func (h *dialer) HandleControl(ctx context.Context, cm *ControlMessage) {
	var reply ControlCode
	switch cm.Code {
	case Connect:
		reply = h.open(ctx)
	case Disconnect:
		h.Close(ctx)
		reply = DisconnectOK
	default:
		dlog.Errorf(ctx, "%s: unhandled connection control message: %s", h.id, cm)
		return
	}
	h.sendTCD(ctx, reply)
}

// HandleMessage sends a package to the underlying TCP/UDP connection
func (h *dialer) HandleMessage(ctx context.Context, dg *rpc.ConnMessage) {
	select {
	case <-ctx.Done():
		return
	case h.incoming <- dg:
	}
}

// Close will close the underlying TCP/UDP connection
func (h *dialer) Close(ctx context.Context) {
	if atomic.CompareAndSwapInt32(&h.connected, 1, 0) {
		h.release()
		h.conn.Close()
	}
}

func (h *dialer) sendTCD(ctx context.Context, code ControlCode) {
	err := h.server.Send(ConnControl(h.id, code, nil))
	if err != nil {
		dlog.Errorf(ctx, "failed to send control message: %v", err)
	}
}

func (h *dialer) readLoop(ctx context.Context) {
	defer func() {
		if ctx.Err() != nil {
			dlog.Errorf(ctx, "-> CLI %s, %v", h.id, ctx.Err())
		}
		h.Close(ctx)
	}()
	b := make([]byte, 0x8000)
	for ctx.Err() == nil {
		if err := h.conn.SetReadDeadline(time.Now().Add(connTTL)); err != nil {
			dlog.Errorf(ctx, "%s: failed to set read deadline on connection: %v", h.id, err)
			return
		}
		n, err := h.conn.Read(b)
		if err != nil {
			if atomic.LoadInt32(&h.connected) > 0 && ctx.Err() == nil {
				if h.id.Protocol() == unix.IPPROTO_TCP {
					h.sendTCD(ctx, ReadClosed)
				}
				dlog.Errorf(ctx, "-> CLI %s, conn read: %v", h.id, err)
			}
			return
		}
		if !h.resetIdle() {
			return
		}
		if n > 0 {
			if err = h.server.Send(&rpc.ConnMessage{ConnId: []byte(h.id), Payload: b[:n]}); err != nil {
				if ctx.Err() == nil {
					dlog.Errorf(ctx, "-> CLI %s, gRPC send: %v", h.id, err)
				}
				return
			}
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
			if !h.resetIdle() {
				return
			}
			pn := len(dg.Payload)
			for n := 0; n < pn; {
				wn, err := h.conn.Write(dg.Payload[n:])
				if err != nil {
					if atomic.LoadInt32(&h.connected) > 0 && ctx.Err() == nil {
						if h.id.Protocol() == unix.IPPROTO_TCP {
							h.sendTCD(ctx, WriteClosed)
						}
						dlog.Errorf(ctx, "<- CLI %s, conn write: %v", h.id, err)
					}
					return
				}
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
