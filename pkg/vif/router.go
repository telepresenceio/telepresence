package vif

import (
	"context"
	"fmt"
	"net"

	"go.opentelemetry.io/otel"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

type Router struct {
	// The vif device that packets will be routed through
	device Device
	// The routing table that will be used to route packets
	routingTable routing.Table
	// Original routes for subnets configured not to be proxied
	neverProxyRoutes []*routing.Route
	// A list of never proxied routes that have already been added to routing table
	staticOverrides []*routing.Route
	// The subnets that are currently being routed
	routedSubnets []*net.IPNet
	// The subnets that are allowed to be routed even in the presence of conflicting routes
	whitelistedSubnets []*net.IPNet
}

func NewRouter(device Device, table routing.Table) *Router {
	return &Router{device: device, routingTable: table}
}

func (rt *Router) GetRoutedSubnets() []*net.IPNet {
	return rt.routedSubnets
}

func (rt *Router) UpdateWhitelist(whitelist []*net.IPNet) {
	rt.whitelistedSubnets = whitelist
}

func (rt *Router) ValidateRoutes(ctx context.Context, routes []*net.IPNet) error {
	// We need the entire table because we need to check for any overlaps, not just "is this IP already routed"
	table, err := routing.GetRoutingTable(ctx)
	if err != nil {
		return err
	}
	_, nonWhitelisted := subnet.Partition(routes, func(_ int, r *net.IPNet) bool {
		for _, w := range rt.whitelistedSubnets {
			if subnet.Covers(w, r) {
				// This is a whitelisted subnet, so we'll overlap it if needed
				return true
			}
		}
		for _, er := range table {
			// Route is already in the routing table.
			if subnet.Equal(r, er.RoutedNet) {
				return true
			}
		}
		return false
	})
	// Slightly awkward nested loops, since they can both continue (i.e. there's probably wasted iterations) but it's okay, there's not gonna be hundreds of routes.
	// In any case, we really wanna run over the table as the outer loop, since it's bigger.
	for _, tr := range table {
		dlog.Tracef(ctx, "checking for overlap with route %q", tr)
		if (subnet.IsZeroMask(tr.RoutedNet) || tr.Default) || // Default route, overlapped if needed
			subnet.IsHalfOfDefault(tr.RoutedNet) || // OpenVPN covers half the address space with a /1 route and the other half with another. This is its way of doing a default route.
			tr.Interface.Name == rt.device.Name() { // This is the interface we're routing through, so we can overlap it
			continue
		}
		for _, r := range nonWhitelisted {
			if subnet.Overlaps(tr.RoutedNet, r) {
				return errcat.Config.New(fmt.Sprintf(
					"subnet %s overlaps with existing route %q. Please see %s for more information",
					r, tr, "https://www.getambassador.io/docs/telepresence/latest/reference/vpn",
				))
			}
		}
	}
	return nil
}

func (rt *Router) UpdateRoutes(ctx context.Context, pleaseProxy, dontProxy []*net.IPNet) error {
	// Don't never-proxy subnets that aren't routed
	if err := rt.ValidateRoutes(ctx, pleaseProxy); err != nil {
		return err
	}

	npRoutes := make([]*routing.Route, 0, len(dontProxy))
	for _, n := range dontProxy {
		r, err := routing.GetRoute(ctx, n)
		if err != nil {
			dlog.Error(ctx, err)
		} else {
			// Ensure the route we append is the one the user requested, not whatever is in the routing table.
			// This is important because if, say, the route requested is 10.0.2.0/24 and the routing table has something like 10.0.0.0/8,
			// we would end up routing all of 10.0.0.0/8 without meaning to.
			r.RoutedNet = n
			r.Default = false
			npRoutes = append(npRoutes, r)
		}
	}
	rt.neverProxyRoutes = npRoutes

	// Remove all no longer desired subnets from the routedSubnets
	var removed []*net.IPNet
	rt.routedSubnets, removed = subnet.Partition(rt.routedSubnets, func(_ int, sn *net.IPNet) bool {
		for _, d := range pleaseProxy {
			if subnet.Equal(sn, d) {
				return true
			}
		}
		return false
	})

	// Remove already routed subnets from the pleaseProxy list
	added, _ := subnet.Partition(pleaseProxy, func(_ int, sn *net.IPNet) bool {
		for _, d := range rt.routedSubnets {
			if subnet.Equal(sn, d) {
				return false
			}
		}
		return true
	})

	// Add pleaseProxy subnets to the currently routed subnets
	rt.routedSubnets = append(rt.routedSubnets, added...)

	for _, sn := range removed {
		if err := rt.device.RemoveSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}

	for _, sn := range added {
		if err := rt.device.AddSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to add subnet %s: %v", sn, err)
		}
	}

	return rt.reconcileStaticOverrides(ctx)
}

func (rt *Router) reconcileStaticOverrides(ctx context.Context) (err error) {
	var desired []*routing.Route
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "reconcileStaticRoutes")
	defer tracing.EndAndRecord(span, err)

	// We're not going to add static routes unless they're actually needed
	// (i.e. unless the existing CIDRs overlap with the never-proxy subnets)
	for _, r := range rt.neverProxyRoutes {
		for _, s := range rt.routedSubnets {
			if s.Contains(r.RoutedNet.IP) || r.Routes(s.IP) {
				desired = append(desired, r)
				break
			}
		}
	}

adding:
	for _, r := range desired {
		for _, c := range rt.staticOverrides {
			if subnet.Equal(r.RoutedNet, c.RoutedNet) {
				continue adding
			}
		}
		if err := rt.routingTable.Add(ctx, r); err != nil {
			dlog.Errorf(ctx, "failed to add static route %s: %v", r, err)
		}
	}

removing:
	for _, c := range rt.staticOverrides {
		for _, r := range desired {
			if subnet.Equal(r.RoutedNet, c.RoutedNet) {
				continue removing
			}
		}
		if err := rt.routingTable.Remove(ctx, c); err != nil {
			dlog.Errorf(ctx, "failed to remove static route %s: %v", c, err)
		}
	}
	rt.staticOverrides = desired
	return nil
}

func (rt *Router) Close(ctx context.Context) {
	for _, sn := range rt.routedSubnets {
		if err := rt.device.RemoveSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}
	for _, r := range rt.staticOverrides {
		if err := rt.routingTable.Remove(ctx, r); err != nil {
			dlog.Errorf(ctx, "failed to remove static route %s: %v", r, err)
		}
	}
}
