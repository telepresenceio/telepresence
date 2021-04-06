package udp

import (
	"context"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

type DatagramHandler interface {
	connpool.Handler
	NewDatagram(ctx context.Context, dg Datagram)
}

type handler struct {
	*connpool.Stream
	id        connpool.ConnID
	remove    func()
	toTun     chan<- ip.Packet
	fromTun   chan Datagram
	idleTimer *time.Timer
	idleLock  sync.Mutex
}

const ioChannelSize = 0x40
const idleDuration = time.Second

func (h *handler) NewDatagram(ctx context.Context, dg Datagram) {
	select {
	case <-ctx.Done():
	case h.fromTun <- dg:
	}
}

func (h *handler) Close(_ context.Context) {
	h.remove()
}

func NewHandler(stream *connpool.Stream, toTun chan<- ip.Packet, id connpool.ConnID, remove func()) DatagramHandler {
	return &handler{
		Stream:  stream,
		id:      id,
		toTun:   toTun,
		remove:  remove,
		fromTun: make(chan Datagram, ioChannelSize),
	}
}

func (h *handler) HandleControl(_ context.Context, _ *connpool.ControlMessage) {
}

func (h *handler) HandleMessage(ctx context.Context, mdg *manager.ConnMessage) {
	pkt := NewDatagram(HeaderLen+len(mdg.Payload), h.id.Destination(), h.id.Source())
	ipHdr := pkt.IPHeader()
	ipHdr.SetChecksum()

	udpHdr := Header(ipHdr.Payload())
	udpHdr.SetSourcePort(h.id.DestinationPort())
	udpHdr.SetDestinationPort(h.id.SourcePort())
	udpHdr.SetPayloadLen(uint16(len(mdg.Payload)))
	copy(udpHdr.Payload(), mdg.Payload)
	udpHdr.SetChecksum(ipHdr)

	select {
	case <-ctx.Done():
		return
	case h.toTun <- pkt:
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
				return
			}
			dlog.Debugf(ctx, "<- TUN %s", dg)
			dlog.Debugf(ctx, "-> MGR %s", dg)
			udpHdr := dg.Header()
			err := h.SendMsg(&manager.ConnMessage{ConnId: []byte(h.id), Payload: udpHdr.Payload()})
			dg.SoftRelease()
			if err != nil {
				if ctx.Err() == nil {
					dlog.Error(ctx, err)
				}
				return
			}
		}
	}
}

func (h *handler) resetIdle() bool {
	h.idleLock.Lock()
	stopped := h.idleTimer.Stop()
	if stopped {
		h.idleTimer.Reset(idleDuration)
	}
	h.idleLock.Unlock()
	return stopped
}
