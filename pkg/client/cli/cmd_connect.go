package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
)

func connectCommand(ctx context.Context) *cobra.Command {
	var kubeFlags *pflag.FlagSet
	var request *connect.Request

	cmd := &cobra.Command{
		Use:   "connect [flags] [-- <command to run while connected>]",
		Args:  cobra.ArbitraryArgs,
		Short: "Connect to a cluster",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			request.KubeFlags = util.FlagMap(kubeFlags)
			cmd.SetContext(connect.WithRequest(cmd.Context(), request))
			return util.RunConnect(cmd, args)
		},
	}
	request, kubeFlags = connect.InitRequest(ctx, cmd)
	return cmd
}
