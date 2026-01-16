//go:build !linux

package teleroute

import (
	"net/netip"

	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

// StartServer returns nil because the teleroute server is only relevant in a container,
// and hence, only relevant in linux.
func StartServer(g log.Group, tap *vif.TunnelingDevice, routesCh <-chan []netip.Prefix, teleroutePort uint16) (Server, error) {
	for range routesCh {
	}
	return nil, nil
}
