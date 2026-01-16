package vif

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
	"slices"
	"sync"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type Router struct {
	sync.RWMutex
	// The vif device that packets will be routed through
	device Device
	// The routing table that will be used to route packets
	routingTable routing.Table
	// A list of never proxied routes that have already been added to routing table
	staticOverrides []routing.Route
	// The subnets that are currently being routed
	routedSubnets []netip.Prefix
	// The subnets that are allowed to be routed even in the presence of conflicting routes
	whitelistedSubnets []netip.Prefix
}

func NewRouter(device Device, table routing.Table) *Router {
	return &Router{device: device, routingTable: table}
}

func (rt *Router) GetRoutedSubnets() []netip.Prefix {
	rt.RLock()
	rsn := slices.Clone(rt.routedSubnets)
	rt.RUnlock()
	return rsn
}

func (rt *Router) UpdateWhitelist(whitelist []netip.Prefix) {
	rt.Lock()
	rt.whitelistedSubnets = whitelist
	rt.Unlock()
}

func (rt *Router) ValidateRoutes(ctx context.Context, routes []netip.Prefix) error {
	// We need the entire table because we need to check for any overlaps, not just "is this IP already routed"
	table, err := routing.GetRoutingTable(ctx)
	if err != nil {
		return err
	}

	rt.RLock()
	nonWhitelisted := slices.DeleteFunc(slices.Clone(routes), func(r netip.Prefix) bool {
		for _, w := range rt.whitelistedSubnets {
			if subnet.Covers(w, r) {
				return true
			}
		}
		for _, er := range table {
			if r == er.RoutedNet && er.InterfaceName == rt.device.Name() {
				// Route is already in the routing table.
				return true
			}
		}
		return false
	})
	rt.RUnlock()

	// Slightly awkward nested loops, since they can both continue (i.e., there are probably wasted iterations), but it's
	// okay, there's not going to be hundreds of routes.
	// In any case, we really wanna run over the table as the outer loop, since it's bigger.
	for _, tr := range table {
		clog.Tracef(ctx, "checking for overlap with route %q", tr)
		if (tr.RoutedNet.Bits() == 0 || tr.Default) || // Default route, overlapped if needed
			subnet.IsHalfOfDefault(tr.RoutedNet) || // OpenVPN covers half the address space with a /1 route and the other half with another. This is its way of doing a default route.
			tr.InterfaceName == rt.device.Name() { // This is the interface we're routing through, so we can overlap it
			continue
		}
		for _, r := range nonWhitelisted {
			if tr.RoutedNet.Overlaps(r) {
				return errcat.Config.New(fmt.Sprintf(
					"subnet %s overlaps with existing route %q. Please see %s for more information",
					r, tr, "https://www.telepresence.io/docs/reference/vpn",
				))
			}
		}
	}
	return nil
}

func (rt *Router) Routes(addr netip.Addr) bool {
	rt.RLock()
	hasRoute := false
	for _, sn := range rt.routedSubnets {
		if sn.Contains(addr) {
			hasRoute = true
			break
		}
	}
	if hasRoute {
		for i := range rt.staticOverrides {
			rn := &rt.staticOverrides[i]
			if rn.RoutedNet.Contains(addr) && uint32(rn.InterfaceIndex) != rt.device.Index() {
				hasRoute = false
				break
			}
		}
	}
	rt.RUnlock()
	return hasRoute
}

