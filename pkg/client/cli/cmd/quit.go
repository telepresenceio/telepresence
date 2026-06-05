package cmd

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/usg"
)

func quit() *cobra.Command {
	quitDaemons := false
	cmd := &cobra.Command{
		Use:   "quit",
		Args:  cobra.NoArgs,
		Short: "Tell telepresence daemons to quit",
		Annotations: map[string]string{
			usg.AnnTrack:     "true",
			usg.AnnSafeFlags: "stop-daemons",
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if quitDaemons {
				connect.InitProgressWriter(cmd)
				connect.Quit(cmd.Context())
			} else {
				cmd.Annotations = map[string]string{ann.UserDaemon: ann.Optional}
				if err := connect.InitCommand(cmd); err != nil {
					return err
				}
				connect.Disconnect(cmd.Context())
			}
			return nil
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&quitDaemons, "stop-daemons", "s", false, "stop all local telepresence daemons")
	return cmd
}
