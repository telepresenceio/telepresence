package cmd

import (
	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Telepresence configuration commands",
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
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	cmd.Flags().BoolP(clientOnlyFlag, "c", false, "Only view config from client file.")
	return cmd
}

func runConfigView(cmd *cobra.Command, _ []string) error {
	defer func() {
		progress.Stop(cmd.Context())
	}()

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

		ctx, _, err := daemon.GetCommandKubeConfig(cmd)
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
