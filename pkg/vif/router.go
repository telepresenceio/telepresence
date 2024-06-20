package vif

import (
	"context"
	"fmt"
	"net"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type Router struct {
	// The vif device that packets will be routed through
	device Device
	// The routing table that will be used to route packets
	routingTable routing.Table
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

func (rt *Router) UpdateRoutes(ctx context.Context, pleaseProxy, dontProxy, dontProxyOverrides []*net.IPNet) error {
	// Don't never-proxy subnets that aren't routed
	if err := rt.ValidateRoutes(ctx, pleaseProxy); err != nil {
		return err
	}

	// Remove all current static routes so that they don't affect the routes for subnets
	// that we're about to add.
	rt.dropStaticOverrides(ctx)

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

	var staticNets []*net.IPNet
	var pr *routing.Route
	for _, sn := range added {
		var err error
		ones, bits := sn.Mask.Size()
		if bits == 32 && ones > 30 {
			staticNets = append(staticNets, sn)
			continue
		}

		if err = rt.device.AddSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to add subnet %s: %v", sn, err)
			continue
		}

		if pr == nil {
			if pr, err = routing.GetRoute(ctx, sn); err == nil {
				dlog.Errorf(ctx, "failed to retrieve route for subnet %s: %v", sn, err)
			}
		}
	}
	if len(staticNets) > 0 && pr == nil {
		return fmt.Errorf("unable to route subnets %v, because there's no subnet with a mask smaller than 31 bits", staticNets)
	}
	return rt.addStaticOverrides(ctx, dontProxy, dontProxyOverrides, staticNets, pr)
}

func (rt *Router) addStaticOverrides(ctx context.Context, neverProxy, neverProxyOverrides, staticNets []*net.IPNet, primaryRoute *routing.Route) (err error) {
	desired := make([]*routing.Route, 0, len(neverProxy)+len(neverProxyOverrides))
	dr, err := routing.DefaultRoute(ctx)
	if err != nil {
		return err
	}
	for _, sn := range neverProxy {
		// All subnets in neverProxy have been verified as being routed by the TUN-device, so we
		// route them to the default route instead.
		desired = append(desired, &routing.Route{
			LocalIP:   dr.LocalIP,
			RoutedNet: sn,
			Interface: dr.Interface,
			Gateway:   dr.Gateway,
			Default:   false,
		})
	}

	for _, sn := range neverProxyOverrides {
		r, err := routing.GetRoute(ctx, sn)
		if err != nil {
			dlog.Error(ctx, err)
		} else {
			desired = append(desired, &routing.Route{
				LocalIP:   r.LocalIP,
				RoutedNet: sn,
				Interface: r.Interface,
				Gateway:   r.Gateway,
				Default:   r.Default,
			})
		}
	}

	for _, sn := range staticNets {
		desired = append(desired, &routing.Route{
			LocalIP:   primaryRoute.LocalIP,
			RoutedNet: sn,
			Interface: primaryRoute.Interface,
			Gateway:   primaryRoute.Gateway,
			Default:   false,
		})
	}

	for _, r := range desired {
		dlog.Debugf(ctx, "Adding static route %s", r)
		if err = rt.routingTable.Add(ctx, r); err != nil {
			dlog.Errorf(ctx, "failed to add static route %s: %v", r, err)
		}
	}
	rt.staticOverrides = desired
	return nil
}

func (rt *Router) dropStaticOverrides(ctx context.Context) {
	// Remove all current static routes so that they don't affect the routes for subnets
	// that we're about to add.
	for _, c := range rt.staticOverrides {
		if err := rt.routingTable.Remove(ctx, c); err != nil {
			dlog.Errorf(ctx, "failed to remove static route %s: %v", c, err)
		}
	}
	rt.staticOverrides = nil
}

func (rt *Router) Close(ctx context.Context) {
	for _, sn := range rt.routedSubnets {
		if err := rt.device.RemoveSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}
	rt.dropStaticOverrides(ctx)
}
