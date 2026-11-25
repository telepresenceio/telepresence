package rootd

import (
	"net/netip"

	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

// neverProxyCovering returns the narrowest never-proxy subnet that covers ip,
// unless an also-proxy subnet that lies inside that never-proxy subnet
// contains ip.
func neverProxyCovering(ip netip.Addr, neverProxy, alsoProxy []netip.Prefix) (netip.Prefix, bool) {
	var best netip.Prefix
	for _, nps := range neverProxy {
		if nps.Contains(ip) && (!best.IsValid() || nps.Bits() > best.Bits()) {
			best = nps
		}
	}
	if !best.IsValid() {
		return best, false
	}
	for _, aps := range alsoProxy {
		if aps.Contains(ip) && subnet.Covers(best, aps) {
			return best, false
		}
	}
	return best, true
}
