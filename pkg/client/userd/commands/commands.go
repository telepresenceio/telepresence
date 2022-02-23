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
	groups := GetCommands()
	for _, cmds := range groups {
		for _, cmd := range cmds {
			cmd.RunE = func(_ *cobra.Command, _ []string) error {
				// err here will be ErrNoUserDaemon "telepresence user daemon is not running"
				return fmt.Errorf("unable to run command: %w", err)
			}
		}
	}
	return groups
}
