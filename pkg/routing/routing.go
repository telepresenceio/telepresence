package routing

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/telepresenceio/dlib/v2/dlog"
)

type Route struct {
	InterfaceIndex int
	InterfaceName  string
	LocalIP        netip.Addr
	RoutedNet      netip.Prefix
	Gateway        netip.Addr
	Default        bool
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

// NewRoute creates a new non-default route for the given prefix, interface index, and name.
func NewRoute(rn netip.Prefix, ifIdx int, ifName string) Route {
	var unSpec netip.Addr
	if rn.Addr().Is4() {
		unSpec = netip.IPv4Unspecified()
	} else {
		unSpec = netip.IPv6Unspecified()
	}
	return Route{
		RoutedNet:      rn.Masked(),
		InterfaceIndex: ifIdx,
		InterfaceName:  ifName,
		LocalIP:        unSpec,
		Gateway:        unSpec,
	}
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

func (r *Route) Routes(ip netip.Addr) bool {
	return r.RoutedNet.Contains(ip)
}

func (r *Route) String() string {
	bf := strings.Builder{}
	if !r.RoutedNet.Addr().IsUnspecified() {
		bf.WriteString(r.RoutedNet.String())
		bf.WriteByte(' ')
	}
	if !r.LocalIP.IsUnspecified() {
		bf.WriteString("via ")
		bf.WriteString(r.LocalIP.String())
		bf.WriteByte(' ')
	}
	bf.WriteString("dev ")
	bf.WriteString(r.InterfaceName)
	if !r.Gateway.IsUnspecified() {
		bf.WriteString(" gw ")
		bf.WriteString(r.Gateway.String())
	}
	if r.Default {
		bf.WriteString(" (default)")
	}
	return bf.String()
}

// AddStatic adds a specific route. This can be used to prevent certain IP addresses
// from being routed to the route's interface.
func (r *Route) AddStatic(ctx context.Context) (err error) {
	dlog.Debugf(ctx, "Adding static route %s", r)
	return r.addStatic(ctx)
}

// RemoveStatic removes a specific route added via AddStatic.
func (r *Route) RemoveStatic(ctx context.Context) (err error) {
	dlog.Debugf(ctx, "Dropping static route %s", r)
	return r.removeStatic(ctx)
}
