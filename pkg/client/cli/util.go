package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func kubeFlagMap(kubeFlags *pflag.FlagSet) map[string]string {
	kubeFlagMap := make(map[string]string, kubeFlags.NFlag())
	kubeFlags.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			kubeFlagMap[flag.Name] = flag.Value.String()
		} else if flag.Name == "kubeconfig" {
			// Certain options' default are bound to the connector daemon process; this is notably true of the kubeconfig file to use
			// So if we connect, disconnect, switch kubeconfigs, and reconnect, we'll connect to our old context -- setting the flag explicitly will prevent that.
			if cfg, ok := os.LookupEnv("KUBECONFIG"); ok {
				kubeFlagMap[flag.Name] = cfg
			}
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
//  - Cleans up after itself unless retain is true (If it launches the daemon or connector, then it will shut
//    them down when it's done.  If they were already running, it will leave them running.)
//
//  - Makes the connector.Connect gRPC call to set up networking
func withConnector(cmd *cobra.Command, retain bool, request *connector.ConnectRequest, f func(context.Context, *connectorState) error) error {
	return cliutil.WithNetwork(cmd.Context(), func(ctx context.Context, daemonClient daemon.DaemonClient) error {
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			didConnect, connInfo, err := connect(ctx, connectorClient, cmd.OutOrStdout(), request)
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

func connect(ctx context.Context, connectorClient connector.ConnectorClient, stdout io.Writer, request *connector.ConnectRequest) (bool, *connector.ConnectInfo, error) {
	var ci *connector.ConnectInfo
	var err error
	if request == nil {
		// implicit calls use the current Status instead of passing flags and mapped namespaces.
		ci, err = connectorClient.Status(ctx, &empty.Empty{})
	} else {
		ci, err = connectorClient.Connect(ctx, request)
	}
	if err != nil {
		return false, nil, err
	}

	var msg string
	cat := errcat.Unknown
	switch ci.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(stdout, "Connected to context %s (%s)\n", ci.ClusterContext, ci.ClusterServer)
		return true, ci, nil
	case connector.ConnectInfo_ALREADY_CONNECTED:
		return false, ci, nil
	case connector.ConnectInfo_DISCONNECTED:
		if request != nil {
			return false, nil, cliutil.ErrNoTrafficManager
		}
		// The attempt is implicit, i.e. caused by direct invocation of another command without a
		// prior call to connect. So we make it explicit here without flags
		return connect(ctx, connectorClient, stdout, &connector.ConnectRequest{})
	case connector.ConnectInfo_MUST_RESTART:
		msg = "Cluster configuration changed, please quit telepresence and reconnect"
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED, connector.ConnectInfo_CLUSTER_FAILED, connector.ConnectInfo_DAEMON_FAILED:
		msg = ci.ErrorText
		if ci.ErrorCategory != 0 {
			cat = errcat.Category(ci.ErrorCategory)
		}
	}
	return false, nil, cat.Newf("connector.Connect: %s", msg)
}
