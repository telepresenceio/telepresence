package routing

import (
	"context"
	"errors"
	"fmt"
	"net"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

type Route struct {
	LocalIP   net.IP
	RoutedNet *net.IPNet
	Interface *net.Interface
	Gateway   net.IP
	Default   bool
}

type Table interface {
	// Add adds a route to the routing table
	Add(ctx context.Context, r *Route) error
	// Remove removes a route from the routing table
	Remove(ctx context.Context, r *Route) error
	// Close closes the routing table
	Close(ctx context.Context) error
}

func OpenTable(ctx context.Context) (Table, error) {
	return openTable(ctx)
}

func DefaultRoute(ctx context.Context) (*Route, error) {
	rt, err := GetRoutingTable(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rt {
		if r.Default {
			return r, nil
		}
	}
	return nil, errors.New("unable to find a default route")
}

func (r *Route) Routes(ip net.IP) bool {
	return r.RoutedNet.Contains(ip)
}

func (r *Route) String() string {
	isDefault := " (default)"
	if !r.Default {
		isDefault = ""
	}
	return fmt.Sprintf("%s via %s dev %s, gw %s%s", r.RoutedNet, r.LocalIP, r.Interface.Name, r.Gateway, isDefault)
}

// AddStatic adds a specific route. This can be used to prevent certain IP addresses
// from being routed to the route's interface.
func (r *Route) AddStatic(ctx context.Context) (err error) {
	dlog.Debugf(ctx, "Adding static route %s", r)
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "AddStatic", trace.WithAttributes(attribute.Stringer("tel2.route", r)))
	defer tracing.EndAndRecord(span, err)
	return r.addStatic(ctx)
}

// RemoveStatic removes a specific route added via AddStatic.
func (r *Route) RemoveStatic(ctx context.Context) (err error) {
	dlog.Debugf(ctx, "Dropping static route %s", r)
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "RemoveStaticRoute", trace.WithAttributes(attribute.Stringer("tel2.route", r)))
	defer tracing.EndAndRecord(span, err)
	return r.removeStatic(ctx)
}

func interfaceLocalIP(iface *net.Interface, ipv4 bool) (net.IP, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return net.IP{}, fmt.Errorf("unable to get interface addresses for interface %s: %w", iface.Name, err)
	}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil {
			return net.IP{}, fmt.Errorf("unable to parse address %s: %v", addr.String(), err)
		}
		if ip4 := ip.To4(); ip4 != nil {
			if !ipv4 {
				continue
			}
			return ip4, nil
		} else if ipv4 {
			continue
		}
		return ip, nil
	}
	return nil, nil
}

func compareRoutes(ctx context.Context, osRoute, tableRoute *Route) (bool, error) {
	dlog.Tracef(ctx, "Comparing OS route %s to table route %s", osRoute, tableRoute)
	if osRoute.Interface.Index == tableRoute.Interface.Index {
		return true, nil
	}
	return osCompareRoutes(ctx, osRoute, tableRoute)
}

func GetRoute(ctx context.Context, routedNet *net.IPNet) (*Route, error) {
	// This is a two-step process. First the OS will be queried for the route directly.
	// Whatever it gives back is not necessarily gonna look exactly like the route from the routing table.
	// i.e. the OS will not tell us, for example, if this is the default route.
	// So once we have the route from the OS, we'll query the routing table to get the full route.
	// If this seems a little contrived, it's because it is. But there are NO system calls that directly query an OS for
	// a route's corresponding entry in the table, and we shouldn't re-implement the OS' routing logic itself by
	// trying to route the packet analytically from the table.
	osRoute, err := getRoute(ctx, routedNet)
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
