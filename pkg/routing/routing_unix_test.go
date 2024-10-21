//go:build !windows

package routing

import (
	"net"
	"net/netip"
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
		if ip := route.RoutedNet.Addr(); ip.Is4() {
			if ip.IsUnspecified() || ip.IsMulticast() {
				// Don't test 0.0.0.0 or any multicast addresses.
				continue
			}
			dlog.Debugf(ctx, "Adding route %s", route)
			addresses[ip.String()] = struct{}{}
			if route.RoutedNet.Bits() < 32 {
				ip2 := ip.As4()
				ip2[3] += 2
				a := netip.AddrFrom4(ip2)
				addresses[a.String()] = struct{}{}
				dlog.Debugf(ctx, "Adding IP %s", a)
			}
		}
	}
	for addr := range addresses {
		t.Run(addr, func(t *testing.T) {
			testNet := netip.PrefixFrom(netip.MustParseAddr(addr), 32)
			osRoute, err := getOsRoute(ctx, testNet)
			require.NoError(t, err)
			route, err := GetRoute(ctx, testNet)
			require.NoError(t, err)
			// This is about as much as we can actually assert, because OSs tend to create
			// routes on the fly when, for example, a default route is hit. So there's no guarantee
			// that the matching "original" route in the table will be identical to the route returned on the fly.
			if runtime.GOOS == "linux" && osRoute.Interface.Flags&net.FlagLoopback != 0 && osRoute.LocalIP == osRoute.RoutedNet.Addr() {
				addrs, err := route.Interface.Addrs()
				assert.NoError(t, err)
				assert.True(t, func() bool {
					for _, addr := range addrs {
						a, ok := netip.AddrFromSlice(iputil.Normalize(addr.(*net.IPNet).IP))
						if ok && a == osRoute.LocalIP {
							return true
						}
					}
					return false
				}(), "Interface addresses %v don't include route's local IP %s", addrs, osRoute.LocalIP)
			} else {
				require.Equal(t, osRoute.Interface.Index, route.Interface.Index, "Routes %s and %s differ", osRoute, route)
			}
			require.True(t, route.RoutedNet.Contains(osRoute.RoutedNet.Addr()) || route.Default, "Route %s doesn't route requested IP %s", route, osRoute.RoutedNet.Addr())
		})
	}
}
