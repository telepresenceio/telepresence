package cliutil

import (
	"os"
	"strconv"

	"github.com/moby/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type CommandGroups map[string][]*cobra.Command

// FlagGroup represents a group of flags and the name of that group
type FlagGroup struct {
	Name  string
	Flags *pflag.FlagSet
}

var UserDaemonRunning = false
var commandGroupMap = make(map[string]CommandGroups)
var GlobalFlagGroups []FlagGroup
var DeprecatedGlobalFlags *pflag.FlagSet
var kubeFlags *pflag.FlagSet

func init() {
	kubeFlags = pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig := genericclioptions.NewConfigFlags(false)
	kubeConfig.Namespace = nil // "connect", don't take --namespace
	kubeConfig.AddFlags(kubeFlags)

	flagEqual := func(a, b *pflag.Flag) bool {
		if a == b {
			return true
		}
		if a == nil || b == nil {
			return false
		}
		return a.Name == b.Name && a.Usage == b.Usage && a.Hidden == b.Hidden
	}

	cobra.AddTemplateFunc("commandGroups", func(cmd *cobra.Command) CommandGroups {
		return commandGroupMap[cmd.Name()]
	})
	cobra.AddTemplateFunc("globalFlagGroups", func() []FlagGroup {
		return GlobalFlagGroups
	})
	cobra.AddTemplateFunc("userDaemonRunning", func() bool {
		return UserDaemonRunning
	})
	cobra.AddTemplateFunc("flags", func(cmd *cobra.Command) *pflag.FlagSet {
		ngFlags := pflag.NewFlagSet("local", pflag.ContinueOnError)
		cmd.Flags().VisitAll(func(flag *pflag.Flag) {
			for _, g := range GlobalFlagGroups {
				if flagEqual(flag, g.Flags.Lookup(flag.Name)) {
					return
				}
			}
			if flagEqual(flag, kubeFlags.Lookup(flag.Name)) {
				return
			}
			ngFlags.AddFlag(flag)
		})
		return ngFlags
	})
	cobra.AddTemplateFunc("hasKubeFlags", func(cmd *cobra.Command) bool {
		yep := true
		flags := cmd.Flags()
		kubeFlags.VisitAll(func(flag *pflag.Flag) {
			if yep && !flagEqual(flag, flags.Lookup(flag.Name)) {
				yep = false
			}
		})
		return yep
	})
	cobra.AddTemplateFunc("kubeFlags", func() *pflag.FlagSet {
		return kubeFlags
	})
	cobra.AddTemplateFunc("wrappedFlagUsages", func(flags *pflag.FlagSet) string {
		// This is based off of what Docker does (github.com/docker/cli/cli/cobra.go), but is
		// adjusted
		//  1. to take a pflag.FlagSet instead of a cobra.Command, so that we can have flag groups, and
		//  2. to correct for the ways that Docker upsets me.

		var cols int
		var err error

		// Obey COLUMNS if the shell or user sets it.  (Docker doesn't do this.)
		if cols, err = strconv.Atoi(os.Getenv("COLUMNS")); err == nil {
			goto end
		}

		// Try to detect the size of the stdout file descriptor.  (Docker checks stdin, not stdout.)
		if ws, err := term.GetWinsize(1); err == nil {
			cols = int(ws.Width)
			goto end
		}

		// If stdout is a terminal but we were unable to get its size (I'm not sure how that can
		// happen), then fall back to assuming 80.  If stdout isn't a terminal, then we leave cols
		// as 0, meaning "don't wrap it".  (Docker wraps it even if stdout isn't a terminal.)
		if term.IsTerminal(1) {
			cols = 80
			goto end
		}

	end:
		return flags.FlagUsagesWrapped(cols)
	})
}

func setCommandGroups(cmd *cobra.Command, groups CommandGroups) {
	commandGroupMap[cmd.Name()] = groups
}

// AddCommandGroups adds all the groups in the given CommandGroups to the command,  replaces
// the its standard usage template with a template that groups the commands according to that group.
func AddCommandGroups(cmd *cobra.Command, groups CommandGroups) {
	for _, commands := range groups {
		cmd.AddCommand(commands...)
	}
	setCommandGroups(cmd, groups)

	// Set a usage template that is derived from the default but replaces the "Available Commands"
	// section with the commandGroups() from the given command
	cmd.SetUsageTemplate(`Usage:{{if and (.Runnable) (not .HasAvailableSubCommands)}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands{{- if not userDaemonRunning }} (list may be incomplete because the User Daemon isn't running){{- end}}:
{{- if commandGroups .}}
{{- range $name, $commands := commandGroups .}}
  {{$name}}:{{range $commands}}
    {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}
{{- else}}
{{- range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}
{{- end}}
{{- end}}
{{- if .HasAvailableLocalFlags}}

Flags:
{{flags . | wrappedFlagUsages | trimTrailingWhitespaces}}{{end}}
{{- if hasKubeFlags .}}

Kubernetes flags:
{{kubeFlags | wrappedFlagUsages | trimTrailingWhitespaces}}{{end}}{{if true}}

Global Flags:{{range $group := globalFlagGroups}}

  {{$group.Name}}:
{{$group.Flags | wrappedFlagUsages | trimTrailingWhitespaces}}{{end}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.

For complete documentation and quick-start guides, check out our website at https://www.telepresence.io{{end}}
`)
}
