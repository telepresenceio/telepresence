package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

type exampleCommandInfo struct {
	sampleFlag string
	array      []string
}

func (e *exampleCommandInfo) run(cmd *cobra.Command, _ []string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Hello from remote!\nFlag is set to %s\n", e.sampleFlag)
	return nil
}

func exampleCommand() *cobra.Command {
	info := exampleCommandInfo{}
	cmd := &cobra.Command{
		Use:   "example",
		Short: "A Remote command",
		Long:  `Try this to see what happens when a command is run by the user daemon!`,
		RunE:  info.run,
	}
	cmd.Flags().StringVarP(&info.sampleFlag, "flag", "f", "nothing", "Flag for flagging")
	cmd.Flags().StringArrayVar(&info.array, "array", []string{"a", "b", "c"}, "array")
	return cmd
}

func GetCommands() cliutil.CommandGroups {
	return map[string][]*cobra.Command{
		"Remote Commands": {exampleCommand()},
	}
}
