package ip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
)

// A Header provides a common interface for the V4Header and the V6Header
type Header interface {
	// Initialize ensures that the header has the correct version and that all other bytes are zero
	Initialize()

	// Version returns ipv4.Version or ipv6.Version
	Version() int

	// Destination returns the destination IP
	Destination() net.IP

	// Source returns the source IP
	Source() net.IP

	// 	HeaderLength() is the length of this header
	HeaderLen() int

	// PayloadLen is the length of the payload
	PayloadLen() int

	// L4Protocol is the protocol of the layer-4 header.
	L4Protocol() int

	// PacketBase returns the full packet that this header is backed by.
	Packet() []byte

	// Payload returns the payload of the package that this header is backed by.
	Payload() []byte

	// PseudoHeader returns the pseudo header used when computing the checksum for a layer-4 header.
	// All fields must be filled in before requesting this header.
	PseudoHeader(l4Proto int) []byte

	// SetTTL sets the hop limit
	SetTTL(id int)

	// SetSource sets the package source IP address
	SetSource(ip net.IP)

	// SetDestination sets the package destination IP address
	SetDestination(ip net.IP)

	// SetL4Protocol sets the layer 4 protocol (a.k.a. the next-layer protocol)
	SetL4Protocol(int)

	// SetPayloadLen sets the length of the payload
	SetPayloadLen(int)

	// SetChecksum computes the checksum for this header. No further modifications must be made once this is called.
	// This method is a no-op for ipv6.
	SetChecksum()
}

// ParseHeader returns a v4 or v6 header backed by the given argument. The type of header is determined
// by looking at the top 4 bits of the first byte which must match ipv4.Version or ipv6.Version
func ParseHeader(b []byte) (Header, error) {
	if len(b) == 0 {
		return nil, errors.New("empty header")
	}
	version := int(b[0] >> 4)
	switch version {
	case ipv4.Version:
		if len(b) < ipv4.HeaderLen {
			return nil, errors.New("ipv4 header too short")
		}
		return V4Header(b), nil

	case ipv6.Version:
		if len(b) < ipv6.HeaderLen {
			return nil, errors.New("ipv6 header too short")
		}
		return V6Header(b), nil
	default:
		return nil, fmt.Errorf("unhandled protocol version %d", version)
	}
}

// L4Checksum computes a checksum for a layer 4 TCP or UDP header using a pseudo header
// created from the given IP header and assigns that checksum to the two bytes starting
// at checksumPosition.
//
// The checksumPosition is the offset into the IP payload for the checksum for the given
// level-4 protocol which should be unix.IPPROTO_TCP or unix.IPPROTO_UDP.
//
// It is assumed that the ipHdr represents an un-fragmented package with a complete L4
// payload.
func L4Checksum(ipHdr Header, checksumPosition, l4Proto int) {
	// reset current checksum, if any
	p := ipHdr.Payload()
	p[checksumPosition] = 0
	p[checksumPosition+1] = 0

	s := 0
	pl := ipHdr.PayloadLen()
	if (pl % 2) != 0 {
		// uneven length, add last byte << 8
		pl--
		s = int(p[pl]) << 8
	}

	h := ipHdr.PseudoHeader(l4Proto)
	hl := len(h)
	for i := 0; i < hl; i += 2 {
		s += int(h[i])<<8 | int(h[i+1])
	}
	for i := 0; i < pl; i += 2 {
		s += int(p[i])<<8 | int(p[i+1])
	}
	for s > 0xffff {
		s = (s >> 16) + (s & 0xffff)
	}
	c := ^uint16(s)

	if c == 0 && l4Proto == ipproto.UDP {
		// From RFC 768: If the computed checksum is zero, it is transmitted as all ones.
		c = 0xffff
	}
	// compute and assign checksum
	binary.BigEndian.PutUint16(p[checksumPosition:], c)
}
