package cmd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
)

const defaultDuration = 30 * time.Minute

type logLevelCommand struct {
	duration   time.Duration
	localOnly  bool
	remoteOnly bool
}

func logLevelArg(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return errors.New("accepts exactly one argument (the log level)")
	}
	_, err := clog.ParseLevel(args[0])
	if err != nil {
		return err
	}
	return nil
}

func loglevel() *cobra.Command {
	lls := logLevelCommand{}
	cmd := &cobra.Command{
		Use:       fmt.Sprintf("loglevel <%s>", strings.Join(clog.LevelStrings, ",")),
		Args:      logLevelArg,
		Short:     "Temporarily change the log-level of the traffic-manager, traffic-agent, and user and root daemons",
		RunE:      lls.setTempLogLevel,
		ValidArgs: clog.LevelStrings,
		Annotations: map[string]string{
			ann.Session: ann.Required,
		},
	}
	flags := cmd.Flags()
	flags.DurationVarP(&lls.duration, "duration", "d", defaultDuration, "The time that the log-level will be in effect (0s means indefinitely)")
	flags.BoolVarP(&lls.localOnly, "local-only", "l", false, "Only affect the user and root daemons")
	flags.BoolVarP(&lls.remoteOnly, "remote-only", "r", false, "Only affect the traffic-manager and traffic-agents")
	return cmd
}

func (lls *logLevelCommand) setTempLogLevel(cmd *cobra.Command, args []string) error {
	rq := &connector.LogLevelRequest{LogLevel: args[0], Duration: durationpb.New(lls.duration)}
	switch {
	case lls.localOnly && lls.remoteOnly:
		return errcat.User.New("the local-only and remote-only options are mutually exclusive")
	case lls.localOnly:
		rq.Scope = connector.LogLevelRequest_LOCAL_ONLY
	case lls.remoteOnly:
		rq.Scope = connector.LogLevelRequest_REMOTE_ONLY
	}

	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	defer progress.Stop(cmd.Context())
	ctx := cmd.Context()
	userD := daemon.MustGetUserClient(ctx)
	_, err := userD.SetLogLevel(ctx, rq)
	return grpc.FromGRPC(err)
}
