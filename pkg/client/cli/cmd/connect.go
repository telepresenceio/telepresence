package cmd

import (
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/usg"
)

func connectCmd() *cobra.Command {
	var request *daemon.CobraRequest

	cmd := &cobra.Command{
		Use:   "connect [flags] [-- <command to run while connected>]",
		Args:  cobra.ArbitraryArgs,
		Short: "Connect to a cluster",
		Annotations: map[string]string{
			ann.Session:      ann.Required,
			usg.AnnTrack:     "true",
			usg.AnnSafeFlags: "docker",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := request.CommitFlags(cmd); err != nil {
				return err
			}
			return connect.RunConnect(cmd, args)
		},
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			dir := cobra.ShellCompDirectiveNoFileComp
			if slices.Contains(os.Args, "--") {
				dir = cobra.ShellCompDirectiveDefault
			}
			return nil, dir
		},
	}
	request = daemon.InitRequest(cmd)
	return cmd
}