func (rt *Router) UpdateRoutes(ctx context.Context, pleaseProxy, dontProxy, dontProxyOverrides []netip.Prefix) error {
	rt.Lock()
	defer rt.Unlock()

	// Remove all current static routes so that they don't affect the routes for subnets
	// that we're about to add.
	rt.dropStaticOverrides(ctx)

	// Remove all no longer desired subnets from the routedSubnets
	var removed []netip.Prefix
	rt.routedSubnets, removed = subnet.Partition(rt.routedSubnets, func(_ int, sn netip.Prefix) bool {
		for _, d := range pleaseProxy {
			if sn == d {
				return true
			}
		}
		return false
	})

	// Remove already routed subnets from the pleaseProxy list
	added := slices.DeleteFunc(pleaseProxy, func(sn netip.Prefix) bool {
		for _, d := range rt.routedSubnets {
			if sn == d {
				return true
			}
		}
		return false
	})

	// Add pleaseProxy subnets to the currently routed subnets
	rt.routedSubnets = append(rt.routedSubnets, added...)

	for _, sn := range removed {
		if err := rt.device.RemoveSubnet(ctx, sn); err != nil {
			clog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}

	ourIdx := int(rt.device.Index())
	ourName := rt.device.Name()

	var staticRoutes []routing.Route
	const linux = runtime.GOOS == "linux"
	for _, sn := range added {
		if linux && sn.IsSingleIP() {
			staticRoutes = append(staticRoutes, routing.NewRoute(sn, ourIdx, ourName))
			continue
		}

		// On linux, this adds a link, so it's still relevant after adding a static route.
		if err := rt.device.AddSubnet(ctx, sn); err != nil {
			clog.Errorf(ctx, "failed to add subnet %s: %v", sn, err)
			continue
		}

		if linux {
			// On linux, we use static routes for conflicting subnets, because those subnets will then belong
			// to our own routing table.
			if slices.ContainsFunc(rt.whitelistedSubnets, func(r netip.Prefix) bool { return r.Overlaps(sn) }) {
				clog.Debugf(ctx, "Using static route for %s because it is an override", sn)
				staticRoutes = append(staticRoutes, routing.NewRoute(sn, ourIdx, ourName))
			}
		}
	}
	dr, err := routing.DefaultRoute(ctx)
	if err != nil {
		return err
	}

	// All subnets in neverProxy have been verified as being routed by the TUN-device, so we
	// route them to the default device.
	for _, sn := range dontProxy {
		staticRoutes = append(staticRoutes, routing.NewRoute(sn, dr.InterfaceIndex, dr.InterfaceName))
	}

	// ... except for the never proxy overrides, which will be routed to our device.
	for _, sn := range dontProxyOverrides {
		staticRoutes = append(staticRoutes, routing.NewRoute(sn, ourIdx, ourName))
	}

	addRts, removeRts := rt.createRoutesDelta(staticRoutes)
	for i := range addRts {
		r := &addRts[i]
		if err = rt.routingTable.Add(ctx, r); err != nil {
			clog.Errorf(ctx, "failed to add static route %s: %v", r, err)
		}
	}
	for i := range removeRts {
		r := &removeRts[i]
		if err = rt.routingTable.Remove(ctx, r); err != nil {
			clog.Errorf(ctx, "failed to remove static route %s: %v", r, err)
		}
	}
	rt.staticOverrides = staticRoutes
	return nil
}

func (rt *Router) createRoutesDelta(rs []routing.Route) (added, removed []routing.Route) {
	for _, r := range rs {
		if !slices.Contains(rt.staticOverrides, r) {
			added = append(added, r)
		}
	}
	for _, r := range rt.staticOverrides {
		if !slices.Contains(rs, r) {
			removed = append(removed, r)
		}
	}
	return added, removed
}

func (rt *Router) dropStaticOverrides(ctx context.Context) {
	// Remove all current static routes so that they don't affect the routes for subnets
	// that we're about to add.
	for i := range rt.staticOverrides {
		r := &rt.staticOverrides[i]
		if err := rt.routingTable.Remove(ctx, r); err != nil {
			clog.Errorf(ctx, "failed to remove static route %s: %v", r, err)
		}
	}
	rt.staticOverrides = nil
}

func (rt *Router) Close(ctx context.Context) {
	rt.RLock()
	for _, sn := range rt.routedSubnets {
		if err := rt.device.RemoveSubnet(ctx, sn); err != nil {
			clog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}
	rt.dropStaticOverrides(ctx)
	rt.RUnlock()
}

func (rt *Router) Table() routing.Table {
	return rt.routingTable
}
