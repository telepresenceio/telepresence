package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

const socketName = "/var/run/edgectl.socket"
const logfile = "/tmp/edgectl.log"
const apiVersion = 1

var displayVersion = fmt.Sprintf("v%s (api v%d)", Version, apiVersion)

const failedToConnect = "Unable to connect to the daemon (See \"edgectl help daemon\")"

var daemonHelp = `The Edge Control Daemon is a long-lived background component that manages
connections and network state.

Launch the Edge Control Daemon:
    sudo edgectl daemon

Examine the Daemon's log output in
    ` + logfile + `
to troubleshoot problems.
`

// edgectl is the full path to the Edge Control binary
var edgectl string

/*
Future command help layout

Edge Stack Commands:
  login             Access the Ambassador Edge Stack admin UI
  license           Set or update the Ambassador Edge Stack license key

Cluster Commands:
  status            Show connectivity status
  connect           Connect to a cluster
  disconnect        Disconnect from the connected cluster
  intercept         Manage deployment intercepts

Daemon Commands:
  daemon            Launch Edge Control Daemon in the background (sudo)
  pause             Turn off network overrides (to use a VPN)
  resume            Turn network overrides on (after using edgectl pause)
  quit              Tell Edge Control Daemon to quit (for upgrades)

Other Commands:
  version           Show program's version number and exit
  help              Help about any command

https://github.com/kubernetes/kubernetes/blob/master/pkg/kubectl/cmd/cmd.go#L487

 */

func main() {
	// Figure out our executable and save it
	if executable, err := os.Executable(); err != nil {
		fmt.Fprintf(os.Stderr, "Internal error: %v", err)
		os.Exit(1)
	} else {
		edgectl = executable
	}

	rootCmd := getRootCommand()
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func getRootCommand() *cobra.Command {
	myName := "Edge Control"
	if !isServerRunning() {
		myName = "Edge Control (daemon unavailable)"
	}

	rootCmd := &cobra.Command{
		Use:          "edgectl",
		Short:        myName,
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
	}

	// Hidden/internal commands. These are called by Edge Control itself from
	// the correct context and execute in-place immediately.

	rootCmd.AddCommand(&cobra.Command{
		Use:    "daemon-foreground",
		Short:  "Launch Edge Control Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return RunAsDaemon()
		},
	})
	teleproxyCmd := &cobra.Command{
		Use:    "teleproxy",
		Short:  "Impersonate Teleproxy (for internal use)",
		Hidden: true,
	}
	teleproxyCmd.AddCommand(&cobra.Command{
		Use:    "intercept",
		Short:  "Impersonate Teleproxy Intercept (for internal use)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return RunAsTeleproxyIntercept()
		},
	})
	teleproxyCmd.AddCommand(&cobra.Command{
		Use:    "bridge",
		Short:  "Impersonate Teleproxy Bridge (for internal use)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return RunAsTeleproxyBridge(args[0], args[1])
		},
	})
	rootCmd.AddCommand(teleproxyCmd)

	// Client commands. These are never sent to the daemon.

	rootCmd.AddCommand(&cobra.Command{
		Use:   "daemon",
		Short: "Launch Edge Control Daemon in the background (sudo)",
		Long:  daemonHelp,
		Args:  cobra.ExactArgs(0),
		RunE:  launchDaemon,
	})
	loginCmd := &cobra.Command{
		Use:   "login [flags] HOSTNAME",
		Short: "Access the Ambassador Edge Stack admin UI",
		Args:  cobra.ExactArgs(1),
		RunE:  aesLogin,
	}
	_ = loginCmd.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = loginCmd.Flags().StringP(
		"namespace", "n", "ambassador",
		"The Kubernetes namespace to use. Defaults to ambassador.",
	)
	_ = loginCmd.Flags().Bool("url", false, "Just show the URL (don't launch a browser)")
	_ = loginCmd.Flags().Bool("token", false, "Also display the login token")
	rootCmd.AddCommand(loginCmd)
	licenseCmd := &cobra.Command{
		Use:   "license [flags] LICENSE_KEY",
		Short: "Set or update the Ambassador Edge Stack license key",
		Args:  cobra.ExactArgs(1),
		RunE:  aesLicense,
	}
	_ = licenseCmd.Flags().StringP(
		"context", "c", "",
		"The Kubernetes context to use. Defaults to the current kubectl context.",
	)
	_ = licenseCmd.Flags().StringP(
		"namespace", "n", "ambassador",
		"The Kubernetes namespace to use. Defaults to ambassador.",
	)
	rootCmd.AddCommand(licenseCmd)

	// Daemon commands. These should be forwarded to the daemon.

	nilDaemon := &Daemon{}
	daemonCmd := nilDaemon.getRootCommand(nil, nil, nil)
	walkSubcommands(daemonCmd)
	rootCmd.AddCommand(daemonCmd.Commands()...)
	rootCmd.PersistentFlags().AddFlagSet(daemonCmd.PersistentFlags())

	return rootCmd
}

func walkSubcommands(cmd *cobra.Command) {
	for _, subCmd := range cmd.Commands() {
		walkSubcommands(subCmd)
	}
	if cmd.RunE != nil {
		cmd.RunE = forwardToDaemon
	}
}

func forwardToDaemon(cmd *cobra.Command, _ []string) error {
	err := mainViaDaemon()
	if err != nil {
		// The version command is special because it must emit the client
		// version if the daemon is unavailable.
		if cmd.Use == "version" {
			fmt.Println("Client", displayVersion)
		}
		fmt.Println(failedToConnect)
	}
	return err
}

