package udp

import (
	"context"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
)

type DatagramHandler interface {
	tunnel.Handler
	HandleDatagram(ctx context.Context, dg Datagram)
}

type timedHandler struct {
	id        tunnel.ConnID
	idleTimer *time.Timer
	idleLock  sync.Mutex
	remove    func()
}

func (h *timedHandler) resetIdle() bool {
	h.idleLock.Lock()
	stopped := h.idleTimer.Stop()
	if stopped {
		h.idleTimer.Reset(idleDuration)
	}
	h.idleLock.Unlock()
	return stopped
}

func (h *timedHandler) Close(_ context.Context) {
	h.remove()
}

type handler struct {
	timedHandler
	stream  tunnel.Stream
	toTun   ip.Writer
	fromTun chan Datagram
}

const ioChannelSize = 0x40
const idleDuration = 5 * time.Second

func NewHandler(stream tunnel.Stream, toTun ip.Writer, id tunnel.ConnID, remove func()) DatagramHandler {
	return &handler{
		timedHandler: timedHandler{
			id:     id,
			remove: remove,
		},
		stream:  stream,
		toTun:   toTun,
		fromTun: make(chan Datagram, ioChannelSize),
	}
}

func (h *handler) HandleDatagram(ctx context.Context, dg Datagram) {
	select {
	case <-ctx.Done():
	case h.fromTun <- dg:
	}
}
func sendUDPToTun(ctx context.Context, id tunnel.ConnID, payload []byte, toTun ip.Writer) {
	pkt := NewDatagram(HeaderLen+len(payload), id.Destination(), id.Source())
	ipHdr := pkt.IPHeader()
	ipHdr.SetChecksum()

	udpHdr := Header(ipHdr.Payload())
	udpHdr.SetSourcePort(id.DestinationPort())
	udpHdr.SetDestinationPort(id.SourcePort())
	udpHdr.SetPayloadLen(uint16(len(payload)))
	copy(udpHdr.Payload(), payload)
	udpHdr.SetChecksum(ipHdr)

	defer pkt.Release()
	if err := toTun.Write(ctx, pkt); err != nil {
		dlog.Errorf(ctx, "!! TUN %s: %v", id, err)
	}
}

func (h *handler) Start(ctx context.Context) {
	h.idleTimer = time.NewTimer(idleDuration)
	go h.writeLoop(ctx)
}

func (h *handler) writeLoop(ctx context.Context) {
	defer h.Close(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.idleTimer.C:
			return
		case dg := <-h.fromTun:
			if !h.resetIdle() {
				dg.Release()
				return
			}
			dlog.Debugf(ctx, "<- TUN %s", dg)
			dlog.Debugf(ctx, "-> MGR %s", dg)
			udpHdr := dg.Header()
			err := h.stream.Send(ctx, tunnel.NewMessage(tunnel.Normal, udpHdr.Payload()))
			dg.Release()
			if err != nil {
				if ctx.Err() == nil {
					dlog.Errorf(ctx, "failed to send ConnMessage: %v", err)
				}
				return
			}
		}
	}
}
