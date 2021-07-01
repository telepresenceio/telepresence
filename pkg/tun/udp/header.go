package udp

import (
	"encoding/binary"
	"fmt"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
)

const HeaderLen = 8

// The UDP datagram and its payload
type Header []byte

func (u Header) SourcePort() uint16 {
	return binary.BigEndian.Uint16(u)
}

func (u Header) SetSourcePort(port uint16) {
	binary.BigEndian.PutUint16(u, port)
}

func (u Header) DestinationPort() uint16 {
	return binary.BigEndian.Uint16(u[2:])
}

func (u Header) SetDestinationPort(port uint16) {
	binary.BigEndian.PutUint16(u[2:], port)
}

func (u Header) PayloadLen() uint16 {
	return u.TotalLen() - HeaderLen
}

func (u Header) TotalLen() uint16 {
	return binary.BigEndian.Uint16(u[4:])
}

func (u Header) SetPayloadLen(l uint16) {
	binary.BigEndian.PutUint16(u[4:], l+HeaderLen)
}

func (u Header) Checksum() uint16 {
	return binary.BigEndian.Uint16(u[6:])
}

func (u Header) SetChecksum(ipHdr ip.Header) {
	ip.L4Checksum(ipHdr, 6, ipproto.UDP)
}

func (u Header) Packet() []byte {
	return u[:u.TotalLen()]
}

func (u Header) Payload() []byte {
	return u[HeaderLen:u.TotalLen()]
}

func (u Header) String() string {
	return fmt.Sprintf("src=%d, dst=%d, length=%d, crc=%d", u.SourcePort(), u.DestinationPort(), u.PayloadLen(), u.Checksum())
}
