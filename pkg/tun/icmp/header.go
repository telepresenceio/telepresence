package icmp

import (
	"encoding/binary"

	"golang.org/x/net/ipv4"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

const HeaderLen = 8 // Same for IPv4 and IPv6

// Header represents an ICMP Header. The Header is obtained by simply casting the IP headers payload.
type Header []byte

func (h Header) MessageType() int {
	return int(h[0])
}

func (h Header) SetMessageType(t int) {
	h[0] = uint8(t)
}

func (h Header) Code() int {
	return int(h[1])
}

func (h Header) SetCode(c int) {
	h[1] = uint8(c)
}

func (h Header) Checksum() uint16 {
	return binary.BigEndian.Uint16(h[2:])
}

func (h Header) RestOfHeader() []byte {
	return h[4:8]
}

func (h Header) Payload() []byte {
	return h[8:]
}

func (h Header) SetChecksum(ipHdr ip.Header) {
	var proto int
	if ipHdr.Version() == ipv4.Version {
		proto = ipproto.ICMP
	} else {
		proto = ipproto.ICMPV6
	}
	ip.L4Checksum(ipHdr, 2, proto)
}
