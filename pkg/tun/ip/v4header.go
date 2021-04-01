package ip

import (
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"sync/atomic"

	"golang.org/x/net/ipv4"

	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
)

var ipID uint32

func NextID() int {
	return int(atomic.AddUint32(&ipID, 1) & 0x0000ffff)
}

type V4Header []byte

type V4Option []byte

func (o V4Option) Copied() bool {
	return o[0]&0x80 == 0x80
}

func (o V4Option) Class() int {
	return int(o[0]&0x60) >> 5
}

func (o V4Option) Number() int {
	return int(o[0] & 0x1f)
}

func (o V4Option) Len() int {
	if o.Number() > 1 {
		return int(o[1])
	}
	return 1
}

func (o V4Option) Data() []byte {
	if o.Number() > 1 {
		return o[2:o.Len()]
	}
	return nil
}

func (h V4Header) Initialize() {
	for i := len(h) - 1; i > 0; i-- {
		h[i] = 0
	}
	h[0] = ipv4.Version<<4 | ipv4.HeaderLen/4
}

func (h V4Header) Version() int {
	return int(h[0] >> 4)
}

func (h V4Header) HeaderLen() int {
	return int(h[0]&0x0f) * 4
}

func (h V4Header) SetHeaderLen(hl int) {
	h[0] = (h[0] & 0xf0) | uint8(hl/4)
}

func (h V4Header) DSCP() int {
	return int(h[1] >> 2)
}

func (h V4Header) ECN() int {
	return int(h[1] & 0x3)
}

func (h V4Header) PayloadLen() int {
	return int(binary.BigEndian.Uint16(h[2:]) - uint16(h.HeaderLen()))
}

func (h V4Header) SetPayloadLen(len int) {
	binary.BigEndian.PutUint16(h[2:], uint16(len+h.HeaderLen()))
}

func (h V4Header) ID() uint16 {
	return binary.BigEndian.Uint16(h[4:])
}

func (h V4Header) SetID(id int) {
	binary.BigEndian.PutUint16(h[4:], uint16(id))
}

func (h V4Header) Flags() ipv4.HeaderFlags {
	return ipv4.HeaderFlags(h[6]) >> 5
}

func (h V4Header) SetFlags(flags ipv4.HeaderFlags) {
	h[6] = (h[6] & 0x1f) | uint8(flags<<5)
}

func (h V4Header) FragmentOffset() int {
	return int(binary.BigEndian.Uint16(h[6:]) & 0x1fff)
}

func (h V4Header) SetFragmentOffset(fragOff int) {
	flagBits := uint16(h[6]&0b11100000) << 8
	binary.BigEndian.PutUint16(h[6:], flagBits|uint16(fragOff&0x1fff))
}

func (h V4Header) TTL() int {
	return int(h[8])
}

func (h V4Header) SetTTL(hops int) {
	h[8] = uint8(hops)
}

func (h V4Header) L4Protocol() int {
	return int(h[9])
}

func (h V4Header) SetL4Protocol(proto int) {
	h[9] = uint8(proto)
}

func (h V4Header) Checksum() int {
	return int(binary.BigEndian.Uint16(h[10:]))
}

func (h V4Header) Source() net.IP {
	return net.IP(h[12:16])
}

func (h V4Header) Destination() net.IP {
	return net.IP(h[16:20])
}

func (h V4Header) SetSource(ip net.IP) {
	if ip4 := ip.To4(); ip4 != nil {
		copy(h[12:], ip)
	}
}

func (h V4Header) SetDestination(ip net.IP) {
	if ip4 := ip.To4(); ip4 != nil {
		copy(h[16:20], ip)
	}
}

