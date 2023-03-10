package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/moby/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

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

For complete documentation and quick-start guides, check out our website at https://www.telepresence.io{{end}}
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
	cobra.AddTemplateFunc("flags", func(cmd *cobra.Command) *pflag.FlagSet { return localFlags(cmd, kubeFlags(), global.Flags(false)) })
	cobra.AddTemplateFunc("hasKubeFlags", hasKubeFlags)
	cobra.AddTemplateFunc("kubeFlags", kubeFlags)
	cobra.AddTemplateFunc("wrappedFlagUsages", func(flags *pflag.FlagSet) string {
		// This is based off of what Docker does (github.com/docker/cli/cli/cobra.go), but is
		// adjusted
		//  1. to take a pflag.FlagSet instead of a cobra.Command, so that we can have flag groups, and
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

	// Set a usage template that is derived from the default but replaces the "Available Commands"
	// section with the commandGroups() from the given command
	cmd.SetUsageTemplate(usage)
}

// OnlySubcommands is a cobra.PositionalArgs that is similar to cobra.NoArgs, but prints a better
// error message.
func OnlySubcommands(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if args[0] == "-h" {
		return nil
	}
	err := fmt.Errorf("invalid subcommand %q", args[0])
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	if suggestions := cmd.SuggestionsFor(args[0]); len(suggestions) > 0 {
		err = fmt.Errorf("%w\nDid you mean one of these?\n\t%s", err, strings.Join(suggestions, "\n\t"))
	}
	return cmd.FlagErrorFunc()(cmd, err)
}

// PerhapsLegacyCommands is like OnlySubcommands but performs some initial check for legacy flags.
func PerhapsLegacyCommands(cmd *cobra.Command, args []string) error {
	// If a user is using a flag that is coming from telepresence 1, we try to
	// construct the tp2 command based on their input. If the args passed to
	// telepresence are one of the flags we recognize, we don't want to error
	// out here.
	tp1Flags := []string{"--swap-deployment", "-s", "--run", "--run-shell", "--docker-run", "--help"}
	for _, v := range args {
		for _, flag := range tp1Flags {
			if v == flag {
				return nil
			}
		}
	}
	return OnlySubcommands(cmd, args)
}

// AddSubCommands adds subcommands to the given command, including the default help, the commands in the
// CommandGroups found in the given command's context, and the completion command. It also replaces
// the standard usage template with a custom template.
func AddSubCommands(cmd *cobra.Command) {
	ctx := cmd.Context()
	commands := getSubCommands(cmd)
	for _, command := range commands {
		if ac := command.Args; ac != nil {
			// Ensure that args errors don't advice the user to look in log files
			command.Args = argsCheck(ac)
		}
		command.SetContext(ctx)
	}
	cmd.AddCommand(commands...)
	cmd.PersistentFlags().AddFlagSet(global.Flags(false))
	addCompletionCommand(cmd)
	cmd.InitDefaultHelpCmd()
	addUsageTemplate(cmd)
}

// RunSubcommands is for use as a cobra.Command.RunE for commands that don't do anything themselves
// but have subcommands.  In such cases, it is important to set RunE even though there's nothing to
// run, because otherwise cobra will treat that as "success", and it shouldn't be "success" if the
// user typos a command and types something invalid.
func RunSubcommands(cmd *cobra.Command, args []string) error {
	// determine if --help was explicitly asked for
	var usedHelpFlag bool
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			usedHelpFlag = true
		}
	}
	// If there are no args or --help was used, then it's not a legacy
	// Telepresence command so we return the help text
	if len(args) == 0 || usedHelpFlag {
		cmd.HelpFunc()(cmd, args)
		return nil
	}
	if err := checkLegacyCmd(cmd, args); err != nil {
		return err
	}
	return nil
}

// Command returns the top level "telepresence" CLI command.
func Command(ctx context.Context) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:  "telepresence",
		Args: PerhapsLegacyCommands,

		Short:              "Connect your workstation to a Kubernetes cluster",
		Long:               help,
		RunE:               RunSubcommands,
		SilenceErrors:      true, // main() will handle it after .ExecuteContext() returns
		SilenceUsage:       true, // our FlagErrorFunc will handle it
		DisableFlagParsing: true, // Bc of the legacyCommand parsing, see legacy_command.go
	}
	rootCmd.SetContext(ctx)
	AddSubCommands(rootCmd)
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errcat.User.New(err)
	})
	return rootCmd
}

func WithSubCommands(ctx context.Context) context.Context {
	return MergeSubCommands(ctx,
		connectCommand(), statusCommand(), quitCommand(),
		listCommand(), intercept.LeaveCommand(), intercept.Command(),
		helmCommand(), uninstallCommand(),
		loglevelCommand(), gatherLogsCommand(),
		GatherTracesCommand(), PushTracesCommand(),
		versionCommand(), ClusterIdCommand(), genYAMLCommand(), vpnDiagCommand(),
		configCommand(),
	)
}

// argsCheck wraps an PositionalArgs checker in a function that wraps a potential error
// using errcat.User.
func argsCheck(f cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := f(cmd, args); err != nil {
			return errcat.User.New(err)
		}
		return nil
	}
}

type subCommandsKey struct{}

func MergeSubCommands(ctx context.Context, commands ...*cobra.Command) context.Context {
	if ecs, ok := ctx.Value(subCommandsKey{}).(*[]*cobra.Command); ok {
		*ecs = mergeCommands(*ecs, commands)
	} else {
		ctx = context.WithValue(ctx, subCommandsKey{}, &commands)
	}
	return ctx
}

func getSubCommands(cmd *cobra.Command) []*cobra.Command {
	if gs, ok := cmd.Context().Value(subCommandsKey{}).(*[]*cobra.Command); ok {
		return *gs
	}
	return nil
}

// mergeCommands merges the command slice b into a, replacing commands using the same name
// and returns the resulting slice.
func mergeCommands(a, b []*cobra.Command) []*cobra.Command {
	ac := make(map[string]*cobra.Command, len(a)+len(b))
	for _, c := range a {
		ac[c.Name()] = c
	}
	for _, c := range b {
		ac[c.Name()] = c
	}
	return maps.ToSortedSlice(ac)
}
