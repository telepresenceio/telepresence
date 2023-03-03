package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
)

// ClusterIdCommand is a simple command that makes it easier for users to
// figure out what their cluster ID is. For now this is just used when
// people are making licenses for air-gapped environments.
func ClusterIdCommand() *cobra.Command {
	kubeConfig := genericclioptions.NewConfigFlags(false)
	cmd := &cobra.Command{
		Use:  "current-cluster-id",
		Args: cobra.NoArgs,

		Short: "Get cluster ID for your kubernetes cluster",
		Long:  "Get cluster ID for your kubernetes cluster, mostly used for licenses in air-gapped environments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			restConfig, err := kubeConfig.ToRESTConfig()
			if err != nil {
				return err
			}
			ki, err := kubernetes.NewForConfig(restConfig)
			if err != nil {
				return err
			}
			clusterID, err := k8sapi.GetClusterID(k8sapi.WithK8sInterface(cmd.Context(), ki))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Cluster ID: %s\n", clusterID)
			return nil
		},
	}
	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig.AddFlags(kubeFlags)
	cmd.Flags().AddFlagSet(kubeFlags)
	return cmd
}

func quitCommand() *cobra.Command {
	quitDaemons := false
	quitRootDaemon := false
	quitUserDaemon := false
	cmd := &cobra.Command{
		Use:  "quit",
		Args: cobra.NoArgs,

		Short:       "Tell telepresence daemon to quit",
		Annotations: map[string]string{ann.UserDaemon: ann.Optional},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := util.InitCommand(cmd); err != nil {
				return err
			}
			if quitUserDaemon {
				fmt.Fprintln(os.Stderr, "--user-daemon (-u) is deprecated, please use --stop-daemons (-s)")
				quitDaemons = true
			}
			if quitRootDaemon {
				fmt.Fprintln(os.Stderr, "--root-daemon (-r) is deprecated, please use --stop-daemons (-s)")
				quitDaemons = true
			}
			ctx := cmd.Context()
			if quitDaemons && util.GetUserDaemon(ctx) == nil {
				// User daemon isn't running. If the root daemon is running, we must
				// kill it from here.
				if conn, err := socket.Dial(ctx, socket.DaemonName); err == nil {
					_, _ = daemon.NewDaemonClient(conn).Quit(ctx, &empty.Empty{})
				}
			}
			return util.Disconnect(cmd.Context(), quitDaemons)
		},
	}
	flags := cmd.Flags()
	flags.BoolVarP(&quitDaemons, "stop-daemons", "s", false, "stop the traffic-manager and network daemons")
	flags.BoolVarP(&quitRootDaemon, "root-daemon", "r", false, "stop daemons")
	flags.BoolVarP(&quitUserDaemon, "user-daemon", "u", false, "stop daemons")

	// retained for backward compatibility but hidden from now on
	flags.Lookup("root-daemon").Hidden = true
	flags.Lookup("user-daemon").Hidden = true
	return cmd
}
