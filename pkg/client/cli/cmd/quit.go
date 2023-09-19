package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func quit() *cobra.Command {
	quitDaemons := false
	quitRootDaemon := false
	quitUserDaemon := false
	cmd := &cobra.Command{
		Use:  "quit",
		Args: cobra.NoArgs,

		Short:       "Tell telepresence daemon to quit",
		Annotations: map[string]string{ann.UserDaemon: ann.Optional},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := connect.InitCommand(cmd); err != nil {
				return err
			}
			if quitUserDaemon {
				ioutil.Println(os.Stderr, "--user-daemon (-u) is deprecated, please use --stop-daemons (-s)")
				quitDaemons = true
			}
			if quitRootDaemon {
				ioutil.Println(os.Stderr, "--root-daemon (-r) is deprecated, please use --stop-daemons (-s)")
				quitDaemons = true
			}
			if quitDaemons {
				connect.Quit(cmd.Context())
			} else {
				connect.Disconnect(cmd.Context())
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.BoolVarP(&quitDaemons, "stop-daemons", "s", false, "stop all local telepresence daemons")
	flags.BoolVarP(&quitRootDaemon, "root-daemon", "r", false, "stop daemons")
	flags.BoolVarP(&quitUserDaemon, "user-daemon", "u", false, "stop daemons")

	// retained for backward compatibility but hidden from now on
	flags.Lookup("root-daemon").Hidden = true
	flags.Lookup("user-daemon").Hidden = true
	return cmd
}
