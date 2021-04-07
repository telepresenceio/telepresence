package icmp

import (
	"encoding/binary"
	"fmt"
	"net"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

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
		ipHdr.SetL4Protocol(unix.IPPROTO_ICMP)
	} else {
		ipHdr.SetL4Protocol(unix.IPPROTO_ICMPV6)
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

func DestinationUnreachablePacket(mtu uint16, origHdr ip.Header, code UnreachableCode) Packet {
	pkt := NewPacket(HeaderLen+origHdr.HeaderLen()+8, origHdr.Source(), origHdr.Destination())
	iph := pkt.IPHeader()
	var msgType int
	if iph.Version() == ipv4.Version {
		msgType = int(ipv4.ICMPTypeDestinationUnreachable)
	} else {
		msgType = int(ipv6.ICMPTypeDestinationUnreachable)
	}
	icmpHdr := Header(iph.Payload())
	icmpHdr.SetMessageType(msgType)
	icmpHdr.SetCode(int(code))
	roh := icmpHdr.RestOfHeader()
	binary.BigEndian.PutUint16(roh[2:], mtu)
	copy(icmpHdr.Payload(), origHdr.Packet()[:origHdr.HeaderLen()+8])
	icmpHdr.SetChecksum(iph)
	return pkt
}
