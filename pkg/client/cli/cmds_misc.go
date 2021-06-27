package cli

import (
	"context"
	"fmt"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

// getClusterID is a simple command that makes it easier for users to
// figure out what their cluster ID is. For now this is just used when
// people are making licenses for air-gapped environments
func ClusterIdCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "current-cluster-id",
		Args: cobra.NoArgs,

		Short: "Get cluster ID for your kubernetes cluster",
		Long:  "Get cluster ID for your kubernetes cluster, mostly used for licenses in air-gapped environments",
		RunE: func(flags *cobra.Command, _ []string) error {
			// NB: Even without logging, dexec is still an improvement over os/exec
			// because it better handles kubectl hanging.
			cmd := dexec.CommandContext(flags.Context(), "kubectl", "get", "ns", "default", "-o", "jsonpath={.metadata.uid}")
			cmd.DisableLogging = true
			cmd.Stderr = flags.ErrOrStderr()

			output, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("kubectl: %w", err)
			}
			fmt.Fprintf(flags.OutOrStdout(), "Cluster ID: %s", output)
			return nil
		},
	}
}

func connectCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "connect [flags] [-- <command to run while connected>]",
		Args: cobra.ArbitraryArgs,

		Short: "Connect to a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return withConnector(cmd, true, func(_ context.Context, _ connector.ConnectorClient, _ *connector.ConnectInfo) error {
					return nil
				})
			}
			return withConnector(cmd, false, func(ctx context.Context, _ connector.ConnectorClient, _ *connector.ConnectInfo) error {
				return start(ctx, args[0], args[1:], true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			})
		},
	}
}

func dashboardCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "dashboard",
		Args: cobra.NoArgs,

		Short: "Open the dashboard in a web page",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := client.LoadEnv(cmd.Context())
			if err != nil {
				return err
			}

			// Ensure we're logged in
			resultCode, err := cliutil.EnsureLoggedIn(cmd.Context())
			if err != nil {
				return err
			}

			if resultCode == connector.LoginResult_OLD_LOGIN_REUSED {
				// The LoginFlow takes the user to the dashboard, so we only need to
				// explicitly take the user to the dashboard if they were already
				// logged in.
				if err := browser.OpenURL(fmt.Sprintf("https://%s/cloud/preview", env.SystemAHost)); err != nil {
					return err
				}
			}

			return nil
		}}
}

func quitCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "quit",
		Args: cobra.NoArgs,

		Short: "Tell telepresence daemon to quit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return quit(cmd.Context())
		},
	}
}
