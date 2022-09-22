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

// IsCommand returns true if the string is encountered before a '--' or end of list. This
// is a best effort, and it might give us false positives.
func IsCommand(s string) bool {
	prev := ""
	for _, arg := range os.Args[1:] {
		if arg == "--" {
			break
		}
		if arg == s {
			// Do a best effort to rule out that this is a flag argument
			if strings.HasPrefix(prev, "--") {
				if prev == "--mapped-namespaces" {
					continue
				}
				// all kubernetes flags take an argument
				if kubeFlags.Lookup(strings.TrimPrefix(prev, "--")) != nil {
					continue
				}
			}
			if prev == "-s" {
				continue
			}
			return true
		}
		prev = arg
	}
	return false
}

func userWantsRootLevelHelp() bool {
	if len(os.Args) <= 1 {
		return true
	}
	arg := os.Args[1]
	return arg == "help" || arg == "--help" || arg == "-h"
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
	rootCmd.SetContext(ctx)

	static := cliutil.CommandGroups{
		"Session Commands": []*cobra.Command{connectCommand(), LoginCommand(), LogoutCommand(), LicenseCommand(), statusCommand(), quitCommand()},
		"Traffic Commands": []*cobra.Command{listCommand(), leaveCommand(), previewCommand()},
		"Install Commands": []*cobra.Command{helmCommand(), uninstallCommand()},
		"Debug Commands":   []*cobra.Command{loglevelCommand(), gatherLogsCommand()},
		"Other Commands":   []*cobra.Command{versionCommand(), dashboardCommand(), ClusterIdCommand(), genYAMLCommand(), vpnDiagCommand()},
	}

	var groups = make(cliutil.CommandGroups)
	if !IsCommand("quit") && !userWantsRootLevelHelp() {
		// These are commands that known to always exist in the user daemon. If the daemon
		// isn't running, it will be started just to retrieve the command spec.
		wellknownRemoteCommands := []string{
			"intercept",
			"gather-traces",
			"upload-traces",
		}

		var err error
		wellKnown := false
		for _, w := range wellknownRemoteCommands {
			if IsCommand(w) {
				wellKnown = true
				break
			}
		}
		if groups, err = getRemoteCommands(ctx, rootCmd, wellKnown); err != nil {
			if err == cliutil.ErrNoUserDaemon {
				// This is not a problem if the command is known to the CLI
				for _, g := range static {
					for _, c := range g {
						if IsCommand(c.Name()) {
							err = nil
						}
					}
				}
			} else if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(1)
			}
		}
	}

	rootCmd.InitDefaultHelpCmd()
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

	addCompletionCommand(rootCmd)

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
	opf := cmd.PreRunE
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		// Allow deprecated global flags so that scripts using them don't break, but print
		// a warning that their values are ignored.
		deprecatedGlobalFlags.VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"use of global flag '--%s' is deprecated and its value is ignored\n", f.Name)
			}
		})
		if opf != nil {
			return opf(cmd, args)
		}
		return nil
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

	globalFlagGroups = GlobalFlagGroups()
}

func GlobalFlagGroups() []cliutil.FlagGroup {
	return []cliutil.FlagGroup{{
		Name: "other Telepresence flags",
		Flags: func() *pflag.FlagSet {
			flags := pflag.NewFlagSet("", 0)
			flags.Bool(
				"no-report", false,
				"turn off anonymous crash reports and log submission on failure",
			)
			flags.String(
				"output", "default",
				"set the output format, supported values are 'json' and 'default'",
			)
			return flags
		}(),
	}}
}
