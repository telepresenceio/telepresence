package iputil

import "net"

// IPKey is an immutable cast of a net.IP suitable to be used as a map key. It must be created using IPKey(ip)
type IPKey string

func (k IPKey) IP() net.IP {
	return net.IP(k)
}

// String returns the human readable string form of the IP (as opposed to the binary junk displayed when using it directly).
func (k IPKey) String() string {
	return net.IP(k).String()
}
