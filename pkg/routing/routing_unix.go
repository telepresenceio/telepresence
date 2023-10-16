//go:build !windows

package routing

import (
	"context"
	"fmt"
	"net"

	"github.com/datawire/dlib/dlog"
)

func GetRoute(ctx context.Context, routedNet *net.IPNet) (*Route, error) {
	// This is a two-step process. First the OS will be queried for the route directly.
	// Whatever it gives back is not necessarily gonna look exactly like the route from the routing table.
	// i.e. the OS will not tell us, for example, if this is the default route.
	// So once we have the route from the OS, we'll query the routing table to get the full route.
	// If this seems a little contrived, it's because it is. But there are NO system calls that directly query an OS for
	// a route's corresponding entry in the table, and we shouldn't re-implement the OS' routing logic itself by
	// trying to route the packet analytically from the table.
	osRoute, err := getOsRoute(ctx, routedNet)
	if err != nil {
		return nil, err
	}
	rt, err := GetRoutingTable(ctx)
	if err != nil {
		return nil, err
	}
	var defaultRoute *Route
	for _, r := range rt {
		if ok, err := compareRoutes(ctx, osRoute, r); ok && err == nil {
			if r.Routes(routedNet.IP) {
				return r, nil
			} else if r.Default {
				defaultRoute = r
			}
		} else if err != nil {
			dlog.Errorf(ctx, "Unable to compare routes %s and %s: %v", r, osRoute, err)
		}
	}
	if defaultRoute != nil {
		dlog.Tracef(ctx, "Picked default route %s for network %s", defaultRoute, routedNet)
		return defaultRoute, nil
	}
	return nil, fmt.Errorf("unable to find route for %s", routedNet)
}

func compareRoutes(ctx context.Context, osRoute, tableRoute *Route) (bool, error) {
	dlog.Tracef(ctx, "Comparing OS route %s to table route %s", osRoute, tableRoute)
	if osRoute.Interface.Index == tableRoute.Interface.Index {
		return true, nil
	}
	return osCompareRoutes(ctx, osRoute, tableRoute)
}
