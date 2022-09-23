package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// ClusterIdCommand is a simple command that makes it easier for users to
// figure out what their cluster ID is. For now this is just used when
// people are making licenses for air-gapped environments
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

func connectCommand() *cobra.Command {
	var kubeFlags *pflag.FlagSet
	var request *connector.ConnectRequest

	cmd := &cobra.Command{
		Use:   "connect [flags] [-- <command to run while connected>]",
		Args:  cobra.ArbitraryArgs,
		Short: "Connect to a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			request.KubeFlags = kubeFlagMap(kubeFlags)
			if len(args) == 0 {
				return withConnector(cmd, true, request, func(_ context.Context, _ *connectorState) error {
					return nil
				})
			}

			return withConnector(cmd, false, request, func(ctx context.Context, _ *connectorState) error {
				return proc.Run(ctx, nil, cmd, args[0], args[1:]...)
			})
		},
	}
	request, kubeFlags = initConnectRequest(cmd)
	return cmd
}

func initConnectRequest(cmd *cobra.Command) (*connector.ConnectRequest, *pflag.FlagSet) {
	cr := connector.ConnectRequest{}
	flags := cmd.Flags()

	nwFlags := pflag.NewFlagSet("Telepresence networking flags", 0)
	nwFlags.StringSliceVar(&cr.MappedNamespaces,
		"mapped-namespaces", nil, ``+
			`Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. `+
			`Defaults to all namespaces`)
	flags.AddFlagSet(nwFlags)

	kubeConfig := genericclioptions.NewConfigFlags(false)
	kubeConfig.Namespace = nil // "connect", don't take --namespace
	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig.AddFlags(kubeFlags)
	flags.AddFlagSet(kubeFlags)
	return &cr, kubeFlags
}

func dashboardCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "dashboard",
		Args: cobra.NoArgs,

		Short: "Open the dashboard in a web page",
		RunE: func(cmd *cobra.Command, args []string) error {
			cloudCfg := client.GetConfig(cmd.Context()).Cloud

			// Ensure we're logged in
			resultCode, err := cliutil.EnsureLoggedIn(cmd.Context(), "")
			if err != nil {
				return err
			}

			if resultCode == connector.LoginResult_OLD_LOGIN_REUSED {
				// The LoginFlow takes the user to the dashboard, so we only need to
				// explicitly take the user to the dashboard if they were already
				// logged in.
				if err := browser.OpenURL(fmt.Sprintf("https://%s/cloud/preview", cloudCfg.SystemaHost)); err != nil {
					return err
				}
			}

			return nil
		}}
}

func quitCommand() *cobra.Command {
	quitDaemons := false
	quitRootDaemon := false
	quitUserDaemon := false
	cmd := &cobra.Command{
		Use:  "quit",
		Args: cobra.NoArgs,

		Short: "Tell telepresence daemon to quit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if quitUserDaemon {
				fmt.Fprintln(os.Stderr, "--user-daemon (-u) is deprecated, please use --stop-daemons (-s)")
			}
			if quitRootDaemon {
				fmt.Fprintln(os.Stderr, "--root-daemon (-r) is deprecated, please use --stop-daemons (-s)")
			}
			return cliutil.Disconnect(cmd.Context(), quitDaemons || quitUserDaemon || quitRootDaemon)
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
