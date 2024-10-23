package iputil

import (
	"net"
	"strings"
)

func IsIpV6Addr(ipAddStr string) bool {
	return strings.Count(ipAddStr, ":") >= 2
}

// Normalize returns the four byte version of an IPv4, even if it
// was expressed as a 16 byte IP.
func Normalize(ip net.IP) net.IP {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip
}
