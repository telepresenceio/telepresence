package iputil

import (
	"net"
	"strconv"
)

func JoinIpPort(ip net.IP, port uint16) string {
	ps := strconv.Itoa(int(port))
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.String() + ":" + ps
	}
	if ip6 := ip.To4(); ip6 != nil {
		return "[" + ip6.String() + "]:" + ps
	}
	return ":" + ps
}
