package cmd

import (
	"os"
	"strconv"

	"github.com/moby/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
)

var CLIHelpDocumentationURL = "https://www.telepresence.io" //nolint:gochecknoglobals // extension point

const (
	help = `Telepresence can connect to a cluster and route all outbound traffic from your
workstation to that cluster so that software running locally can communicate
as if it executed remotely, inside the cluster. This is achieved using the
command:

telepresence connect

Telepresence can also intercept traffic intended for a specific service in a
cluster and redirect it to your local workstation:

telepresence intercept <name of service>

Telepresence uses background processes to manage the cluster session. One of
the processes runs with superuser privileges because it modifies the network.
Unless the daemons are already started, an attempt will be made to start them.
This will involve a call to sudo unless this command is run as root (not
recommended) which in turn may result in a password prompt.`

	usage = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{flags . | wrappedFlagUsages | trimTrailingWhitespaces}}{{end}}
{{- if hasKubeFlags .}}

Kubernetes flags:
{{kubeFlags | wrappedFlagUsages | trimTrailingWhitespaces}}{{end}}

Global flags:
{{globalFlags . | wrappedFlagUsages | trimTrailingWhitespaces}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.

For complete documentation and quick-start guides, check out our website at {{ getDocumentationURL }}{{end}}
`
)

func flagEqual(a, b *pflag.Flag) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Usage == b.Usage && a.Hidden == b.Hidden
}

func localFlags(cmd *cobra.Command, exclude ...*pflag.FlagSet) *pflag.FlagSet {
	ngFlags := pflag.NewFlagSet("local", pflag.ContinueOnError)
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		for _, ex := range exclude {
			if flagEqual(flag, ex.Lookup(flag.Name)) {
				return
			}
		}
		ngFlags.AddFlag(flag)
	})
	return ngFlags
}

func kubeFlags() *pflag.FlagSet {
	pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig := genericclioptions.NewConfigFlags(false)
	kubeConfig.Namespace = nil // "connect", don't take --namespace
	flags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig.AddFlags(flags)
	return flags
}

func hasKubeFlags(cmd *cobra.Command) bool {
	yep := true
	flags := cmd.Flags()
	kubeFlags().VisitAll(func(flag *pflag.Flag) {
		if yep && !flagEqual(flag, flags.Lookup(flag.Name)) {
			yep = false
		}
	})
	return yep
}

func addUsageTemplate(cmd *cobra.Command) {
	cobra.AddTemplateFunc("globalFlags", func(cmd *cobra.Command) *pflag.FlagSet { return global.Flags(hasKubeFlags(cmd)) })
	cobra.AddTemplateFunc("flags", func(cmd *cobra.Command) *pflag.FlagSet {
		return localFlags(cmd, kubeFlags(), global.Flags(hasKubeFlags(cmd)))
	})
	cobra.AddTemplateFunc("hasKubeFlags", hasKubeFlags)
	cobra.AddTemplateFunc("kubeFlags", kubeFlags)
	cobra.AddTemplateFunc("wrappedFlagUsages", func(flags *pflag.FlagSet) string {
		// This is based off of what Docker does (github.com/docker/cli/cli/cobra.go), but is
		// adjusted
		//  1. to take a pflag.FlagSet instead of a cobra.interceptCmd, so that we can have flag groups, and
		//  2. to correct for the ways that Docker upsets me.

		// Obey COLUMNS if the shell or user sets it.  (Docker doesn't do this.)
		cols, err := strconv.Atoi(os.Getenv("COLUMNS"))
		if err != nil {
			// Try to detect the size of the stdout file descriptor.  (Docker checks stdin, not stdout.)
			if ws, err := term.GetWinsize(1); err != nil {
				// If stdout is a terminal, but we were unable to get its size (I'm not sure how that can
				// happen), then fall back to assuming 80.  If stdout isn't a terminal, then we leave cols
				// as 0, meaning "don't wrap it".  (Docker wraps it even if stdout isn't a terminal.)
				if term.IsTerminal(1) {
					cols = 80
				}
			} else {
				cols = int(ws.Width)
			}
		}
		return flags.FlagUsagesWrapped(cols)
	})
	cobra.AddTemplateFunc("getDocumentationURL", func() string {
		return CLIHelpDocumentationURL
	})

	// Set a usage template that is derived from the default but replaces the "Available Commands"
	// section with the commandGroups() from the given command
	cmd.SetUsageTemplate(usage)
}
