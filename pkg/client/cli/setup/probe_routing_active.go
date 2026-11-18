package setup

import (
	"context"
	"net/netip"
	"time"

	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

// defaultActiveRoutes asks the host user daemon, which owns the root daemon
// session, which subnets that session routes and over which local interface.
// It reports ok=false whenever there is no host user daemon, no connected
// session, or any other failure talking to it; none of those are treated as
// a probe failure, only as "nothing to exclude". A containerized user daemon
// is ignored because its routes are not on the host.
func defaultActiveRoutes(ctx context.Context) (subnets []netip.Prefix, interfaceName string, ok bool) {
	info, err := daemon.NewUserInfoLoader(ctx).LoadInfo(daemon.InfoFileName)
	if err != nil || info.InDocker() {
		return nil, "", false
	}
	conn, err := daemon.DialUserDaemon(ctx, false)
	if err != nil {
		return nil, "", false
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ci, err := connector.NewConnectorClient(conn).Status(ctx, &empty.Empty{})
	if err != nil {
		return nil, "", false
	}
	return routesFromStatus(ci)
}

// routesFromStatus extracts the routed subnets and the interface name from a user daemon status.
func routesFromStatus(ci *connector.ConnectInfo) (subnets []netip.Prefix, interfaceName string, ok bool) {
	ds := ci.GetDaemonStatus()
	cfg, err := daemon.GetRootClientConfig(ds)
	if err != nil {
		return nil, "", false
	}
	return cfg.Routing().Subnets, ds.VifInterfaceName, true
}
