package vif

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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
	// Host routes for local DNS servers that fall inside a routed subnet. These must
	// follow their real path (e.g. a second NIC or VPN) instead of the default route,
	// so they are routed via the existing best path. Every other never-proxy subnet
	// keeps the historical default-route behavior. See issue #2429.
	localDNSRoutes []netip.Prefix
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

// SetLocalDNSRoutes records the never-proxy host routes that target a local DNS
// server inside a routed subnet. Only these are routed via their existing best
// path; all other never-proxy subnets continue to use the default route.
func (rt *Router) SetLocalDNSRoutes(routes []netip.Prefix) {
	rt.Lock()
	rt.localDNSRoutes = routes
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

	// A route inside a Telepresence-owned range — the virtual subnet or the IPv6 ULA —
	// is the device's own source address or, under proxy-via, the virtual subnet itself.
	// Such a route can linger on a not-yet-torn-down tel device across a reconnect, so it
	// is skipped, but only when it sits on one of our own devices (identified by a shared
	// interface-name prefix such as tel0/tel1). A route in those ranges on a foreign
	// interface is a genuine conflict and is still reported.
	vipSubnet := client.GetConfig(ctx).Routing().VirtualSubnet

	// Slightly awkward nested loops, since they can both continue (i.e., there are probably wasted iterations), but it's
	// okay, there's not going to be hundreds of routes.
	// In any case, we really wanna run over the table as the outer loop, since it's bigger.
	for _, tr := range table {
		clog.Tracef(ctx, "checking for overlap with route %q", tr)
		ownedRange := (vipSubnet.IsValid() && vipSubnet.Overlaps(tr.RoutedNet)) || TelepresenceULA6.Overlaps(tr.RoutedNet)
		if (tr.RoutedNet.Bits() == 0 || tr.Default) || // Default route, overlapped if needed
			subnet.IsHalfOfDefault(tr.RoutedNet) || // OpenVPN covers half the address space with a /1 route and the other half with another. This is its way of doing a default route.
			tr.InterfaceName == rt.device.Name() || // This is the interface we're routing through, so we can overlap it
			(ownedRange && sameDevicePrefix(tr.InterfaceName, rt.device.Name())) { // stale owned/virtual route on one of our own devices
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

// sameDevicePrefix reports whether two interface names share the same non-numeric
// prefix (for example "tel0" and "tel1", or "utun4" and "utun5"). It identifies
// different incarnations of the same kind of device, used to recognize a route left
// on an earlier Telepresence device rather than on a foreign interface.
func sameDevicePrefix(a, b string) bool {
	return devicePrefix(a) == devicePrefix(b)
}

func devicePrefix(name string) string {
	return strings.TrimRightFunc(name, func(r rune) bool { return r >= '0' && r <= '9' })
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

	const linux = runtime.GOOS == "linux"
	for _, sn := range removed {
		if linux {
			r := rt.subnetRoute(ctx, sn)
			if err := rt.routingTable.Remove(ctx, &r); err != nil {
				clog.Errorf(ctx, "failed to remove route for subnet %s: %v", sn, err)
			}
		} else if err := rt.device.RemoveSubnet(ctx, sn); err != nil {
			clog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}

	ourIdx := int(rt.device.Index())
	ourName := rt.device.Name()

	var staticRoutes []routing.Route
	for _, sn := range added {
		if linux {
			// Plain router: every cluster subnet is a route through our device in the
			// policy table, sourced from the device's owned address. The table's rule
			// priority gives it precedence over conflicting routes, so single IPs and
			// override subnets need no special-casing here.
			r := rt.subnetRoute(ctx, sn)
			if err := rt.routingTable.Add(ctx, &r); err != nil {
				clog.Errorf(ctx, "failed to add route for subnet %s: %v", sn, err)
			}
			continue
		}
		if err := rt.device.AddSubnet(ctx, sn); err != nil {
			clog.Errorf(ctx, "failed to add subnet %s: %v", sn, err)
		}
	}
	dr, err := routing.DefaultRoute(ctx)
	if err != nil {
		return err
	}

	// All subnets in neverProxy have been verified as being routed by the TUN-device.
	// Local DNS server host routes must follow their real path — the resolver may be
	// reachable through a more specific route on another interface (a second NIC, a
	// VPN, or the integration test's veth pair) — so they are routed via the existing
	// best path. Every other never-proxy subnet keeps the historical behavior of being
	// routed via the default route, to avoid changing how established never-proxy
	// entries are routed.
	for _, sn := range dontProxy {
		if slices.Contains(rt.localDNSRoutes, sn) {
			staticRoutes = append(staticRoutes, rt.routeViaExisting(ctx, sn, dr, ourIdx, ourName))
		} else {
			staticRoutes = append(staticRoutes, routeViaDefault(sn, dr))
		}
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

// subnetRoute builds the route that sends a cluster subnet through our device, sourced
// from the device's owned address of the matching family. It is used on platforms where
// the routing table carries cluster subnets directly (Linux's policy table) rather than
// the device claiming them as interface addresses.
func (rt *Router) subnetRoute(ctx context.Context, sn netip.Prefix) routing.Route {
	r := routing.NewRoute(sn, int(rt.device.Index()), rt.device.Name())
	if owned, ok := ownedAddress(ctx, sn.Addr().Is4(), rt.device.Index()); ok {
		r.LocalIP = owned
	}
	return r
}

// routeViaExisting builds a never-proxy route for sn that follows the
// destination's real path. It first asks the OS which route would be used. If
// that route points at our own device, then Telepresence has already made the
// destination look routed and we fall back to scanning the routing table for the
// best non-Telepresence match. The second table scan is intentional: GetRoute
// answers "what wins now", while existingRoute finds the route we are trying to
// preserve beneath our own route.
func (rt *Router) routeViaExisting(ctx context.Context, sn netip.Prefix, dr *routing.Route, ourIdx int, ourName string) routing.Route {
	if existing := rt.osRoute(ctx, sn, ourIdx, ourName); existing != nil {
		return routeVia(sn, existing)
	}
	if existing := rt.existingRoute(ctx, sn.Addr(), ourName); existing != nil {
		return routeVia(sn, existing)
	}
	return routeViaDefault(sn, dr)
}

func (rt *Router) osRoute(ctx context.Context, sn netip.Prefix, ourIdx int, ourName string) *routing.Route {
	existing, err := routing.GetRoute(ctx, sn)
	if err != nil {
		clog.Debugf(ctx, "unable to discover current route for never-proxy %s: %v", sn, err)
		return nil
	}
	if existing.InterfaceIndex == ourIdx || existing.InterfaceName == ourName {
		return nil
	}
	return existing
}

// existingRoute returns the most specific non-default route to addr that isn't on
// our own device, or nil if no such route exists (in which case the default route
// should be used).
func (rt *Router) existingRoute(ctx context.Context, addr netip.Addr, ourName string) *routing.Route {
	table, err := routing.GetRoutingTable(ctx)
	if err != nil {
		clog.Errorf(ctx, "failed to read routing table while resolving never-proxy route for %s: %v", addr, err)
		return nil
	}
	return mostSpecificRoute(table, addr, ourName)
}

// mostSpecificRoute returns the most specific route in table that contains addr,
// ignoring the default route, the OpenVPN half-of-default routes, and any route on
// the ourName interface. Routes on our own device are ignored because they are
// exactly the ones we're trying to bypass for never-proxy destinations. It returns
// nil when no such route exists, signalling that the default route should be used.
func mostSpecificRoute(table []*routing.Route, addr netip.Addr, ourName string) *routing.Route {
	var best *routing.Route
	for _, r := range table {
		if r.Default || subnet.IsHalfOfDefault(r.RoutedNet) || r.InterfaceName == ourName {
			continue
		}
		if !r.RoutedNet.Contains(addr) {
			continue
		}
		if best == nil || r.RoutedNet.Bits() > best.RoutedNet.Bits() {
			best = r
		}
	}
	return best
}

func routeViaDefault(sn netip.Prefix, dr *routing.Route) routing.Route {
	return routeVia(sn, dr)
}

func routeVia(sn netip.Prefix, base *routing.Route) routing.Route {
	r := routing.NewRoute(sn, base.InterfaceIndex, base.InterfaceName)
	if sameAddrFamily(sn.Addr(), base.Gateway) {
		r.Gateway = base.Gateway
	}
	if sameAddrFamily(sn.Addr(), base.LocalIP) {
		r.LocalIP = base.LocalIP
	}
	return r
}

func sameAddrFamily(addr, candidate netip.Addr) bool {
	return candidate.IsValid() && addr.Is4() == candidate.Is4()
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
	const linux = runtime.GOOS == "linux"
	for _, sn := range rt.routedSubnets {
		if linux {
			r := rt.subnetRoute(ctx, sn)
			if err := rt.routingTable.Remove(ctx, &r); err != nil {
				clog.Errorf(ctx, "failed to remove route for subnet %s: %v", sn, err)
			}
		} else if err := rt.device.RemoveSubnet(ctx, sn); err != nil {
			clog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}
	rt.dropStaticOverrides(ctx)
	rt.RUnlock()
}

func (rt *Router) Table() routing.Table {
	return rt.routingTable
}
