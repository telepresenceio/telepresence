package iputil

import (
	"fmt"
	"net"
	"net/netip"
)

// ParseAddr is like netip.ParseAddr but removes any IPv4-mapped IPv6 address prefix.
func ParseAddr(ipStr string) (ip netip.Addr, err error) {
	if ip, err = netip.ParseAddr(ipStr); err == nil {
		ip = ip.Unmap()
	}
	return ip, err
}

// SplitToIPPort splits the given address into an IP and a port number. It's
// an error if the address is based on a hostname rather than an IP.
func SplitToIPPort(netAddr net.Addr) (netip.AddrPort, error) {
	ipAddr, ok := netAddr.(interface{ AddrPort() netip.AddrPort })
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("address %q is not an IP:port address", netAddr)
	}
	return ipAddr.AddrPort(), nil
}
