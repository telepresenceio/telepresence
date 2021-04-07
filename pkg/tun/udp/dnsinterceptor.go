package udp

import (
	"context"
	"net"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

type dnsInterceptor struct {
	handler
	dnsConn *net.UDPConn
}

// NewDnsInterceptor returns a handler that exchanges messages directly with the given dnsConn
// instead of passing them on to the traffic-manager
//
// TODO: Get rid of most of this. We can use the kube-system/kube-dns service directly for everything except the tel2_search domain
func NewDnsInterceptor(stream *connpool.Stream, toTun chan<- ip.Packet, id connpool.ConnID, remove func(), dnsAddr *net.UDPAddr) (DatagramHandler, error) {
	h := &dnsInterceptor{
		handler: handler{
			Stream:  stream,
			id:      id,
			toTun:   toTun,
			remove:  remove,
			fromTun: make(chan Datagram, ioChannelSize),
		},
	}
	var err error
	if h.dnsConn, err = net.DialUDP("udp", nil, dnsAddr); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *dnsInterceptor) Close(ctx context.Context) {
	if h.dnsConn != nil {
		_ = h.dnsConn.Close()
	}
	h.handler.Close(ctx)
}

func (h *dnsInterceptor) Start(ctx context.Context) {
	h.idleTimer = time.NewTimer(idleDuration)
	go h.readLoop(ctx)
	go h.writeLoop(ctx)
}

func (h *dnsInterceptor) readLoop(ctx context.Context) {
	b := make([]byte, 0x400)
	for ctx.Err() == nil {
		n, err := h.dnsConn.Read(b)
		if err != nil {
			return
		}
		if !h.resetIdle() {
			return
		}
		if n > 0 {
			dlog.Debugf(ctx, "<- DNS %s, len %d", h.id.ReplyString(), n)
			h.HandleMessage(ctx, &manager.ConnMessage{ConnId: []byte(h.id), Payload: b[:n]})
		}
	}
}

func (h *dnsInterceptor) writeLoop(ctx context.Context) {
	defer h.Close(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.idleTimer.C:
			return
		case dg := <-h.fromTun:
			if !h.resetIdle() {
				return
			}
			payload := dg.Header().Payload()
			pn := len(payload)
			dlog.Debugf(ctx, "-> DNS %s, len %d", h.id, pn)
			for n := 0; n < pn; {
				wn, err := h.dnsConn.Write(payload[n:])
				if err != nil && ctx.Err() == nil {
					dlog.Errorf(ctx, "%s failed to write TCP: %v", h.id, err)
				}
				n += wn
			}
		}
	}
}
