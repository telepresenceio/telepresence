package udp

import (
	"fmt"
	"net"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

type Datagram interface {
	ip.Packet
	Header() Header
}

type datagram struct {
	ipHdr ip.Header
	data  *buffer.Data
}

func DatagramFromData(ipHdr ip.Header, data *buffer.Data) Datagram {
	return &datagram{ipHdr: ipHdr, data: data}
}

func NewDatagram(ipPayloadLen int, src, dst net.IP) Datagram {
	pkt := &datagram{}
	ip.InitPacket(pkt, ipPayloadLen, src, dst)
	pkt.ipHdr.SetL4Protocol(ipproto.UDP)
	return pkt
}

func (p *datagram) IPHeader() ip.Header {
	return p.ipHdr
}

func (p *datagram) Data() *buffer.Data {
	return p.data
}

func (p *datagram) SetDataAndIPHeader(data *buffer.Data, ipHdr ip.Header) {
	p.ipHdr = ipHdr
	p.data = data
}

func (p *datagram) SoftRelease() {
	p.Release()
}

func (p *datagram) Release() {
	buffer.DataPool.Put(p.data)
}

func (p *datagram) Header() Header {
	return p.IPHeader().Payload()
}

func (p *datagram) String() string {
	ipHdr := p.IPHeader()
	udpHdr := p.Header()
	return fmt.Sprintf("udp %s:%d -> %s:%d", ipHdr.Source(), udpHdr.SourcePort(), ipHdr.Destination(), udpHdr.DestinationPort())
}
