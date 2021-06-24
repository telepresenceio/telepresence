package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

type sessionInfo struct {
	cmd *cobra.Command
}

func kubeFlagMap() map[string]string {
	kubeFlagMap := make(map[string]string)
	kubeFlags.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			kubeFlagMap[flag.Name] = flag.Value.String()
		}
	})
	return kubeFlagMap
}

// withConnector establishes a daemon and a connector session and calls the function with the gRPC client. If
// retain is false, the sessions will end unless they were already started.
func (si *sessionInfo) withConnector(retain bool, f func(state *connectorState) error) error {
	return cliutil.WithDaemon(si.cmd.Context(), dnsIP, func(ctx context.Context, daemonClient daemon.DaemonClient) (err error) {
		if cliutil.DidLaunchDaemon(ctx) {
			defer func() {
				if err != nil || !retain {
					_ = cliutil.QuitDaemon(dcontext.WithoutCancel(ctx))
				}
			}()
		}

		cs, err := si.newConnectorState(daemonClient)
		if err == errConnectorIsNotRunning {
			err = nil
		}
		if err != nil {
			return err
		}
		defer cs.disconnect()
		return client.WithEnsuredState(cs, retain, func() error { return f(cs) })
	})
}

func withStartedConnector(cmd *cobra.Command, f func(state *connectorState) error) error {
	return cliutil.WithStartedDaemon(cmd.Context(), func(ctx context.Context, daemonClient daemon.DaemonClient) error {
		if err := assertConnectorStarted(); err != nil {
			return err
		}
		si := &sessionInfo{cmd: cmd}
		cs, err := si.newConnectorState(daemonClient)
		if err == errConnectorIsNotRunning {
			err = nil
		}
		if err != nil {
			return err
		}
		defer cs.disconnect()
		return client.WithEnsuredState(cs, true, func() error { return f(cs) })
	})
}

func (si *sessionInfo) connect(cmd *cobra.Command, args []string) error {
	si.cmd = cmd
	if len(args) == 0 {
		return si.withConnector(true, func(_ *connectorState) error { return nil })
	}
	return si.withConnector(false, func(cs *connectorState) error {
		return start(cmd.Context(), args[0], args[1:], true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	})
}

func (si *sessionInfo) newConnectorState(daemon daemon.DaemonClient) (*connectorState, error) {
	cs := NewConnectorState(si, daemon)
	err := assertConnectorStarted()
	if err == nil {
		err = cs.connect()
	}
	return cs, err
}

func connectCommand() *cobra.Command {
	si := &sessionInfo{}
	cmd := &cobra.Command{
		Use:  "connect [flags] [-- <command to run while connected>]",
		Args: cobra.ArbitraryArgs,

		Short: "Connect to a cluster",
		RunE:  si.connect,
	}
	return cmd
}
