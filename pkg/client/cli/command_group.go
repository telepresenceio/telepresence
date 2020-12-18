package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CommandGroup represents a group of commands and the name of that group
type CommandGroup struct {
	Name     string
	Commands []*cobra.Command
}

// FlagGroup represents a group of flags and the name of that group
type FlagGroup struct {
	Name  string
	Flags *pflag.FlagSet
}

var commandGroupMap = make(map[string][]CommandGroup)
var globalFlagGroups []FlagGroup

func init() {
	cobra.AddTemplateFunc("commandGroups", func(cmd *cobra.Command) []CommandGroup {
		return commandGroupMap[cmd.Name()]
	})
	cobra.AddTemplateFunc("globalFlagGroups", func() []FlagGroup {
		return globalFlagGroups
	})
}

func setCommandGroups(cmd *cobra.Command, groups []CommandGroup) {
	commandGroupMap[cmd.Name()] = groups
}

// AddCommandGroups adds all the groups in the given CommandGroup to the command,  replaces
// the its standard usage template with a template that groups the commands according to that group.
func AddCommandGroups(cmd *cobra.Command, groups []CommandGroup) {
	for _, group := range groups {
		cmd.AddCommand(group.Commands...)
	}
	setCommandGroups(cmd, groups)

	// Set a usage template that is derived from the default but replaces the "Available Commands"
	// section with the commandGroups() from the given command
	cmd.SetUsageTemplate(`Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range $group := commandGroups .}}
  {{$group.Name}}:{{range $group.Commands}}
    {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalNonPersistentFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if true}}

Global Flags:{{range $group := globalFlagGroups}}

  {{$group.Name}}:
{{$group.Flags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`)
}
