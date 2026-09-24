package setup

import (
	"context"
	"net/netip"

	empty "google.golang.org/protobuf/types/known/emptypb"

	daemonRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

// defaultActiveRoutes asks the root daemon which subnets it currently routes
// and over which local interface. It reports ok=false whenever there is no
// root daemon, no connected session, or any other failure talking to it;
// none of those are treated as a probe failure, only as "nothing to
// exclude".
func defaultActiveRoutes(ctx context.Context) (subnets []netip.Prefix, interfaceName string, ok bool) {
	conn, err := daemon.DialRootDaemon(ctx, false)
	if err != nil {
		return nil, "", false
	}
	defer conn.Close()
	status, err := daemonRpc.NewDaemonClient(conn).Status(ctx, &empty.Empty{})
	if err != nil {
		return nil, "", false
	}
	cfg, err := daemon.GetRootClientConfig(status)
	if err != nil {
		return nil, "", false
	}
	return cfg.Routing().Subnets, status.VifInterfaceName, true
}
