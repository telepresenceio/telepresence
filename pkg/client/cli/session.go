package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type sessionInfo struct {
	cmd *cobra.Command
}

// withDaemon establishes a daemon session and calls the function with the gRPC client. If
// retain is false, the session will end unless it was already started.
func (si *sessionInfo) withDaemon(retain bool, f func(state *daemonState) error) error {
	// OK with dns empty. Daemon must be up and running
	ds, err := si.newDaemonState()
	if err == errDaemonIsNotRunning {
		err = nil
	}
	if err != nil {
		return err
	}
	defer ds.disconnect()
	return client.WithEnsuredState(ds, retain, func() error { return f(ds) })
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

func withStartedDaemon(cmd *cobra.Command, f func(state *daemonState) error) error {
	err := assertDaemonStarted()
	if err != nil {
		return err
	}
	si := &sessionInfo{cmd: cmd}
	return si.withDaemon(false, f)
}

// withConnector establishes a daemon and a connector session and calls the function with the gRPC client. If
// retain is false, the sessions will end unless they were already started.
func (si *sessionInfo) withConnector(retain bool, f func(state *connectorState) error) error {
	return si.withDaemon(retain, func(ds *daemonState) error {
		cs, err := si.newConnectorState(ds.grpc)
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
	err := assertDaemonStarted()
	if err != nil {
		return err
	}
	err = assertConnectorStarted()
	if err != nil {
		return err
	}
	si := &sessionInfo{cmd: cmd}
	return si.withConnector(false, f)
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
		Use:  "connect [flags] [-- <additional kubectl arguments...>]",
		Args: cobra.ArbitraryArgs,

		Short: "Connect to a cluster",
		RunE:  si.connect,
	}
	return cmd
}
