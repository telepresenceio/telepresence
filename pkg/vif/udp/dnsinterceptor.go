package udp

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
)

type dnsInterceptor struct {
	timedHandler
	toTun   ip.Writer
	fromTun chan Datagram
	dnsConn *net.UDPConn
}

// NewDnsInterceptor returns a handler that exchanges messages directly with the given dnsConn
// instead of passing them on to the traffic-manager
func NewDnsInterceptor(toTun ip.Writer, id tunnel.ConnID, remove func(), dnsAddr *net.UDPAddr) (DatagramHandler, error) {
	h := &dnsInterceptor{
		timedHandler: timedHandler{
			id:     id,
			remove: remove,
		},
		toTun:   toTun,
		fromTun: make(chan Datagram, ioChannelSize),
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
	h.timedHandler.Close(ctx)
}

func (h *dnsInterceptor) HandleDatagram(ctx context.Context, dg Datagram) {
	select {
	case <-ctx.Done():
	case h.fromTun <- dg:
	}
}

func (h *dnsInterceptor) Start(ctx context.Context) error {
	h.idleTimer = time.NewTimer(idleDuration)
	go func() {
		defer h.Close(ctx)
		wg := sync.WaitGroup{}
		wg.Add(2)
		go h.connToTun(ctx, &wg)
		go h.tunToConn(ctx, &wg)
		wg.Wait()
	}()
	return nil
}

func (h *dnsInterceptor) connToTun(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	b := make([]byte, 0x400)
	for ctx.Err() == nil {
		n, err := h.dnsConn.Read(b)
		if err != nil {
			return
		}
		if n > 0 {
			dlog.Tracef(ctx, "<- DNS %s, len %d", h.id.ReplyString(), n)
			sendUDPToTun(ctx, h.id, b[:n], h.toTun)
		}
	}
}

func (h *dnsInterceptor) tunToConn(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() {
		if err := h.dnsConn.Close(); err != nil {
			dlog.Errorf(ctx, "failed to close dnsConn: %v", err)
		}
		h.dnsConn = nil
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.idleTimer.C:
			return
		case dg := <-h.fromTun:
			payload := dg.Header().Payload()
			pn := len(payload)
			dlog.Tracef(ctx, "-> DNS %s, len %d", h.id, pn)
			for n := 0; n < pn; {
				wn, err := h.dnsConn.Write(payload[n:])
				if err != nil && ctx.Err() == nil {
					dlog.Errorf(ctx, "!! DNS %s, failed to write TCP: %v", h.id, err)
				}
				n += wn
			}
			dg.SoftRelease()
			if !h.resetIdle() {
				return
			}
		}
	}
}
