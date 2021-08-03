package icmp

import (
	"fmt"
	"net"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

type Packet interface {
	ip.Packet
	Header() Header
	PayloadLen() int
}

type packet struct {
	ipHdr ip.Header
	data  *buffer.Data
}

func PacketFromData(ipHdr ip.Header, data *buffer.Data) Packet {
	return &packet{ipHdr: ipHdr, data: data}
}

func NewPacket(ipPayloadLen int, src, dst net.IP) Packet {
	pkt := &packet{}
	ip.InitPacket(pkt, ipPayloadLen, src, dst)
	ipHdr := pkt.IPHeader()
	if ipHdr.Version() == ipv4.Version {
		ipHdr.SetL4Protocol(ipproto.ICMP)
	} else {
		ipHdr.SetL4Protocol(ipproto.ICMPV6)
	}
	ipHdr.SetChecksum()
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
	p.Release()
}

func (p *packet) Release() {
	buffer.DataPool.Put(p.data)
}

func (p *packet) Header() Header {
	return p.IPHeader().Payload()
}

func (p *packet) PayloadLen() int {
	return p.IPHeader().PayloadLen() - 8
}

func (p *packet) String() string {
	ipHdr := p.IPHeader()
	icmpHdr := p.Header()
	var icmpType icmp.Type
	if ipHdr.Version() == ipv4.Version {
		icmpType = ipv4.ICMPType(icmpHdr.MessageType())
	} else {
		icmpType = ipv6.ICMPType(icmpHdr.MessageType())
	}
	return fmt.Sprintf("icmp %s -> %s, type %s, code %d", ipHdr.Source(), ipHdr.Destination(), icmpType, icmpHdr.Code())
}

type UnreachableCode int

const (
	NetworkUnreachable = UnreachableCode(iota)
	HostUnreachable
	ProtocolUnreachable
	PortUnreachable
	MustFragment
	SourceRouteFailed
	DestinationNetworkUnknown
	DestinationHostUnknown
	SourceHostIsolated
	DestinationNetworkProhibited
	DestinationHostProhibited
	NetworkTypeOfService
	HostTypeOfService
	CommunicationProhibited
	HostPrecedenceViolation
	PrecedenceCutoffInEffect
)

const IPv6MinMTU = 1280 // From RFC 2460, section 5

func DestinationUnreachablePacket(origHdr ip.Header, code UnreachableCode) Packet {
	var msgType int
	var origSz int
	if origHdr.Version() == ipv4.Version {
		msgType = int(ipv4.ICMPTypeDestinationUnreachable)

		// include header + 64 bits of original payload
		origSz = origHdr.HeaderLen() + 8
	} else {
		msgType = int(ipv6.ICMPTypeDestinationUnreachable)

		// include as much of invoking packet as possible without the ICMPv6 packet
		// exceeding the minimum IPv6 MTU
		origSz = origHdr.HeaderLen() + origHdr.PayloadLen()
		if HeaderLen+origSz > IPv6MinMTU {
			origSz = IPv6MinMTU - HeaderLen
		}
	}
	pkt := NewPacket(HeaderLen+origSz, origHdr.Source(), origHdr.Destination())
	iph := pkt.IPHeader()
	icmpHdr := Header(iph.Payload())
	icmpHdr.SetMessageType(msgType)
	icmpHdr.SetCode(int(code))
	copy(icmpHdr.Payload(), origHdr.Packet()[:origSz])
	icmpHdr.SetChecksum(iph)
	return pkt
}
