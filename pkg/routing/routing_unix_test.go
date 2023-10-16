//go:build !windows

package routing

import (
	"net"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func TestGetRouteConsistency(t *testing.T) {
	ctx := dlog.NewTestContext(t, true)
	addresses := map[string]struct{}{
		"192.168.1.23": {},
		"10.0.5.3":     {},
		"127.0.0.1":    {},
		"8.8.8.8":      {},
	}
	table, err := GetRoutingTable(ctx)
	assert.NoError(t, err)
	for _, route := range table {
		if ip := route.RoutedNet.IP.To4(); ip != nil {
			if ip.String() == "0.0.0.0" || ip.IsMulticast() {
				// Don't test 0.0.0.0 or any multicast addresses.
				continue
			}
			dlog.Debugf(ctx, "Adding route %s", route)
			addresses[ip.String()] = struct{}{}
			if n, _ := route.RoutedNet.Mask.Size(); n < 32 {
				ip2 := make(net.IP, len(ip))
				copy(ip2, ip)
				ip2[3]++
				addresses[ip2.String()] = struct{}{}
				dlog.Debugf(ctx, "Adding IP %s", ip2)
			}
		}
	}
	for addr := range addresses {
		t.Run(addr, func(t *testing.T) {
			testNet := &net.IPNet{
				IP:   iputil.Parse(addr),
				Mask: net.CIDRMask(32, 32),
			}
			osRoute, err := getOsRoute(ctx, testNet)
			require.NoError(t, err)
			route, err := GetRoute(ctx, testNet)
			require.NoError(t, err)
			// This is about as much as we can actually assert, because OSs tend to create
			// routes on the fly when, for example, a default route is hit. So there's no guarantee
			// that the matching "original" route in the table will be identical to the route returned on the fly.
			if runtime.GOOS == "linux" && osRoute.Interface.Flags&net.FlagLoopback != 0 && osRoute.LocalIP.Equal(osRoute.RoutedNet.IP) {
				addrs, err := route.Interface.Addrs()
				assert.NoError(t, err)
				assert.True(t, func() bool {
					for _, addr := range addrs {
						if addr.(*net.IPNet).IP.Equal(osRoute.LocalIP) {
							return true
						}
					}
					return false
				}(), "Interface addresses %v don't include route's local IP %s", addrs, osRoute.LocalIP)
			} else {
				require.Equal(t, osRoute.Interface.Index, route.Interface.Index, "Routes %s and %s differ", osRoute, route)
			}
			require.True(t, route.RoutedNet.Contains(osRoute.RoutedNet.IP) || route.Default, "Route %s doesn't route requested IP %s", route, osRoute.RoutedNet.IP)
		})
	}
}
