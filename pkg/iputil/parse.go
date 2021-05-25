package iputil

import (
	"fmt"
	"math"
	"net"
	"strconv"
)

// Parse is like net.ParseIP but converts an IPv4 in 16 byte form to its 4 byte form
func Parse(ipStr string) (ip net.IP) {
	if ip = net.ParseIP(ipStr); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
	}
	return
}

// SplitToIPPort splits the given address into an IP and a port number. It's
// an  error if the address is based on a hostname rather than an IP.
func SplitToIPPort(netAddr net.Addr) (net.IP, uint16, error) {
	addr := netAddr.String()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, 0, fmt.Errorf("address %q is not an IP and a port", addr)
	}
	ip := Parse(host)
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || ip == nil {
		return nil, 0, fmt.Errorf("address %q does not have an integer port <= to %d", addr, math.MaxUint16)
	}
	return ip, uint16(p), nil
}
