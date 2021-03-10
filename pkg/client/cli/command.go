package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/client/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon"
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

// TODO: Provide a link in the help text to more info about telepresence

func statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "status",
		Args: cobra.NoArgs,

		Short: "Show connectivity status",
		RunE:  status,
	}
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,

		Short:   "Show version",
		PreRunE: forcedUpdateCheck,
		RunE:    printVersion,
	}
}

func quitCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "quit",
		Args: cobra.NoArgs,

		Short: "Tell telepresence daemon to quit",
		RunE:  quit,
	}
}

// global options
var dnsIP string
var fallbackIP string
var mappedNamespaces []string
var kubeFlags *pflag.FlagSet

// OnlySubcommands is a cobra.PositionalArgs that is similar to cobra.NoArgs, but prints a better
// error message.
func OnlySubcommands(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		err := fmt.Errorf("invalid subcommand %q", args[0])

		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if suggestions := cmd.SuggestionsFor(args[0]); len(suggestions) > 0 {
			err = fmt.Errorf("%w\nDid you mean one of these?\n\t%s", err, strings.Join(suggestions, "\n\t"))
		}

		return cmd.FlagErrorFunc()(cmd, err)
	}
	return nil
}

// RunSubCommands is for use as a cobra.Command.RunE for commands that don't do anything themselves
// but have subcommands.  In such cases, it is important to set RunE even though there's nothing to
// run, because otherwise cobra will treat that as "success", and it shouldn't be "success" if the
// user typos a command and types something invalid.
func RunSubcommands(cmd *cobra.Command, args []string) error {
	cmd.SetOut(cmd.ErrOrStderr())
	cmd.HelpFunc()(cmd, args)
	return nil
}

// Command returns the top level "telepresence" CLI command
func Command(ctx context.Context) *cobra.Command {
	myName := "Telepresence"
	if !IsServerRunning() {
		myName = "Telepresence (daemon unavailable)"
	}

	rootCmd := &cobra.Command{
		Use:  "telepresence",
		Args: OnlySubcommands,

		Short:         myName,
		Long:          help,
		RunE:          RunSubcommands,
		SilenceErrors: true, // main() will handle it after .ExecuteContext() returns
		SilenceUsage:  true, // our FlagErrorFunc will handle it
	}

	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		if err == nil {
			return nil
		}

		// If the error is multiple lines, include an extra blank line before the "See
		// --help" line.
		errStr := strings.TrimRight(err.Error(), "\n")
		if strings.Contains(errStr, "\n") {
			errStr += "\n"
		}

		fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\nSee '%s --help'.\n", cmd.CommandPath(), errStr, cmd.CommandPath())
		os.Exit(2)
		return nil
	})

	// Hidden/internal commands. These are called by Telepresence itself from
	// the correct context and execute in-place immediately.
	rootCmd.AddCommand(daemon.Command())
	rootCmd.AddCommand(connector.Command())

	globalFlagGroups = []FlagGroup{
		{
			Name: "Kubernetes flags",
			Flags: func() *pflag.FlagSet {
				kubeFlags = pflag.NewFlagSet("", 0)
				cfgFlags := kates.NewConfigFlags(false)
				cfgFlags.Namespace = nil // some of the subcommands, like "connect", don't take --namespace
				cfgFlags.AddFlags(kubeFlags)
				return kubeFlags
			}(),
		}}

	globalFlagGroups = append(globalFlagGroups, FlagGroup{
		Name: "Telepresence networking flags",
		Flags: func() *pflag.FlagSet {
			netflags := pflag.NewFlagSet("", 0)
			// TODO: Those flags aren't applicable on a Linux with systemd.resolved configured either but
			//  that's unknown until it's been tested during the first connect attempt.
			if runtime.GOOS != "darwin" {
				netflags.StringVarP(&dnsIP,
					"dns", "", "",
					"DNS IP address to intercept locally. Defaults to the first nameserver listed in /etc/resolv.conf.",
				)
				netflags.StringVarP(&fallbackIP,
					"fallback", "", "",
					"DNS fallback, how non-cluster DNS queries are resolved. Defaults to Google DNS (8.8.8.8).",
				)
			}
			netflags.StringSliceVar(&mappedNamespaces,
				"mapped-namespaces", nil, ``+
					`Comma separated list of namespaces considered by DNS resolver and NAT for outbound connections. `+
					`Defaults to all namespaces`)

			return netflags
		}(),
	})

	globalFlagGroups = append(globalFlagGroups, FlagGroup{
		Name: "other Telepresence flags",
		Flags: func() *pflag.FlagSet {
			flags := pflag.NewFlagSet("", 0)
			flags.Bool(
				"no-report", false,
				"turn off anonymous crash reports and log submission on failure",
			)
			return flags
		}(),
	})

	rootCmd.InitDefaultHelpCmd()
	AddCommandGroups(rootCmd, []CommandGroup{
		{
			Name:     "Session Commands",
			Commands: []*cobra.Command{connectCommand(), auth.LoginCommand(), auth.LogoutCommand(), statusCommand(), quitCommand()},
		},
		{
			Name:     "Traffic Commands",
			Commands: []*cobra.Command{listCommand(), interceptCommand(ctx), leaveCommand(), previewCommand()},
		},
		{
			Name:     "Other Commands",
			Commands: []*cobra.Command{versionCommand(), uninstallCommand(), dashboardCommand()},
		},
	})
	for _, group := range globalFlagGroups {
		rootCmd.PersistentFlags().AddFlagSet(group.Flags)
	}
	return rootCmd
}
