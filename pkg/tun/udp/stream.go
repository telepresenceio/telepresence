package udp

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

type Stream struct {
	bidiStream manager.Manager_UDPTunnelClient
	toTun      chan<- ip.Packet
}

func NewStream(bidiStream manager.Manager_UDPTunnelClient, toTun chan<- ip.Packet) *Stream {
	return &Stream{bidiStream: bidiStream, toTun: toTun}
}

func (us *Stream) ReadLoop(ctx context.Context) error {
	for {
		mdg, err := us.bidiStream.Recv()
		if err != nil {
			return err
		}
		pkt := newPacket(mdg)
		select {
		case <-ctx.Done():
			pkt.SoftRelease() // Packet lost
			return nil
		case us.toTun <- pkt:
		}
	}
}

func newPacket(mdg *manager.UDPDatagram) Datagram {
	pkt := NewDatagram(HeaderLen+len(mdg.Payload), mdg.SourceIp, mdg.DestinationIp)
	ipHdr := pkt.IPHeader()
	ipHdr.SetChecksum()

	udpHdr := Header(ipHdr.Payload())
	udpHdr.SetSourcePort(uint16(mdg.SourcePort))
	udpHdr.SetDestinationPort(uint16(mdg.DestinationPort))
	udpHdr.SetPayloadLen(uint16(len(mdg.Payload)))
	copy(udpHdr.Payload(), mdg.Payload)
	udpHdr.SetChecksum(ipHdr)
	return pkt
}
