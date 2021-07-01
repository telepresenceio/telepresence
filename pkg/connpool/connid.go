package connpool

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
)

// A ConnID is a compact and immutable representation of protocol, source IP, source port, destination IP and destination port which
// is suitable as a map key.
type ConnID string

// NewConnID returns a new ConnID for the given values.
func NewConnID(proto int, src, dst net.IP, srcPort, dstPort uint16) ConnID {
	src4 := src.To4()
	dst4 := dst.To4()
	if src4 != nil && dst4 != nil {
		// These are not NOOPs because a IPv4 can be represented using a 16 byte net.IP. Here
		// we ensure that the 4 byte form is used.
		src = src4
		dst = dst4
	} else {
		src = src.To16()
		dst = dst.To16()
	}
	ls := len(src)
	ld := len(dst)
	bs := make([]byte, ls+ld+5)
	copy(bs, src)
	binary.BigEndian.PutUint16(bs[ls:], srcPort)
	ls += 2
	copy(bs[ls:], dst)
	ls += ld
	binary.BigEndian.PutUint16(bs[ls:], dstPort)
	ls += 2
	bs[ls] = byte(proto)
	return ConnID(bs)
}

// IsIPv4 returns true if the source and destination of this ConnID are IPv4
func (id ConnID) IsIPv4() bool {
	return len(id) == 13
}

// Source returns the source IP
func (id ConnID) Source() net.IP {
	if id.IsIPv4() {
		return net.IP(id[0:4])
	}
	return net.IP(id[0:16])
}

// SourceAddr returns the *net.TCPAddr or *net.UDPAddr that corresponds to the
// source IP and port of this instance.
func (id ConnID) SourceAddr() net.Addr {
	if id.Protocol() == ipproto.TCP {
		return &net.TCPAddr{IP: id.Source(), Port: int(id.SourcePort())}
	}
	return &net.UDPAddr{IP: id.Source(), Port: int(id.SourcePort())}
}

// SourcePort returns the source port
func (id ConnID) SourcePort() uint16 {
	if id.IsIPv4() {
		return binary.BigEndian.Uint16([]byte(id)[4:])
	}
	return binary.BigEndian.Uint16([]byte(id)[16:])
}

// Destination returns the destination IP
func (id ConnID) Destination() net.IP {
	if id.IsIPv4() {
		return net.IP(id[6:10])
	}
	return net.IP(id[18:34])
}

// DestinationAddr returns the *net.TCPAddr or *net.UDPAddr that corresponds to the
// destination IP and port of this instance.
func (id ConnID) DestinationAddr() net.Addr {
	if id.Protocol() == ipproto.TCP {
		return &net.TCPAddr{IP: id.Destination(), Port: int(id.DestinationPort())}
	}
	return &net.UDPAddr{IP: id.Destination(), Port: int(id.DestinationPort())}
}

// DestinationPort returns the destination port
func (id ConnID) DestinationPort() uint16 {
	if id.IsIPv4() {
		return binary.BigEndian.Uint16([]byte(id)[10:])
	}
	return binary.BigEndian.Uint16([]byte(id)[34:])
}

// Protocol returns the protocol, e.g. unix.IPPROTO_TCP
func (id ConnID) Protocol() int {
	return int(id[len(id)-1])
}

// ProtocolString returns the protocol string, e.g. "tcp4"
func (id ConnID) ProtocolString() (proto string) {
	p := id.Protocol()
	switch p {
	case ipproto.TCP:
		if id.IsIPv4() {
			proto = "tcp4"
		} else {
			proto = "tcp6"
		}
	case ipproto.UDP:
		if id.IsIPv4() {
			proto = "udp4"
		} else {
			proto = "udp6"
		}
	default:
		proto = fmt.Sprintf("unknown-%d", p)
	}
	return proto
}

// Network returns either "ip4" or "ip6"
func (id ConnID) Network() string {
	if id.IsIPv4() {
		return "ip4"
	}
	return "ip6"
}

// IPProto returns the IP protocol for the given network. Currently only supports
// TCP, UDP, and ICMP
func IPProto(network string) int {
	switch network {
	case "tcp", "tcp4":
		return ipproto.TCP
	case "udp", "udp4", "udp6":
		return ipproto.UDP
	case "icmp":
		return ipproto.ICMP
	case "icmpv6":
		return ipproto.ICMPV6
	default:
		return -1
	}
}

func protoString(proto int) string {
	switch proto {
	case ipproto.ICMP:
		return "icmp"
	case ipproto.TCP:
		return "tcp"
	case ipproto.UDP:
		return "udp"
	case ipproto.ICMPV6:
		return "icmpv6"
	default:
		return fmt.Sprintf("IP-protocol %d", proto)
	}
}

// Reply returns a copy of this ConnID with swapped source and destination properties
func (id ConnID) Reply() ConnID {
	return NewConnID(id.Protocol(), id.Destination(), id.Source(), id.DestinationPort(), id.SourcePort())
}

// ReplyString returns a formatted string suitable for logging showing the destination:destinationPort -> source:sourcePort
func (id ConnID) ReplyString() string {
	return fmt.Sprintf("%s %s:%d -> %s:%d", protoString(id.Protocol()), id.Destination(), id.DestinationPort(), id.Source(), id.SourcePort())
}

// String returns a formatted string suitable for logging showing the source:sourcePort -> destination:destinationPort
func (id ConnID) String() string {
	return fmt.Sprintf("%s %s:%d -> %s:%d", protoString(id.Protocol()), id.Source(), id.SourcePort(), id.Destination(), id.DestinationPort())
}
