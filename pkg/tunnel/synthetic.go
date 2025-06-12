package tunnel

import (
	"net/netip"
)

type SyntheticIPResolver interface {
	Resolve(netip.Addr) (netip.Addr, error)
	ResolveName(netip.Addr) string
}
