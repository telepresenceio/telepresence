package connect

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
)

func Command() *cobra.Command {
	var request *daemon.Request

	cmd := &cobra.Command{
		Use:   "connect [flags] [-- <command to run while connected>]",
		Args:  cobra.ArbitraryArgs,
		Short: "Connect to a cluster",
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			request.CommitFlags(cmd)
			return RunConnect(cmd, args)
		},
	}
	request = daemon.InitRequest(cmd)
	return cmd
}

func QuitCommand() *cobra.Command {
	quitDaemons := false
	quitRootDaemon := false
	quitUserDaemon := false
	cmd := &cobra.Command{
		Use:  "quit",
		Args: cobra.NoArgs,

		Short:       "Tell telepresence daemon to quit",
		Annotations: map[string]string{ann.UserDaemon: ann.Optional},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := InitCommand(cmd); err != nil {
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
			if quitDaemons && daemon.GetUserClient(ctx) == nil {
				// User daemon isn't running. If the root daemon is running, we must
				// kill it from here.
				if conn, err := socket.Dial(ctx, socket.DaemonName); err == nil {
					_, _ = rpc.NewDaemonClient(conn).Quit(ctx, &empty.Empty{})
				}
			}
			return Disconnect(cmd.Context(), quitDaemons)
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
