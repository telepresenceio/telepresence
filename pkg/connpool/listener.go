package connpool

import (
	"context"
	"fmt"
	"net"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

// listener uses the same identification, create and close semantics as a connection dialer
type listener struct {
	dialer
	listener net.Listener
}

func NewListener(id ConnID, bidiStream TunnelStream, release func()) Handler {
	return &listener{dialer: dialer{id: id, release: release, bidiStream: bidiStream}}
}

func (h *listener) Start(ctx context.Context) {
	lsConf := net.ListenConfig{}
	addr := fmt.Sprintf(":%d", h.id.DestinationPort())
	var err error
	h.listener, err = lsConf.Listen(ctx, h.id.ProtocolString(), addr)
	if err != nil {
		dlog.Errorf(ctx, "failed to establish listener for %s %s: %v", h.id.ProtocolString(), addr, err)
		return
	}
	go h.acceptLoop(ctx)
	dlog.Infof(ctx, "Started %s listener at %s", h.id.ProtocolString(), h.listener.Addr())
}

func (h *listener) Close(ctx context.Context) {
	if h.listener != nil {
		_ = h.listener.Close()
	}
	h.release()
}

func (h *listener) acceptLoop(ctx context.Context) {
	defer h.Close(ctx)
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			dlog.Errorf(ctx, "listener failed to establish connection: %v", err)
			return
		}
		go h.openFromAccept(ctx, conn)
	}
}

func (h *listener) openFromAccept(ctx context.Context, conn net.Conn) {
	var (
		ip    net.IP
		port  uint16
		err   error
		found bool
	)
	defer func() {
		if err != nil {
			dlog.Error(ctx, err)
			conn.Close()
		}
	}()
	dlog.Infof(ctx, "Accept got connection from %s", conn.RemoteAddr())

	if ip, port, err = iputil.SplitToIPPort(conn.RemoteAddr()); err != nil {
		return
	}

	id := NewConnID(h.id.Protocol(), ip, h.id.Source(), port, h.id.SourcePort())
	_, found, err = GetPool(ctx).Get(ctx, id, func(ctx context.Context, release func()) (Handler, error) {
		return HandlerFromConn(id, h.bidiStream, release, conn), nil
	})
	if err != nil {
		return
	}
	if found {
		// This should really never happen. It indicates that there are two connections originating from the same port.
		err = fmt.Errorf("%s: multiple connections for", id)
	}
}
