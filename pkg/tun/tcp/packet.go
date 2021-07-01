package tcp

import (
	"bytes"
	"fmt"
	"net"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

type Packet interface {
	ip.Packet
	Header() Header
	PayloadLen() int
	Reset() Packet
}

type packet struct {
	ipHdr   ip.Header
	data    *buffer.Data
	withAck bool
}

func PacketFromData(ipHdr ip.Header, data *buffer.Data) Packet {
	return &packet{ipHdr: ipHdr, data: data}
}

func NewPacket(ipPayloadLen int, src, dst net.IP, withAck bool) Packet {
	pkt := &packet{withAck: withAck}
	ip.InitPacket(pkt, ipPayloadLen, src, dst)
	return pkt
}

func (p *packet) IPHeader() ip.Header {
	return p.ipHdr
}

func (p *packet) Data() *buffer.Data {
	return p.data
}

func (p *packet) SetDataAndIPHeader(data *buffer.Data, ipHdr ip.Header) {
	p.ipHdr = ipHdr
	p.data = data
}

func (p *packet) SoftRelease() {
	if !p.withAck {
		p.Release()
	}
}

func (p *packet) Release() {
	buffer.DataPool.Put(p.data)
}

func (p *packet) Header() Header {
	return p.IPHeader().Payload()
}

func (p *packet) PayloadLen() int {
	return p.IPHeader().PayloadLen() - p.Header().DataOffset()*4
}

func (p *packet) String() string {
	b := bytes.Buffer{}
	ipHdr := p.IPHeader()
	tcpHdr := p.Header()
	fmt.Fprintf(&b, "tcp %s:%d -> %s:%d, sq %.3d, an %.3d, wz %d, len %d, flags=",
		ipHdr.Source(), tcpHdr.SourcePort(), ipHdr.Destination(), tcpHdr.DestinationPort(), tcpHdr.Sequence(), tcpHdr.AckNumber(), tcpHdr.WindowSize(), len(tcpHdr.Payload()))
	tcpHdr.AppendFlags(&b)
	return b.String()
}

// Reset creates an ACK+RST packet for this packet.
func (p *packet) Reset() Packet {
	incIp := p.IPHeader()
	incTcp := p.Header()

	pkt := NewPacket(HeaderLen, incIp.Source(), incIp.Destination(), false)
	iph := pkt.IPHeader()
	iph.SetL4Protocol(ipproto.TCP)
	iph.SetChecksum()

	tcpHdr := Header(iph.Payload())
	tcpHdr.SetDataOffset(5)
	tcpHdr.SetSourcePort(incTcp.SourcePort())
	tcpHdr.SetDestinationPort(incTcp.DestinationPort())
	tcpHdr.SetRST(true)
	tcpHdr.SetACK(true)

	if incTcp.ACK() {
		tcpHdr.SetSequence(incTcp.AckNumber())
		tcpHdr.SetAckNumber(incTcp.Sequence() + 1)
	} else {
		tcpHdr.SetSequence(0)
		tcpHdr.SetAckNumber(incTcp.Sequence() + uint32(len(incTcp.Payload())))
	}

	tcpHdr.SetChecksum(iph)
	return pkt
}
