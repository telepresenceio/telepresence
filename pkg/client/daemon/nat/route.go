package nat

import (
	"context"
	"fmt"
	"net"
)

// FirewallRouter is an interface to what is essentially a routing table, but implemented in the
// firewall.
//
// TODO(lukeshu): Why have we implemented the routing table in the firewall?  Mostly historical
// reasons, and we should consider using the real routing table.
type FirewallRouter interface {
	ClearTCP(ctx context.Context, ip, port string) error
	ClearUDP(ctx context.Context, ip, port string) error
	Disable(ctx context.Context) error
	Enable(ctx context.Context) error
	ForwardTCP(ctx context.Context, ip, port, toPort string) error
	ForwardUDP(ctx context.Context, ip, port, toPort string) error
	GetOriginalDst(conn *net.TCPConn) (rawaddr []byte, host string, err error)
}

func NewRouter(name string) FirewallRouter {
	// newRouter is implemented in platform-specific files.
	return newRouter(name)
}

type Address struct {
	Proto string
	IP    string
	Port  string // a comma-separated list of zero or more port numbers
}

type Entry struct {
	Destination Address
	Port        string
}

func (e *Entry) String() string {
	return fmt.Sprintf("%s:%s->%s", e.Destination.Proto, e.Destination.IP, e.Port)
}

type routingTableCommon struct {
	Name string
	// Mappings is the routing table itself.  Unlike a real routing table, we have the
	// capability of doing per-port routes.  Unlike a real routing table, instead of routing to
	// an IP, we route to a localhost port number.
	Mappings map[Address]string
}
