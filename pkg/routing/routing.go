package routing

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

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

type rtError string

func (r rtError) Error() string {
	return string(r)
}

const (
	errInconsistentRT      = rtError("routing table is inconsistent")
	maxInconsistentRetries = 3
	inconsistentRetryDelay = 50 * time.Millisecond
)

// GetRoutingTable will return a list of Route objects created from the current routing table.
func GetRoutingTable(ctx context.Context) ([]*Route, error) {
	// The process of creating routes is not atomic. If an intercept is deleted shortly before this function is
	// called, then an interface referenced from a route might no longer exist. When this happens, there will
	// be a short delay followed by a retry.
	for i := 0; i < maxInconsistentRetries; i++ {
		rt, err := getConsistentRoutingTable(ctx)
		if err != errInconsistentRT {
			return rt, err
		}
		time.Sleep(inconsistentRetryDelay)
	}
	return nil, errInconsistentRT
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
