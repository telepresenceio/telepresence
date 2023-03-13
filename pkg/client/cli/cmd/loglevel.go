package cmd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
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
	lvl, err := logrus.ParseLevel(args[0])
	if err != nil {
		return err
	}
	switch lvl {
	case logrus.PanicLevel, logrus.FatalLevel:
		return fmt.Errorf("unsupported log level: %s", lvl)
	}
	return nil
}

func loglevel() *cobra.Command {
	lvs := logrus.AllLevels[2:] // Don't include `panic` and `fatal`
	lvStrs := make([]string, len(lvs))
	for i, lv := range lvs {
		lvStrs[i] = lv.String()
	}
	lls := logLevelCommand{}
	cmd := &cobra.Command{
		Use:       fmt.Sprintf("loglevel <%s>", strings.Join(lvStrs, ",")),
		Args:      logLevelArg,
		Short:     "Temporarily change the log-level of the traffic-manager, traffic-agent, and user and root daemons",
		RunE:      lls.setTempLogLevel,
		ValidArgs: lvStrs,
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
	ctx := cmd.Context()
	userD := daemon.GetUserClient(ctx)
	_, err := userD.SetLogLevel(ctx, rq)
	return err
}
