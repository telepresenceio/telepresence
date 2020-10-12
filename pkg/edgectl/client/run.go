package client

import (
	"github.com/spf13/cobra"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
)

var RunHelp = `edgectl run is a shorthand command for starting the daemon, connecting to the traffic
manager, adding an intercept, running a command, and then removing the intercept,
disconnecting, and quitting the daemon.

The command ensures that only those resources that were acquired are cleaned up. This
means that the daemon will not quit if it was already started, no disconnect will take
place if the connection was already established, and the intercept will not be removed
if it was already added.

Unless the daemon is already started, an attempt will be made to start it. This will
involve a call to sudo unless this command is run as root (not recommended).

Run a command:
    edgectl run -d hello -n example-url -t 9000 -- <command> arguments...
`

// RunInfo contains all parameters needed in order to run an intercepted command.
type RunInfo struct {
	rpc.ConnectRequest
	rpc.InterceptRequest
	DNS      string
	Fallback string
}

// RunCommand will ensure that an intercept is in place and then execute the command given by args[0]
// and the arguments starting at args[1:].
func (ri *RunInfo) RunCommand(cmd *cobra.Command, args []string) error {
	// Fail early if intercept args are inconsistent
	if err := prepareIntercept(cmd, &ri.InterceptRequest); err != nil {
		return err
	}

	ri.ConnectRequest.Namespace = ri.InterceptRequest.Namespace // resolve struct ambiguity

	ds, err := newDaemonState(cmd.OutOrStdout(), ri.DNS, ri.Fallback)
	if err != nil && err != daemonIsNotRunning {
		return err
	}

	out := cmd.OutOrStdout()
	return edgectl.WithEnsuredState(ds, func() error {
		ri.InterceptEnabled = true
		cs, err := newConnectorState(ds.grpc, &ri.ConnectRequest, out)
		if err != nil && err != connectorIsNotRunning {
			return err
		}
		return edgectl.WithEnsuredState(cs, func() error {
			is := newInterceptState(cs.grpc, &ri.InterceptRequest, out)
			return edgectl.WithEnsuredState(is, func() error {
				return start(args[0], args[1:], true, cmd.InOrStdin(), out, cmd.OutOrStderr())
			})
		})
	})
}
