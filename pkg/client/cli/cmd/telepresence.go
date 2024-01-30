package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/kubeauth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	userDaemon "github.com/telepresenceio/telepresence/v2/pkg/client/userd/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// Telepresence returns the top level "telepresence" CLI command.
func Telepresence(ctx context.Context) *cobra.Command {
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v", err)
		os.Exit(1)
	}
	ctx = client.WithConfig(ctx, cfg)
	if ctx, err = logging.InitContext(ctx, "cli", logging.RotateDaily, false); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	rootCmd := &cobra.Command{
		Use:  "telepresence",
		Args: perhapsLegacy,

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

// TelepresenceDaemon returns the top level "telepresence" CLI limited to the subcommands [kubeauth|connector|daemon]-foreground.
func TelepresenceDaemon(ctx context.Context) *cobra.Command {
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
	cmd.SetContext(ctx)
	AddSubCommands(cmd)
	return cmd
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
	addCompletion(cmd)
	cmd.InitDefaultHelpCmd()
	addUsageTemplate(cmd)
	_ = cmd.RegisterFlagCompletionFunc("context", autocompleteContext)
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
	if err := checkLegacy(cmd, args); err != nil {
		return err
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
		configCmd(), connectCmd(), currentClusterId(), gatherLogs(), gatherTraces(), genYAML(), helmCmd(),
		interceptCmd(), kubeauthCmd(), leave(), list(), listContexts(), listNamespaces(), loglevel(), quit(), statusCmd(),
		testVPN(), uninstall(), uploadTraces(), version(), listNamespaces(), listContexts(),
	)
}

func WithDaemonSubCommands(ctx context.Context) context.Context {
	return MergeSubCommands(ctx, kubeauth.Command(), userDaemon.Command(), rootd.Command())
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
	dlog.Debugf(ctx, "context completion: %q", toComplete)
	cfg, err := daemon.GetKubeStartingConfig(cmd)
	if err != nil {
		dlog.Errorf(ctx, "GetKubeStartingConfig: %v", err)
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
