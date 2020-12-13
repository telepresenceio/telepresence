package cli

import (
	"github.com/spf13/cobra"
)

// CommandGroup represents a group of commands and the name of that group
type CommandGroup struct {
	Name     string
	Commands []*cobra.Command
}

var commandGroupMap = make(map[string][]CommandGroup)

func init() {
	cobra.AddTemplateFunc("commandGroups", func(cmd *cobra.Command) []CommandGroup {
		return commandGroupMap[cmd.Name()]
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
{{.Example}}{{end}}{{range commandGroups .}}

{{.Name}}:{{range .Commands}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`)
}
