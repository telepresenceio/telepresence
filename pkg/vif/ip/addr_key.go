package ip

import (
	"encoding/binary"
	"fmt"
	"net"
)

// AddrKey is an immutable form of an L4 address (ip and port), suitable as a map key.
type AddrKey string

func MakeAddrKey(ip net.IP, port uint16) AddrKey {
	// Ensure that we use the short 4-byte form if this is an ipv4
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	bs := make([]byte, len(ip)+2)
	binary.BigEndian.PutUint16(bs, port)
	copy(bs[2:], ip)
	return AddrKey(bs)
}

func (a AddrKey) Port() uint16 {
	return binary.BigEndian.Uint16([]byte(a))
}

func (a AddrKey) IP() net.IP {
	return []byte(a)[2:]
}

func (a AddrKey) String() string {
	if len(a) < 6 {
		return "invalid address"
	}
	ip := a.IP()
	if len(ip) > 4 {
		return fmt.Sprintf("[%s]:%d", ip, a.Port())
	}
	return fmt.Sprintf("%s:%d", ip, a.Port())
}
