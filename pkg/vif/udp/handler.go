package udp

import (
	"context"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
)

type DatagramHandler interface {
	tunnel.Handler
	HandleDatagram(ctx context.Context, dg Datagram)
}

type handler struct {
	tunnel.TimedHandler
	stream  tunnel.Stream
	toTun   ip.Writer
	fromTun chan Datagram
}

const ioChannelSize = 0x40
const idleDuration = 5 * time.Second

func NewHandler(stream tunnel.Stream, toTun ip.Writer, id tunnel.ConnID, remove func()) DatagramHandler {
	return &handler{
		TimedHandler: tunnel.NewTimedHandler(id, idleDuration, remove),
		stream:       stream,
		toTun:        toTun,
		fromTun:      make(chan Datagram, ioChannelSize),
	}
}

func (h *handler) HandleDatagram(ctx context.Context, dg Datagram) {
	select {
	case <-ctx.Done():
	case h.fromTun <- dg:
	}
}

func createReply(id tunnel.ConnID, payload []byte) Datagram {
	pkt := NewDatagram(HeaderLen+len(payload), id.Destination(), id.Source())
	ipHdr := pkt.IPHeader()
	ipHdr.SetChecksum()

	udpHdr := Header(ipHdr.Payload())
	udpHdr.SetSourcePort(id.DestinationPort())
	udpHdr.SetDestinationPort(id.SourcePort())
	udpHdr.SetPayloadLen(uint16(len(payload)))
	copy(udpHdr.Payload(), payload)
	udpHdr.SetChecksum(ipHdr)
	return pkt
}

func sendUDPToTun(ctx context.Context, id tunnel.ConnID, payload []byte, toTun ip.Writer) {
	pkt := createReply(id, payload)
	if err := toTun.Write(ctx, pkt); err != nil {
		dlog.Errorf(ctx, "!! TUN %s: %v", id, err)
	}
}

func (h *handler) Start(ctx context.Context) {
	h.TimedHandler.Start(ctx)
	go h.readLoop(ctx)
	go h.writeLoop(ctx)
}

func (h *handler) readLoop(ctx context.Context) {
	defer h.Stop(ctx)
	for ctx.Err() == nil {
		m, err := h.stream.Receive(ctx)
		if err != nil {
			return
		}
		switch m.Code() {
		case tunnel.DialOK:
		case tunnel.DialReject, tunnel.Disconnect:
			return
		case tunnel.Normal:
			sendUDPToTun(ctx, h.ID, m.Payload(), h.toTun)
		}
	}
}

func (h *handler) writeLoop(ctx context.Context) {
	defer h.Stop(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.Idle():
			return
		case dg := <-h.fromTun:
			if !h.ResetIdle() {
				return
			}
			dlog.Tracef(ctx, "<- TUN %s", dg)
			dlog.Tracef(ctx, "-> MGR %s", dg)
			udpHdr := dg.Header()
			err := h.stream.Send(ctx, tunnel.NewMessage(tunnel.Normal, udpHdr.Payload()))
			if err != nil {
				if ctx.Err() == nil {
					dlog.Errorf(ctx, "failed to send ConnMessage: %v", err)
				}
				return
			}
		}
	}
}
