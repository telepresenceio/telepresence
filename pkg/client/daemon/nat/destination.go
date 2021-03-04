package nat

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// A Destination is an immutable value that is used as a key in the routing table and when sorting routes.
// The choice to use a single string was motivated by a desire to have a value that:
//  - can be ordered correctly (IPs or ports represented as a strings cannot)
//  - is compact and hence very efficient when lexically compared or when used as a hash key
//  - is immutable
type Destination string

// NewDestination creates a new Destination. Valid protocols are "tcp" and "udp"
func NewDestination(proto string, ip net.IP, ports []int) (Destination, error) {
	// Convert 16 byte IP representing 4 byte IP into 4 byte IP
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	ipLen := len(ip)
	b := make([]byte, 2+ipLen+len(ports)*2)
	switch proto {
	case "tcp":
		b[0] = 1
	case "udp":
		b[0] = 2
	default:
		return "", fmt.Errorf("unknown protocol: %q", proto)
	}
	b[1] = byte(ipLen)
	copy(b[2:], ip)

	if len(ports) > 0 {
		sort.Ints(ports)
		n := ipLen + 2
		for _, p := range ports {
			b[n] = byte((p & 0xff00) >> 8)
			n++
			b[n] = byte(p & 0xff)
			n++
		}
	}
	return Destination(b), nil
}

// IP Returns the IP-address
func (rk Destination) IP() net.IP {
	return net.IP(rk[2 : 2+rk[1]])
}

// Ports returns the ports, if any
func (rk Destination) Ports() []int {
	s := int(2 + rk[1])    // start position of ports
	n := (len(rk) - s) / 2 // number of ports
	if n > 0 {
		ports := make([]int, n)
		for i := 0; i < n; i++ {
			ports[i] = int(rk[s])<<8 + int(rk[s+1])
			s += 2
		}
		return ports
	}
	return nil
}

// Proto returns the protocol
func (rk Destination) Proto() string {
	if rk[0] == 2 {
		return "udp"
	}
	return "tcp"
}

// String returns a string <proto>:<ip> and optionally a comma separated list
// of ports delimited by []
func (rk Destination) String() string {
	w := strings.Builder{}
	w.WriteString(rk.Proto())
	w.WriteByte(':')
	w.WriteString(rk.IP().String())
	ports := rk.Ports()
	if len(ports) > 0 {
		w.WriteByte('[')
		for i, p := range ports {
			if i > 0 {
				w.WriteByte(',')
			}
			w.WriteString(strconv.Itoa(p))
		}
		w.WriteByte(']')
	}
	return w.String()
}
