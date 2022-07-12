package udp

import (
	"context"
	"net"
	"sync"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
)

type dnsInterceptor struct {
	tunnel.TimedHandler
	toTun   ip.Writer
	fromTun chan Datagram
	dnsConn *net.UDPConn
}

// NewDnsInterceptor returns a handler that exchanges messages directly with the given dnsConn
// instead of passing them on to the traffic-manager
func NewDnsInterceptor(toTun ip.Writer, id tunnel.ConnID, remove func(), dnsAddr *net.UDPAddr) (DatagramHandler, error) {
	h := &dnsInterceptor{
		TimedHandler: tunnel.NewTimedHandler(id, idleDuration, remove),
		toTun:        toTun,
		fromTun:      make(chan Datagram, ioChannelSize),
	}
	var err error
	if h.dnsConn, err = net.DialUDP("udp", nil, dnsAddr); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *dnsInterceptor) Stop(ctx context.Context) {
	if h.dnsConn != nil {
		_ = h.dnsConn.Close()
	}
	h.TimedHandler.Stop(ctx)
}

func (h *dnsInterceptor) HandleDatagram(ctx context.Context, dg Datagram) {
	select {
	case <-ctx.Done():
	case h.fromTun <- dg:
	}
}

func (h *dnsInterceptor) Start(ctx context.Context) {
	h.TimedHandler.Start(ctx)
	go func() {
		defer h.Stop(ctx)
		wg := sync.WaitGroup{}
		wg.Add(2)
		go h.connToTun(ctx, &wg)
		go h.tunToConn(ctx, &wg)
		wg.Wait()
	}()
}

func (h *dnsInterceptor) connToTun(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	b := make([]byte, 0x400)
	dnsConn := h.dnsConn
	for ctx.Err() == nil {
		n, err := dnsConn.Read(b)
		if err != nil {
			return
		}
		if n > 0 {
			dlog.Tracef(ctx, "<- DNS %s, len %d", h.ID.ReplyString(), n)
			sendUDPToTun(ctx, h.ID, b[:n], h.toTun)
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
		case <-h.Idle():
			return
		case dg := <-h.fromTun:
			payload := dg.Header().Payload()
			pn := len(payload)
			dlog.Tracef(ctx, "-> DNS %s, len %d", h.ID, pn)
			for n := 0; n < pn; {
				wn, err := h.dnsConn.Write(payload[n:])
				if err != nil && ctx.Err() == nil {
					dlog.Errorf(ctx, "!! DNS %s, failed to write TCP: %v", h.ID, err)
				}
				n += wn
			}
			if !h.ResetIdle() {
				return
			}
		}
	}
}
