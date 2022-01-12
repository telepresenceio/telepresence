package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

// GetCommands will return all commands implemented by the connector daemon.
func GetCommands() cliutil.CommandGroups {
	return cliutil.CommandGroups{}
}

// GetCommandsForLocal will return the same commands as GetCommands but in a non-runnable state that reports
// the error given. Should be used to build help strings even if it's not possible to connect to the connector daemon.
func GetCommandsForLocal(err error) cliutil.CommandGroups {
	cmds := GetCommands()
	for _, grp := range cmds {
		for _, cmd := range grp {
			cmd.RunE = func(_ *cobra.Command, _ []string) error {
				return fmt.Errorf("unable to run command: no connection to local daemon (%w)", err)
			}
		}
	}
	return cmds
}
