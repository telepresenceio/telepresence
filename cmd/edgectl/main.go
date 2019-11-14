package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/pkg/errors"
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

func launchDaemon(ccmd *cobra.Command, _ []string) error {
	if os.Geteuid() != 0 {
		fmt.Println("Edge Control Daemon must be launched as root.")
		fmt.Printf("\n  sudo %s\n\n", ccmd.CommandPath())
		return errors.New("root privileges required")
	}
	fmt.Println("Launching Edge Control Daemon", displayVersion)

	cmd := exec.Command(edgectl, "daemon-foreground")
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.ExtraFiles = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err := cmd.Start()
	if err != nil {
		return errors.Wrap(err, "failed to launch the server")
	}

	success := false
	for count := 0; count < 40; count++ {
		if isServerRunning() {
			success = true
			break
		}
		if count == 4 {
			fmt.Println("Waiting for daemon to start...")
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !success {
		fmt.Println("Server did not come up!")
		fmt.Printf("Take a look at %s for more information.\n", logfile)
		return errors.New("launch failed")
	}
	return nil
}
