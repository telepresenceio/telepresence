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
	if ip16 := ip.To16(); ip16 != nil {
		return "[" + ip16.String() + "]:" + ps
	}
	return ":" + ps
}

func JoinHostPort(host string, port uint16) string {
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}
