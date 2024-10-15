package cmd

import (
	"context"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "config",
	}
	cmd.AddCommand(configView())
	return cmd
}

const clientOnlyFlag = "client-only"

func configView() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "view",
		Args:              cobra.NoArgs,
		PersistentPreRunE: output.DefaultYAML,
		Short:             "View current Telepresence configuration",
		RunE:              runConfigView,
		Annotations: map[string]string{
			ann.Session: ann.Optional,
		},
	}
	cmd.Flags().BoolP(clientOnlyFlag, "c", false, "Only view config from client file.")
	return cmd
}

// GetCommandKubeConfig will return the fully resolved client.Kubeconfig for the given command.
func GetCommandKubeConfig(cmd *cobra.Command) (context.Context, *client.Kubeconfig, error) {
	ctx := cmd.Context()
	uc := daemon.GetUserClient(ctx)
	var kc *client.Kubeconfig
	var err error
	if uc != nil && !cmd.Flag("context").Changed {
		// Get the context that we're currently connected to.
		var ci *connector.ConnectInfo
		ci, err = uc.Status(ctx, &empty.Empty{})
		if err == nil {
			ctx, kc, err = client.NewKubeconfig(ctx, map[string]string{"context": ci.ClusterContext}, "")
		}
	} else {
		if daemon.GetRequest(ctx) == nil {
			if ctx, err = daemon.WithDefaultRequest(ctx, cmd); err != nil {
				return ctx, nil, err
			}
		}
		rq := daemon.GetRequest(ctx)
		ctx, kc, err = client.NewKubeconfig(ctx, rq.KubeFlags, rq.ManagerNamespace)
	}
	return ctx, kc, err
}

func runConfigView(cmd *cobra.Command, _ []string) error {
	var cfg client.SessionConfig
	clientOnly, _ := cmd.Flags().GetBool(clientOnlyFlag)
	if !clientOnly {
		cmd.Annotations = map[string]string{
			ann.Session: ann.Required,
		}
		if err := connect.InitCommand(cmd); err != nil {
			// Unable to establish a session, so try to convey the local config instead. It
			// may be helpful in diagnosing the problem.
			cmd.Annotations = map[string]string{}
			clientOnly = true
		}
	}

	if clientOnly {
		if err := connect.InitCommand(cmd); err != nil {
			return err
		}

		ctx, _, err := GetCommandKubeConfig(cmd)
		if err != nil {
			return err
		}
		cfg.Config = client.GetConfig(ctx)
		cfg.ClientFile = client.GetConfigFile(ctx)
		output.Object(cmd.Context(), &cfg, true)
		return nil
	}

	cmd.Annotations = map[string]string{
		ann.Session: ann.Required,
	}
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	cc, err := daemon.GetUserClient(ctx).GetConfig(ctx, &empty.Empty{})
	if err != nil {
		return err
	}
	err = client.UnmarshalJSON(cc.Json, &cfg, false)
	if err != nil {
		return err
	}
	output.Object(ctx, &cfg, true)
	return nil
}
