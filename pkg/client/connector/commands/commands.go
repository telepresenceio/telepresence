package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

type exampleCommandInfo struct {
	sampleFlag string
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
	cmd.Flags().StringVar(&info.sampleFlag, "flag", "nothing", "Flag for flagging")
	return cmd
}

func GetCommands() map[string][]*cobra.Command {
	return map[string][]*cobra.Command{
		"Remote Commands": {exampleCommand()},
	}
}

func CommandsToRPC(cmds map[string][]*cobra.Command) *connector.CommandGroups {
	groups := make(map[string]*connector.CommandGroups_Commands)
	for name, g := range cmds {
		cmds := []*connector.CommandGroups_Command{}
		for _, cmd := range g {
			flags := []*connector.CommandGroups_Flag{}
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				flags = append(flags, &connector.CommandGroups_Flag{
					Type: f.Value.Type(),
					Flag: f.Name,
					Help: f.Usage,
				})
			})
			cmds = append(cmds, &connector.CommandGroups_Command{
				Name:      cmd.Use,
				LongHelp:  cmd.Long,
				ShortHelp: cmd.Short,
				Flags:     flags,
			})
		}
		groups[name] = &connector.CommandGroups_Commands{Commands: cmds}
	}
	return &connector.CommandGroups{CommandGroups: groups}
}
