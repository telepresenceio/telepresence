package cli

import (
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client/auth"
	"github.com/datawire/telepresence2/pkg/client/connector"
	"github.com/datawire/telepresence2/pkg/client/daemon"
)

var help = `telepresence can run a command in a sub shell after ensuring that a connection
has been established with a Traffic Manager and optionally also that an intercept has
been added.

The command ensures that only those resources that were acquired are cleaned up. This
means that the telepresence daemon will not quit if it was already started, no disconnect
will take place if the connection was already established, and the intercept will not be
removed if it was already added.

Unless the daemon is already started, an attempt will be made to start it. This will
involve a call to sudo unless this command is run as root (not recommended).

run a command with an intercept in place:
    telepresence --intercept hello --port 9000 -- <command> arguments...
`

func statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show connectivity status",
		Args:  cobra.NoArgs,
		RunE:  status,
	}
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version",
		Args:  cobra.NoArgs,
		RunE:  printVersion,
	}
}

func quitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "quit",
		Short: "Tell telepresence daemon to quit",
		Args:  cobra.NoArgs,
		RunE:  quit,
	}
}

// Command returns the top level "telepresence" CLI command
func Command() *cobra.Command {
	myName := "Telepresence"
	if !IsServerRunning() {
		myName = "Telepresence (daemon unavailable)"
	}

	rootCmd := &cobra.Command{
		Use:          "telepresence",
		Short:        myName,
		Long:         help,
		Args:         cobra.NoArgs,
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
	}
	_ = rootCmd.PersistentFlags().Bool(
		"no-report", false, "turn off anonymous crash reports and log submission on failure",
	)

	// Hidden/internal commands. These are called by Telepresence itself from
	// the correct context and execute in-place immediately.
	rootCmd.AddCommand(daemon.Command())
	rootCmd.AddCommand(connector.Command())

	rootCmd.AddCommand(auth.LoginCommand())
	rootCmd.AddCommand(connectCommand())
	rootCmd.AddCommand(interceptCommand())
	rootCmd.AddCommand(leaveCommand())
	rootCmd.AddCommand(listCommand())
	rootCmd.AddCommand(statusCommand())
	rootCmd.AddCommand(quitCommand())
	rootCmd.AddCommand(versionCommand())
	rootCmd.InitDefaultHelpCmd()
	return rootCmd
}
