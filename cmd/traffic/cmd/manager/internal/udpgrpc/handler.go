package udpgrpc

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
)

// The idleDuration controls how long a handler remains alive without reading or writing any messages
const idleDuration = time.Second

// The Handler takes care of dispatching messages between gRPC and UDP connections
type Handler struct {
	id        connpool.ConnID
	server    rpc.Manager_UDPTunnelServer
	incoming  chan *rpc.UDPDatagram
	conn      *net.UDPConn
	idleTimer *time.Timer
}

// NewHandler creates a new handler that dispatches messages in both directions between the given gRPC server
// and the destination identified by the given connID.
//
// The handler remains active until it's been idle for idleDuration, at which time it will automatically close
// and call the release function it got from the connpool.Pool to ensure that it gets properly released.
func NewHandler(ctx context.Context, connID connpool.ConnID, server rpc.Manager_UDPTunnelServer, release func()) (*Handler, error) {
	destAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", connID.Destination(), connID.DestinationPort()))
	if err != nil {
		return nil, fmt.Errorf("udp connection %s unable to resolve destination address: %v", connID, err)
	}
	conn, err := net.DialUDP("udp4", nil, destAddr)
	if err != nil {
		return nil, fmt.Errorf("udp connection %s failed: %v", connID, err)
	}
	handler := &Handler{
		id:       connID,
		server:   server,
		incoming: make(chan *rpc.UDPDatagram, 10),
		conn:     conn,
	}

	// Set up the idle timer to close and release this handler when it's been idle for a while.
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	handler.idleTimer = time.AfterFunc(idleDuration, func() {
		release()
		handler.Close(ctx)
		cancel()
	})
	go handler.readLoop(ctx)
	go handler.writeLoop(ctx)
	return handler, nil
}

// Send a package to the underlying UDP connection
func (uh *Handler) Send(ctx context.Context, dg *rpc.UDPDatagram) {
	select {
	case <-ctx.Done():
		return
	case uh.incoming <- dg:
	}
}

// Close will close the underlying UDP connection
func (uh *Handler) Close(_ context.Context) {
	_ = uh.conn.Close()
}

func (uh *Handler) readLoop(ctx context.Context) {
	for ctx.Err() == nil {
		b := make([]byte, 1460)
		n, err := uh.conn.Read(b)
		if err != nil {
			return
		}
		// dlog.Debugf(ctx, "%s read UDP package of size %d", uh.id, n)
		if !uh.idleTimer.Reset(idleDuration) {
			// Timer had already fired. Prevent that it fires again. We're done here.
			uh.idleTimer.Stop()
			return
		}
		if n > 0 {
			err = uh.server.Send(&rpc.UDPDatagram{
				SourceIp:        uh.id.Destination(),
				SourcePort:      int32(uh.id.DestinationPort()),
				DestinationIp:   uh.id.Source(),
				DestinationPort: int32(uh.id.SourcePort()),
				Payload:         b[:n],
			})
			if err != nil {
				return
			}
		}
	}
}

func (uh *Handler) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case dg := <-uh.incoming:
			if !uh.idleTimer.Reset(idleDuration) {
				// Timer had already fired. Prevent that it fires again. We're done here.
				uh.idleTimer.Stop()
				return
			}
			n := len(dg.Payload)
			if n > 0 {
				// dlog.Debugf(ctx, "%s writing UDP package of size %d", uh.id, n)
				_, err := uh.conn.Write(dg.Payload)
				if err != nil {
					dlog.Errorf(ctx, "%s failed to write UDP: %v", uh.id, err)
				}
			} else {
				dlog.Debugf(ctx, "%s skipped write of empty UDP package", uh.id)
			}
		}
	}
}
