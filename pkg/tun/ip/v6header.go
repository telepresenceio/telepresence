package ip

import (
	"encoding/binary"
	"net"

	"golang.org/x/net/ipv6"

	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
)

type V6Header []byte

func (h V6Header) Initialize() {
	for i := len(h) - 1; i > 0; i-- {
		h[i] = 0
	}
	h[0] = ipv6.Version << 4
}

func (h V6Header) Version() int {
	return int(h[0] >> 4)
}

func (h V6Header) TrafficClass() int {
	return int(h[0]&0x0f)<<4 | int(h[1])>>4
}

func (h V6Header) FlowLabel() int {
	return int(h[1]&0x0f)<<16 | int(h[2])<<8 | int(h[3])
}

func (h V6Header) PayloadLen() int {
	return int(binary.BigEndian.Uint16(h[4:6]))
}

func (h V6Header) NextHeader() int {
	return int(h[6])
}

func (h V6Header) HopLimit() int {
	return int(h[7])
}

func (h V6Header) SetTTL(hops int) {
	h[7] = uint8(hops)
}

func (h V6Header) Source() net.IP {
	return net.IP(h[8:24])
}

func (h V6Header) Destination() net.IP {
	return net.IP(h[24:40])
}

func (h V6Header) SetSource(ip net.IP) {
	if ip6 := ip.To16(); ip6 != nil {
		copy(h[8:24], ip)
	}
}

func (h V6Header) SetDestination(ip net.IP) {
	if ip6 := ip.To16(); ip6 != nil {
		copy(h[24:40], ip)
	}
}

func (h V6Header) HeaderLen() int {
	return ipv6.HeaderLen
}

func (h V6Header) SetPayloadLen(tl int) {
	binary.BigEndian.PutUint16(h[4:], uint16(tl))
}

func (h V6Header) SetL4Protocol(proto int) {
	h[6] = uint8(proto)
}

func (h V6Header) L4Protocol() int {
	return h.NextHeader()
}

func (h V6Header) SetChecksum() {}

func (h V6Header) Packet() []byte {
	return h[:ipv6.HeaderLen+h.PayloadLen()]
}

func (h V6Header) Payload() []byte {
	return h[ipv6.HeaderLen : ipv6.HeaderLen+h.PayloadLen()]
}

func (h V6Header) PseudoHeader(l4Proto int) []byte {
	b := make([]byte, 16*2+8)
	copy(b, h[8:40]) // src and dst
	binary.BigEndian.PutUint32(b[32:], uint32(h.PayloadLen()))
	b[39] = uint8(l4Proto)
	return b
}

func (h V6Header) ProcessFragments(data *buffer.Data, fragsMap map[uint16][]*buffer.Data) *buffer.Data {
	// TODO: Implement based on Extension headers
	return nil
}
