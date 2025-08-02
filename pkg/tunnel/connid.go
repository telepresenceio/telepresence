package tunnel

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// A ConnID is a compact and immutable representation of protocol, source IP, source port, destination IP and destination port which
// is suitable as a map key.
type ConnID string

func ConnIDFromUDP(src, dst netip.AddrPort) ConnID {
	return NewConnID(types.ProtoUDP, src, dst)
}

// NewConnID returns a new ConnID for the given values.
func NewConnID(proto types.Proto, src, dst netip.AddrPort) ConnID {
	srcAddr := src.Addr()
	dstAddr := dst.Addr()
	switch {
	case srcAddr.Is4():
		if dstAddr.Is4In6() {
			dstAddr = dstAddr.Unmap()
		} else if dstAddr.Is6() {
			srcAddr = netip.AddrFrom16(srcAddr.As16())
		}
	case srcAddr.Is4In6():
		if dstAddr.Is4() {
			srcAddr = srcAddr.Unmap()
		} else if dstAddr.Is4In6() {
			srcAddr = srcAddr.Unmap()
			dstAddr = dstAddr.Unmap()
		}
	default:
		if dstAddr.Is4() {
			dstAddr = netip.AddrFrom16(dstAddr.As16())
		}
	}

	ls := srcAddr.BitLen() / 8
	ld := dstAddr.BitLen() / 8
	if ls == 0 {
		panic("invalid source IP")
	}
	if ld == 0 {
		panic("invalid destination IP")
	}
	bs := make([]byte, ls+ld+5)
	copy(bs, srcAddr.AsSlice())
	binary.BigEndian.PutUint16(bs[ls:], src.Port())
	ls += 2
	copy(bs[ls:], dstAddr.AsSlice())
	ls += ld
	binary.BigEndian.PutUint16(bs[ls:], dst.Port())
	ls += 2
	bs[ls] = byte(proto)
	return ConnID(bs)
}

func NewZeroID() ConnID {
	return ConnID(make([]byte, 13))
}

// areBothIPv4 returns true if the source and destination of this ConnID are both IPv4.
func (id ConnID) areBothIPv4() bool {
	return len(id) == 13
}

// IsSourceIPv4 returns true if the source of this ConnID is IPv4.
func (id ConnID) IsSourceIPv4() bool {
	return id.areBothIPv4() || len(id) > 16 && net.IP(id[0:16]).To4() != nil
}

// IsDestinationIPv4 returns true if the destination of this ConnID is IPv4.
func (id ConnID) IsDestinationIPv4() bool {
	return id.areBothIPv4() || len(id) == 37 && net.IP(id[18:34]).To4() != nil
}

// Source returns the source address and port.
func (id ConnID) Source() netip.AddrPort {
	return netip.AddrPortFrom(id.SourceAddr(), id.SourcePort())
}

// SourceAddr returns the source IP.
func (id ConnID) SourceAddr() netip.Addr {
	b := []byte(id)
	if id.areBothIPv4() {
		return netip.AddrFrom4(*(*[4]byte)(b[0:4]))
	}
	return netip.AddrFrom16(*(*[16]byte)(b[0:16])).Unmap()
}

// SourcePort returns the source port.
func (id ConnID) SourcePort() uint16 {
	if id.areBothIPv4() {
		return binary.BigEndian.Uint16([]byte(id)[4:])
	}
	return binary.BigEndian.Uint16([]byte(id)[16:])
}

// Destination returns the destination address and port.
func (id ConnID) Destination() netip.AddrPort {
	return netip.AddrPortFrom(id.DestinationAddr(), id.DestinationPort())
}

// DestinationAddr returns the destination IP.
func (id ConnID) DestinationAddr() netip.Addr {
	b := []byte(id)
	if id.areBothIPv4() {
		return netip.AddrFrom4(*(*[4]byte)(b[6:10]))
	}
	return netip.AddrFrom16(*(*[16]byte)(b[18:34])).Unmap()
}

// DestinationPort returns the destination port.
func (id ConnID) DestinationPort() uint16 {
	if id.areBothIPv4() {
		return binary.BigEndian.Uint16([]byte(id)[10:])
	}
	return binary.BigEndian.Uint16([]byte(id)[34:])
}

// Protocol returns the protocol, e.g. ipproto.TCP.
func (id ConnID) Protocol() types.Proto {
	return types.Proto(id[len(id)-1])
}

// SourceProtocolString returns the protocol string for the source, e.g. "tcp4".
func (id ConnID) SourceProtocolString() (proto string) {
	p := id.Protocol()
	switch p {
	case types.ProtoTCP:
		if id.IsSourceIPv4() {
			proto = "tcp4"
		} else {
			proto = "tcp6"
		}
	case types.ProtoUDP:
		if id.IsSourceIPv4() {
			proto = "udp4"
		} else {
			proto = "udp6"
		}
	default:
		proto = fmt.Sprintf("unknown-%d", p)
	}
	return proto
}

// DestinationProtocolString returns the protocol string for the source, e.g. "tcp4".
func (id ConnID) DestinationProtocolString() (proto string) {
	p := id.Protocol()
	switch p {
	case types.ProtoTCP:
		if id.IsDestinationIPv4() {
			proto = "tcp4"
		} else {
			proto = "tcp6"
		}
	case types.ProtoUDP:
		if id.IsDestinationIPv4() {
			proto = "udp4"
		} else {
			proto = "udp6"
		}
	default:
		proto = fmt.Sprintf("unknown-%d", p)
	}
	return proto
}

// SourceNetwork returns either "ip4" or "ip6".
func (id ConnID) SourceNetwork() string {
	if id.IsSourceIPv4() {
		return "ip4"
	}
	return "ip6"
}

// DestinationNetwork returns either "ip4" or "ip6".
func (id ConnID) DestinationNetwork() string {
	if id.IsDestinationIPv4() {
		return "ip4"
	}
	return "ip6"
}

// Reply returns a copy of this ConnID with swapped source and destination properties.
func (id ConnID) Reply() ConnID {
	return NewConnID(id.Protocol(), id.Destination(), id.Source())
}

// ReplyString returns a formatted string suitable for logging showing the destination:destinationPort -> source:sourcePort.
func (id ConnID) ReplyString() string {
	return fmt.Sprintf("%s %s -> %s",
		id.Protocol(), id.Destination(), id.Source())
}

// String returns a formatted string suitable for logging showing the source:sourcePort -> destination:destinationPort.
func (id ConnID) String() string {
	if len(id) < 13 {
		return "bogus ConnID"
	}
	return fmt.Sprintf("%s %s -> %s",
		id.Protocol(), id.Source(), id.Destination())
}
