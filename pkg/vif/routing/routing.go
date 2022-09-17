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

func Subnets(routes []*Route) []*net.IPNet {
	ns := make([]*net.IPNet, len(routes))
	for i, r := range routes {
		ns[i] = r.RoutedNet
	}
	return ns
}

func Routes(c context.Context, ms []*net.IPNet) []*Route {
	rs := make([]*Route, 0, len(ms))
	for _, n := range ms {
		r, err := GetRoute(c, n)
		if err != nil {
			dlog.Errorf(c, "unable to get route for never-proxied subnet %s. "+
				"If this is your kubernetes API server you may want to open an issue, since telepresence may "+
				"not work if it falls within the CIDR for pods/services. Error: %v",
				n, err)
			continue
		}
		dlog.Infof(c, "Adding never-proxy subnet %s", n)
		rs = append(rs, r)
	}
	return rs
}

func (r *Route) Routes(ip net.IP) bool {
	return r.RoutedNet.Contains(ip)
}

func (r *Route) String() string {
	if r.Default {
		return fmt.Sprintf("default via %s dev %s, gw %s", r.LocalIP, r.Interface.Name, r.Gateway)
	}
	return fmt.Sprintf("%s via %s dev %s, gw %s", r.RoutedNet, r.LocalIP, r.Interface.Name, r.Gateway)
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
