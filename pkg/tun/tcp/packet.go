package tcp

import (
	"bytes"
	"fmt"
	"net"

	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"

	"golang.org/x/sys/unix"

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

func MakePacket(ipHdr ip.Header, data *buffer.Data) Packet {
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
	fmt.Fprintf(&b, "tcp sq %.3d, an %.3d, %s.%d -> %s.%d, flags=",
		tcpHdr.Sequence(), tcpHdr.AckNumber(), ipHdr.Source(), tcpHdr.SourcePort(), ipHdr.Destination(), tcpHdr.DestinationPort())
	tcpHdr.AppendFlags(&b)
	return b.String()
}

// Reset creates an ACK+RST packet for this packet.
func (p *packet) Reset() Packet {
	incIp := p.IPHeader()
	incTcp := p.Header()

	pkt := NewPacket(HeaderLen, incIp.Source(), incIp.Destination(), false)
	iph := pkt.IPHeader()
	iph.SetL4Protocol(unix.IPPROTO_TCP)
	iph.SetChecksum()

	tcpHdr := Header(iph.Payload())
	tcpHdr.SetDataOffset(5)
	tcpHdr.SetSourcePort(incTcp.SourcePort())
	tcpHdr.SetDestinationPort(incTcp.DestinationPort())
	tcpHdr.SetWindowSize(uint16(maxReceiveWindow))
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
