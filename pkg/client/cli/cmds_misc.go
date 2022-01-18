package cli

import (
	"context"
	"fmt"
	"runtime"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// ClusterIdCommand is a simple command that makes it easier for users to
// figure out what their cluster ID is. For now this is just used when
// people are making licenses for air-gapped environments
func ClusterIdCommand() *cobra.Command {
	kubeConfig := kates.NewConfigFlags(false)
	cmd := &cobra.Command{
		Use:  "current-cluster-id",
		Args: cobra.NoArgs,

		Short: "Get cluster ID for your kubernetes cluster",
		Long:  "Get cluster ID for your kubernetes cluster, mostly used for licenses in air-gapped environments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := kates.NewClientFromConfigFlags(kubeConfig)
			if err != nil {
				return err
			}
			clusterID, err := k8sapi.GetClusterID(cmd.Context(), client)
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
	var dnsIP string
	var mappedNamespaces []string

	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	cmd := &cobra.Command{
		Use:   "connect [flags] [-- <command to run while connected>]",
		Args:  cobra.ArbitraryArgs,
		Short: "Connect to a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			request := &connector.ConnectRequest{
				KubeFlags:        kubeFlagMap(kubeFlags),
				MappedNamespaces: mappedNamespaces,
			}

			if len(args) == 0 {
				return withConnector(cmd, true, request, func(_ context.Context, _ *connectorState) error {
					return nil
				})
			}

			return withConnector(cmd, false, request, func(ctx context.Context, _ *connectorState) error {
				return proc.Run(ctx, nil, args[0], args[1:]...)
			})
		},
	}

	flags := cmd.Flags()

	nwFlags := pflag.NewFlagSet("Telepresence networking flags", 0)
	// TODO: Those flags aren't applicable on a Linux with systemd-resolved configured either but
	//  that's unknown until it's been tested during the first connect attempt.
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		nwFlags.StringVarP(&dnsIP,
			"dns", "", "",
			"DNS IP address to intercept locally. Defaults to the first nameserver listed in /etc/resolv.conf.",
		)
	}
	nwFlags.StringSliceVar(&mappedNamespaces,
		"mapped-namespaces", nil, ``+
			`Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. `+
			`Defaults to all namespaces`)
	flags.AddFlagSet(nwFlags)

	kubeConfig := kates.NewConfigFlags(false)
	kubeConfig.Namespace = nil // "connect", don't take --namespace
	kubeConfig.AddFlags(kubeFlags)
	flags.AddFlagSet(kubeFlags)
	return cmd
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
	quitRootDaemon := false
	quitUserDaemon := false
	cmd := &cobra.Command{
		Use:  "quit",
		Args: cobra.NoArgs,

		Short: "Tell telepresence daemon to quit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cliutil.Disconnect(cmd.Context(), quitUserDaemon, quitRootDaemon)
		},
	}
	flags := cmd.Flags()
	flags.BoolVarP(&quitRootDaemon, "root-daemon", "r", false, "stop root daemon")
	flags.BoolVarP(&quitUserDaemon, "user-daemon", "u", false, "stop user daemon")
	return cmd
}
