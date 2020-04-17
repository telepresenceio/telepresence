package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

const (
	socketName = "/var/run/edgectl.socket"
	logfile    = "/tmp/edgectl.log"
	apiVersion = 1
)

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

var simpleTransport = &http.Transport{
	// #nosec G402
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	Proxy:           nil,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 1 * time.Second,
		DualStack: true,
	}).DialContext,
	DisableKeepAlives: true,
}

var hClient = &http.Client{
	Transport: simpleTransport,
	Timeout:   15 * time.Second,
}

func main() {
	// Figure out our executable and save it
	if executable, err := os.Executable(); err != nil {
		fmt.Fprintf(os.Stderr, "Internal error: %v", err)
		os.Exit(1)
	} else {
		edgectl = executable
	}

	rootCmd := getRootCommand()

	var cg []CmdGroup
	if DaemonWorks() {
		cg = []CmdGroup{
			CmdGroup{
				GroupName: "Management Commands",
				CmdNames:  []string{"install", "login", "license"},
			},
			CmdGroup{
				GroupName: "Development Commands",
				CmdNames:  []string{"status", "connect", "disconnect", "intercept"},
			},
			CmdGroup{
				GroupName: "Advanced Commands",
				CmdNames:  []string{"daemon", "pause", "resume", "quit"},
			},
			CmdGroup{
				GroupName: "Other Commands",
				CmdNames:  []string{"version", "help"},
			},
		}
	} else {
		cg = []CmdGroup{
			CmdGroup{
				GroupName: "Management Commands",
				CmdNames:  []string{"install", "login", "license"},
			},
			CmdGroup{
				GroupName: "Other Commands",
				CmdNames:  []string{"version", "help"},
			},
		}
	}

	usageFunc := NewCmdUsage(rootCmd, cg)
	rootCmd.SetUsageFunc(usageFunc)
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

	myHelp := myName + `
  https://www.getambassador.io/docs/latest/topics/install/
`

	rootCmd := &cobra.Command{
		Use:          "edgectl",
		Short:        myName,
		Long:         myHelp,
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
	}
	_ = rootCmd.PersistentFlags().Bool(
		"no-report", false, "turn off anonymous crash reports and log submission on failure",
	)

	// Hidden/internal commands. These are called by Edge Control itself from
	// the correct context and execute in-place immediately.

	rootCmd.AddCommand(&cobra.Command{
		Use:    "daemon-foreground",
		Short:  "Launch Edge Control Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return RunAsDaemon(args[0], args[1])
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
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return RunAsTeleproxyIntercept(args[0], args[1])
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

	if DaemonWorks() {
		daemonCmd := &cobra.Command{
			Use:   "daemon",
			Short: "Launch Edge Control Daemon in the background (sudo)",
			Long:  daemonHelp,
			Args:  cobra.ExactArgs(0),
			RunE:  launchDaemon,
		}
		_ = daemonCmd.Flags().String(
			"dns", "",
			"DNS IP address to intercept. Defaults to the first nameserver listed in /etc/resolv.conf.",
		)
		_ = daemonCmd.Flags().String(
			"fallback", "",
			"DNS fallback, how non-cluster DNS queries are resolved. Defaults to Google DNS (8.8.8.8).",
		)
		rootCmd.AddCommand(daemonCmd)
	}
	loginCmd := &cobra.Command{
		Use:   "login [flags] HOSTNAME",
		Short: "Log in to the Ambassador Edge Policy Console",
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
	rootCmd.AddCommand(aesInstallCmd())

	// Daemon commands. These should be forwarded to the daemon.

	if DaemonWorks() {
		nilDaemon := &Daemon{}
		daemonCmd := nilDaemon.getRootCommand(nil, nil, nil)
		walkSubcommands(daemonCmd)
		rootCmd.AddCommand(daemonCmd.Commands()...)
		rootCmd.PersistentFlags().AddFlagSet(daemonCmd.PersistentFlags())
	} else {
		rootCmd.AddCommand(&cobra.Command{
			Use:   "version",
			Short: "Show program's version number and exit",
			Args:  cobra.ExactArgs(0),
			RunE: func(_ *cobra.Command, _ []string) error {
				fmt.Println("Client", displayVersion)
				fmt.Println("Daemon unavailable on this platform")
				return nil
			},
		})
	}

	rootCmd.InitDefaultHelpCmd()

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
