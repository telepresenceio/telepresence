package nat

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// FirewallRouter is an interface to what is essentially a routing table, but implemented in the
// firewall.
//
// TODO(lukeshu): Why have we implemented the routing table in the firewall?  Mostly historical
// reasons, and we should consider using the real routing table.
type FirewallRouter interface {
	// Flush will flush any pending rule changes that needs to be committed
	Flush(ctx context.Context) error

	// Clear the given route. Returns true if the route was cleared and  false if no such route was found.
	Clear(ctx context.Context, route *Route) (bool, error)

	// Add the given route. If the route already exists and is different from the given route, it is
	// cleared before the new route is added. Returns true if the route was add and false if it was already present.
	Add(ctx context.Context, route *Route) (bool, error)

	// Disable the router.
	Disable(ctx context.Context) error

	// Enable the router
	Enable(ctx context.Context) error

	// Get the original destination for a connection that has been routed.
	GetOriginalDst(conn *net.TCPConn) (host string, err error)
}

func NewRouter(name string, localIP net.IP) FirewallRouter {
	// newRouter is implemented in platform-specific files.
	return newRouter(name, localIP)
}

type Table struct {
	Name   string
	Routes []*Route
}

type Route struct {
	Destination
	ToPort int
}

func ParsePorts(portsStr string) ([]int, error) {
	portsStr = strings.TrimSpace(portsStr)
	if len(portsStr) == 0 {
		return nil, nil
	}
	portStrs := strings.Split(portsStr, ",")
	ports := make([]int, len(portStrs))
	for i, p := range portStrs {
		ps := strings.TrimSpace(p)
		var err error
		if ports[i], err = strconv.Atoi(ps); err != nil {
			return nil, fmt.Errorf("invalid port number in route %q", ps)
		}
	}
	return ports, nil
}

func NewRoute(proto string, ip net.IP, ports []int, toPort int) (*Route, error) {
	dest, err := NewDestination(proto, ip, ports)
	if err != nil {
		return nil, err
	}

	return &Route{
		Destination: dest,
		ToPort:      toPort,
	}, nil
}

func (e *Route) Equal(o *Route) bool {
	return e == o || e != nil && o != nil && e.Destination == o.Destination && e.ToPort == o.ToPort
}

func (e *Route) String() string {
	return fmt.Sprintf("%s->%d", e.Destination, e.ToPort)
}

type routingTableCommon struct {
	Name string
	// mappings is the routing table itself.  Unlike a real routing table, we have the
	// capability of doing per-port routes.  Unlike a real routing table, instead of routing to
	// an IP, we route to a localhost port number.
	mappings map[Destination]*Route
}
