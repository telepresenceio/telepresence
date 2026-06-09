//go:build !linux

package dns

import (
	"context"
	"net/netip"
)

// SystemResolvers returns the host's configured DNS server addresses. It is only
// implemented on linux. Other platforms rely on the explicitly configured
// dns.localAddresses (and the runtime-resolved server addresses) instead.
func SystemResolvers(context.Context) []netip.AddrPort {
	return nil
}