func (h V4Header) Options() ([]V4Option, error) {
	optBytes := h[20:h.HeaderLen()]
	var opts []V4Option
	obl := len(optBytes)
	for i := 0; i < obl; {
		optionType := optBytes[i]
		switch optionType {
		case 0: // end of list
			i = obl
		case 1: // byte padding
			opts = append(opts, V4Option(optBytes[i:i+1]))
			i++
		default:
			if i+1 < obl {
				optLen := int(optBytes[i+1])
				if i+optLen < obl {
					opts = append(opts, V4Option(optBytes[i:i+optLen]))
					i += optLen
					continue
				}
			}
			return nil, errors.New("option data is outside IPv4 header")
		}
	}
	return opts, nil
}

func (h V4Header) Packet() []byte {
	return h[:h.HeaderLen()+h.PayloadLen()]
}

func (h V4Header) Payload() []byte {
	return h[h.HeaderLen() : h.HeaderLen()+h.PayloadLen()]
}

func (h V4Header) SetChecksum() {
	h[10] = 0 // clear current checksum
	h[11] = 0

	s := 0
	t := h.HeaderLen()
	for i := 0; i < t; i += 2 {
		s += int(h[i])<<8 | int(h[i+1])
	}
	for s > 0xffff {
		s = (s >> 16) + (s & 0xffff)
	}
	c := ^uint16(s)
	if c == 0 {
		// From RFC 768: If the computed checksum is zero, it is transmitted as all ones.
		c = 0xffff
	}
	binary.BigEndian.PutUint16(h[10:], c)
}

func (h V4Header) PseudoHeader(l4Proto int) []byte {
	b := make([]byte, 4*2+4)
	copy(b, h[12:20]) // source and destination
	b[9] = uint8(l4Proto)
	binary.BigEndian.PutUint16(b[10:], uint16(h.PayloadLen()))
	return b
}

func (h V4Header) ConcatFragments(data *buffer.Data, fragsMap map[uint16][]*buffer.Data) *buffer.Data {
	if h.Flags()&ipv4.MoreFragments == 0 && h.FragmentOffset() == 0 {
		return data
	}

	fragments, ok := fragsMap[h.ID()]
	if !ok {
		// first fragment
		fragsMap[h.ID()] = []*buffer.Data{data}
		return nil
	}

	last := V4Header(fragments[len(fragments)-1].Buf())
	fragments = append(fragments, data)
	if h.FragmentOffset() < last.FragmentOffset() {
		// Fragments didn't arrive in order. Sort them
		sort.Slice(fragments, func(i, j int) bool {
			return V4Header(fragments[i].Buf()).FragmentOffset() < V4Header(fragments[j].Buf()).FragmentOffset()
		})
	} else {
		last = h
	}

	if last.Flags()&ipv4.MoreFragments != 0 {
		// last fragment hasn't arrived yet.
		return nil
	}

	// Ensure that there are no holes in the fragment chain
	lastPayload := 0
	expectedOffset := 0
	for _, data := range fragments {
		eh := V4Header(data.Buf())
		if eh.FragmentOffset()*8 != expectedOffset {
			// There's a gap. Await more fragments
			return nil
		}
		lastPayload = eh.PayloadLen()
		expectedOffset += lastPayload
	}
	totalPayload := expectedOffset + lastPayload
	firstHeader := V4Header(fragments[0].Buf())

	final := buffer.DataPool.Get(firstHeader.HeaderLen() + totalPayload)
	fb := final.Buf()
	copy(fb[:firstHeader.HeaderLen()], firstHeader)
	offset := firstHeader.HeaderLen()
	for _, data := range fragments {
		eh := V4Header(data.Buf())
		copy(fb[offset+eh.FragmentOffset()*8:], eh.Payload())
		buffer.DataPool.Put(data)
	}
	delete(fragsMap, h.ID())

	firstHeader = fb
	firstHeader.SetFlags(firstHeader.Flags() &^ ipv4.MoreFragments)
	firstHeader.SetPayloadLen(firstHeader.HeaderLen() + totalPayload)
	firstHeader.SetChecksum()
	return final
}
