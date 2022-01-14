package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func kubeFlagMap() map[string]string {
	kubeFlagMap := make(map[string]string)
	kubeFlags.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			kubeFlagMap[flag.Name] = flag.Value.String()
		}
	})
	return kubeFlagMap
}

type connectorState struct {
	*connector.ConnectInfo
	userD connector.ConnectorClient
	rootD daemon.DaemonClient
}

// withConnector is like cliutil.WithConnector, but also
//
//  - Ensures that the damon is running too
//
//  - Cleans up after itself if !retain (If it launches the daemon or connector, then it will shut
//    them down when it's done.  If they were already running, it will leave them running.)
//
//  - Makes the connector.Connect gRPC call to set up networking
func withConnector(cmd *cobra.Command, retain bool, f func(context.Context, *connectorState) error) error {
	return cliutil.WithNetwork(cmd.Context(), func(ctx context.Context, daemonClient daemon.DaemonClient) error {
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			didConnect, connInfo, err := connect(ctx, connectorClient, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if didConnect {
				// The daemon will shut down the connector for us.
				defer func() {
					if err != nil || !retain {
						_ = cliutil.Disconnect(dcontext.WithoutCancel(ctx), false, false)
					}
				}()
			}
			return f(ctx, &connectorState{ConnectInfo: connInfo, userD: connectorClient, rootD: daemonClient})
		})
	})
}

func connect(ctx context.Context, connectorClient connector.ConnectorClient, stdout io.Writer) (bool, *connector.ConnectInfo, error) {
	resp, err := connectorClient.Connect(ctx, &connector.ConnectRequest{
		KubeFlags:        kubeFlagMap(),
		MappedNamespaces: mappedNamespaces,
	})
	if err != nil {
		return false, nil, err
	}

	var msg string
	cat := errcat.Unknown
	switch resp.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(stdout, "Connected to context %s (%s)\n", resp.ClusterContext, resp.ClusterServer)
		return true, resp, nil
	case connector.ConnectInfo_ALREADY_CONNECTED:
		return false, resp, nil
	case connector.ConnectInfo_DISCONNECTED:
		return false, nil, cliutil.ErrNoTrafficManager
	case connector.ConnectInfo_MUST_RESTART:
		msg = "Cluster configuration changed, please quit telepresence and reconnect"
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED, connector.ConnectInfo_CLUSTER_FAILED, connector.ConnectInfo_DAEMON_FAILED:
		msg = resp.ErrorText
		if resp.ErrorCategory != 0 {
			cat = errcat.Category(resp.ErrorCategory)
		}
	}
	return false, nil, cat.Newf("connector.Connect: %s", msg)
}
