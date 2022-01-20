package cliutil

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

func CommandsToRPC(cmds CommandGroups) *connector.CommandGroups {
	groups := make(map[string]*connector.CommandGroups_Commands)
	for name, g := range cmds {
		cmds := []*connector.CommandGroups_Command{}
		for _, cmd := range g {
			flags := []*connector.CommandGroups_Flag{}
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				flags = append(flags, &connector.CommandGroups_Flag{
					Type:         f.Value.Type(),
					Flag:         f.Name,
					Help:         f.Usage,
					Shorthand:    f.Shorthand,
					DefaultValue: f.Value.String(),
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

func RPCToCommands(remote *connector.CommandGroups, runner func(*cobra.Command, []string) error) (CommandGroups, error) {
	groups := CommandGroups{}
	for name, cmds := range remote.GetCommandGroups() {
		commands := []*cobra.Command{}
		for _, cmd := range cmds.GetCommands() {
			cobraCmd := &cobra.Command{
				Use:   cmd.GetName(),
				Long:  cmd.GetLongHelp(),
				Short: cmd.GetShortHelp(),
				RunE:  runner,
			}
			for _, flag := range cmd.GetFlags() {
				tp, err := TypeFromString(flag.GetType())
				if err != nil {
					return nil, err
				}
				val, err := tp.NewFlagValueFromPFlagString(flag.GetDefaultValue())
				if err != nil {
					return nil, err
				}
				if flag.GetShorthand() == "" {
					cobraCmd.Flags().Var(val, flag.GetFlag(), flag.GetHelp())
				} else {
					cobraCmd.Flags().VarP(val, flag.GetFlag(), flag.GetShorthand(), flag.GetHelp())
				}
			}
			commands = append(commands, cobraCmd)
		}
		groups[name] = commands
	}
	return groups, nil
}
