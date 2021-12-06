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

// withConnector is like cliutil.WithConnector, but also
//
//  - Ensures that the damon is running too
//
//  - Cleans up after itself if !retain (If it launches the daemon or connector, then it will shut
//    them down when it's done.  If they were already running, it will leave them running.)
//
//  - Makes the connector.Connect gRPC call to set up networking
func withConnector(cmd *cobra.Command, retain bool, f func(context.Context, connector.ConnectorClient, *connector.ConnectInfo, daemon.DaemonClient) error) error {
	return cliutil.WithDaemon(cmd.Context(), dnsIP, func(ctx context.Context, daemonClient daemon.DaemonClient) (err error) {
		if cliutil.DidLaunchDaemon(ctx) {
			defer func() {
				if err != nil || !retain {
					_ = cliutil.QuitDaemon(dcontext.WithoutCancel(ctx))
				}
			}()
		}
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) (err error) {
			if cliutil.DidLaunchConnector(ctx) && !cliutil.DidLaunchDaemon(ctx) {
				// Don't shut down the connector if we're shutting down the daemon.
				// The daemon will shut down the connector for us, and if we shut it
				// down early the daemon will get upset.
				defer func() {
					if err != nil || !retain {
						_ = cliutil.QuitConnector(dcontext.WithoutCancel(ctx))
					}
				}()
			}
			connInfo, err := setConnectInfo(ctx, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return f(ctx, connectorClient, connInfo, daemonClient)
		})
	})
}

func setConnectInfo(ctx context.Context, stdout io.Writer) (*connector.ConnectInfo, error) {
	var resp *connector.ConnectInfo
	err := cliutil.WithStartedConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		resp, err = connectorClient.Connect(ctx, &connector.ConnectRequest{
			KubeFlags:        kubeFlagMap(),
			MappedNamespaces: mappedNamespaces,
		})
		if err != nil {
			return err
		}

		var msg string
		cat := errcat.Unknown
		switch resp.Error {
		case connector.ConnectInfo_UNSPECIFIED:
			fmt.Fprintf(stdout, "Connected to context %s (%s)\n", resp.ClusterContext, resp.ClusterServer)
			return nil
		case connector.ConnectInfo_ALREADY_CONNECTED:
			return nil
		case connector.ConnectInfo_DISCONNECTED:
			msg = "Not connected"
		case connector.ConnectInfo_MUST_RESTART:
			msg = "Cluster configuration changed, please quit telepresence and reconnect"
		case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED, connector.ConnectInfo_CLUSTER_FAILED, connector.ConnectInfo_DAEMON_FAILED:
			msg = resp.ErrorText
			if resp.ErrorCategory != 0 {
				cat = errcat.Category(resp.ErrorCategory)
			}
		}
		return cat.Newf("connector.Connect: %s", msg) // Return err != nil to ensure disconnect
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}
