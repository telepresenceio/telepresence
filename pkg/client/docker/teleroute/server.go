package teleroute

import "net/netip"

type Server interface {
	// DaemonAddresses returns the daemon's IP on the connected teleroute network. It will return an
	// Invalid address if the container hasn't been connected yet.
	DaemonAddresses() []netip.Addr
}
