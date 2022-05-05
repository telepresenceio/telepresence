package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

var help = `Telepresence can connect to a cluster and route all outbound traffic from your
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

// OnlySubcommands is a cobra.PositionalArgs that is similar to cobra.NoArgs, but prints a better
// error message.
func OnlySubcommands(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
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

// PerhapsLegacyCommands is like OnlySubcommands but performs some initial check for legacy flags
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

// RunSubcommands is for use as a cobra.Command.RunE for commands that don't do anything themselves
// but have subcommands.  In such cases, it is important to set RunE even though there's nothing to
// run, because otherwise cobra will treat that as "success", and it shouldn't be "success" if the
// user typos a command and types something invalid.
func RunSubcommands(cmd *cobra.Command, args []string) error {
	cmd.SetOut(cmd.ErrOrStderr())

	// determine if --help was explicitly asked for
	var usedHelpFlag bool
	for _, arg := range args {
		if arg == "--help" {
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

// Command returns the top level "telepresence" CLI command
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

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if cmd.RunE == nil {
			return
		}

		var output Output
		cmd.RunE = output.RunE(cmd.RunE)
	}

	var groups cliutil.CommandGroups
	if len(os.Args) > 1 && os.Args[1] == "quit" {
		groups = make(cliutil.CommandGroups)
	} else {
		var err error
		if groups, err = getRemoteCommands(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	}

	rootCmd.InitDefaultHelpCmd()
	static := cliutil.CommandGroups{
		"Session Commands": []*cobra.Command{connectCommand(), LoginCommand(), LogoutCommand(), LicenseCommand(), statusCommand(), quitCommand()},
		"Traffic Commands": []*cobra.Command{listCommand(), interceptCommand(ctx), leaveCommand(), previewCommand()},
		"Debug Commands":   []*cobra.Command{loglevelCommand(), gatherLogsCommand()},
		"Other Commands":   []*cobra.Command{versionCommand(), uninstallCommand(), dashboardCommand(), ClusterIdCommand(), genYAMLCommand(), vpnDiagCommand()},
	}
	for name, cmds := range static {
		if _, ok := groups[name]; !ok {
			groups[name] = []*cobra.Command{}
		}
		groups[name] = append(groups[name], cmds...)
	}

	AddCommandGroups(rootCmd, groups)
	initGlobalFlagGroups()
	for _, commands := range groups {
		for _, command := range commands {
			if ac := command.Args; ac != nil {
				// Ensure that args errors don't advice the user to look in log files
				command.Args = argsCheck(ac)
			}
			initDeprecatedPersistentFlags(command)
		}
	}
	for _, group := range globalFlagGroups {
		rootCmd.PersistentFlags().AddFlagSet(group.Flags)
	}
	return rootCmd
}

// argsCheck wraps an PositionalArgs checker in a function that wraps a potential error
// using errcat.User
func argsCheck(f cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := f(cmd, args); err != nil {
			return errcat.User.New(err)
		}
		return nil
	}
}

func initDeprecatedPersistentFlags(cmd *cobra.Command) {
	cmd.Flags().AddFlagSet(deprecatedGlobalFlags)
	opf := cmd.PostRun
	cmd.PostRun = func(cmd *cobra.Command, args []string) {
		// Allow deprecated global flags so that scripts using them don't break, but print
		// a warning that their values are ignored.
		deprecatedGlobalFlags.VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"use of global flag '--%s' is deprecated and its value is ignored\n", f.Name)
			}
		})
		if opf != nil {
			opf(cmd, args)
		}
	}
}

func initGlobalFlagGroups() {
	deprecatedGlobalFlags = pflag.NewFlagSet("deprecated global flags", 0)

	kubeFlags := pflag.NewFlagSet("", 0)
	genericclioptions.NewConfigFlags(false).AddFlags(kubeFlags)
	deprecatedGlobalFlags.AddFlagSet(kubeFlags)

	netflags := pflag.NewFlagSet("", 0)
	netflags.StringP("dns", "", "", "")
	netflags.StringSlice("mapped-namespaces", nil, "")

	deprecatedGlobalFlags.AddFlagSet(netflags)
	deprecatedGlobalFlags.VisitAll(func(flag *pflag.Flag) {
		flag.Hidden = true
	})

	globalFlagGroups = []cliutil.FlagGroup{{
		Name: "other Telepresence flags",
		Flags: func() *pflag.FlagSet {
			flags := pflag.NewFlagSet("", 0)
			flags.Bool(
				"no-report", false,
				"turn off anonymous crash reports and log submission on failure",
			)
			flags.String(
				"output", "default",
				"set the output format",
			)
			return flags
		}(),
	}}
}
