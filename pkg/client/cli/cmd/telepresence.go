package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/kubeauth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	userDaemon "github.com/telepresenceio/telepresence/v2/pkg/client/userd/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// Telepresence returns the top level "telepresence" CLI command.
func Telepresence(ctx context.Context, args []string) *cobra.Command {
	useMarkdown := len(args) > 0 && args[0] == "man-pages"
	longHelp := helpPlain
	if useMarkdown {
		os.Setenv("KUBECACHEDIR", "$HOME/.kube/cache")
		longHelp = helpMarkdown
	}
	rootCmd := &cobra.Command{
		Use:               "telepresence",
		Args:              OnlySubcommands,
		Short:             "Connect your workstation to a Kubernetes cluster",
		Long:              longHelp,
		PersistentPreRunE: output.SetFormat,
		RunE:              RunSubcommands,
		SilenceErrors:     true, // main() will handle it after .ExecuteContext() returns
		SilenceUsage:      true, // our FlagErrorFunc will handle it
		TraverseChildren:  true,
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	rootCmd.SetArgs(args)
	rootCmd.SetContext(ctx)
	AddSubCommands(rootCmd, useMarkdown)
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errcat.User.New(err)
	})
	return rootCmd
}

// TelepresenceDaemon returns the top level "telepresence" CLI limited to the subcommands kubeauthd, userd, and rootd.
func TelepresenceDaemon(ctx context.Context, args []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "telepresence",
		Args: OnlySubcommands,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SetOut(cmd.ErrOrStderr())
			return nil
		},
		SilenceErrors: true, // main() will handle it after .ExecuteContext() returns
		SilenceUsage:  true, // our FlagErrorFunc will handle it
	}
	cmd.SetArgs(args)
	cmd.SetContext(ctx)
	AddSubCommands(cmd, false)
	return cmd
}

func setContext(cmd *cobra.Command, ctx context.Context) {
	cmd.SetContext(ctx)
	for _, c := range cmd.Commands() {
		setContext(c, ctx)
	}
}

// AddSubCommands adds subcommands to the given command, including the default help, the commands in the
// CommandGroups found in the given command's context, and the completion command. It also replaces
// the standard usage template with a custom template.
func AddSubCommands(cmd *cobra.Command, markdown bool) {
	ctx := cmd.Context()
	commands := getSubCommands(cmd)
	for _, command := range commands {
		if ac := command.Args; ac != nil {
			// Ensure that args errors don't advice the user to look in log files
			command.Args = argsCheck(ac)
		}
		setContext(command, ctx)
	}
	cmd.AddCommand(commands...)
	if client.ProcessName() != client.RootDaemonName {
		cmd.PersistentFlags().AddFlagSet(global.Flags(ctx, false, markdown))
		addCompletion(cmd, markdown)
		addUsageTemplate(cmd, markdown)
		_ = cmd.RegisterFlagCompletionFunc("context", autocompleteContext)
	}
}

// RunSubcommands is for use as a cobra.interceptCmd.RunE for commands that don't do anything themselves
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
	return nil
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

func WithSubCommands(ctx context.Context) context.Context {
	return MergeSubCommands(ctx,
		composeCmd(),
		configCmd(),
		connectCmd(),
		curlCmd(),
		dockerRunCmd(),
		gatherLogs(),
		genYAML(),
		helmCmd(),
		ingestCmd(),
		interceptCmd(),
		kubeauthCmd(),
		leaveCmd(),
		list(),
		listContexts(),
		revokeCmd(),
		listNamespaces(),
		loglevel(),
		manPages(),
		mcp(),
		quit(),
		replaceCmd(),
		serveCmd(),
		statusCmd(),
		uninstall(),
		versionCmd(),
		wiretapCmd(),
	)
}

func WithDaemonSubCommands(ctx context.Context) context.Context {
	return MergeSubCommands(ctx, kubeauth.Command(ctx), userDaemon.Command(ctx), rootd.Command(ctx))
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

func autocompleteContext(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	ctx := cmd.Context()
	clog.Debugf(ctx, "context completion: %q", toComplete)
	cfg, err := daemon.GetKubeStartingConfig(cmd)
	if err != nil {
		clog.Errorf(ctx, "GetKubeStartingConfig: %v", err)
		return nil, cobra.ShellCompDirectiveError
	}
	cxl := cfg.Contexts
	nss := make([]string, len(cxl))
	i := 0
	for n := range cxl {
		nss[i] = n
		i++
	}
	return nss, cobra.ShellCompDirectiveNoFileComp
}
