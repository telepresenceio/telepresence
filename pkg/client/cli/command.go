package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/telepresence2/pkg/client/auth"
	"github.com/datawire/telepresence2/pkg/client/connector"
	"github.com/datawire/telepresence2/pkg/client/daemon"
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

// global options
var dnsIP string
var fallbackIP string
var kubeFlags *pflag.FlagSet

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

	// Hidden/internal commands. These are called by Telepresence itself from
	// the correct context and execute in-place immediately.
	rootCmd.AddCommand(daemon.Command())
	rootCmd.AddCommand(connector.Command())

	globalFlagGroups = []FlagGroup{
		{
			Name: "Kubernetes flags",
			Flags: func() *pflag.FlagSet {
				kubeFlags = pflag.NewFlagSet("", 0)
				kates.NewConfigFlags(false).AddFlags(kubeFlags)
				return kubeFlags
			}(),
		},
		{
			Name: "Telepresence networking flags",
			Flags: func() *pflag.FlagSet {
				netflags := pflag.NewFlagSet("", 0)
				netflags.StringVarP(&dnsIP,
					"dns", "", "",
					"DNS IP address to intercept locally. Defaults to the first nameserver listed in /etc/resolv.conf.",
				)
				netflags.StringVarP(&fallbackIP,
					"fallback", "", "",
					"DNS fallback, how non-cluster DNS queries are resolved. Defaults to Google DNS (8.8.8.8).",
				)
				return netflags
			}(),
		},
		{
			Name: "other Telepresence flags",
			Flags: func() *pflag.FlagSet {
				flags := pflag.NewFlagSet("", 0)
				flags.Bool(
					"no-report", false,
					"turn off anonymous crash reports and log submission on failure",
				)
				return flags
			}(),
		},
	}

	rootCmd.InitDefaultHelpCmd()
	AddCommandGroups(rootCmd, []CommandGroup{
		{
			Name:     "Session Commands",
			Commands: []*cobra.Command{connectCommand(), auth.LoginCommand(), auth.LogoutCommand(), statusCommand(), quitCommand()},
		},
		{
			Name:     "Traffic Commands",
			Commands: []*cobra.Command{listCommand(), interceptCommand(), leaveCommand() /*, "preview" */},
		},
		{
			Name:     "Other Commands",
			Commands: []*cobra.Command{versionCommand(), uninstallCommand()},
		},
	})
	for _, group := range globalFlagGroups {
		rootCmd.PersistentFlags().AddFlagSet(group.Flags)
	}
	return rootCmd
}
