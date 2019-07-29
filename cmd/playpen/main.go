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

const socketName = "/var/run/playpen.socket"
const logfile = "/tmp/playpen.log"
const apiVersion = 1

var displayVersion = fmt.Sprintf("v%s (api v%d)", Version, apiVersion)

func main() {
	// Let the daemon take care of everything if possible
	failedToConnectErr := mainViaDaemon()

	// Couldn't reach the daemon. Try to handle things locally.
	// ...

	rootCmd := &cobra.Command{
		Use:          "playpen",
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
		Args:         cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return failedToConnectErr
		},
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "show program's version number and exit",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("Client", displayVersion)
			fmt.Println()
			return failedToConnectErr
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:    "daemon-foreground",
		Short:  "launch Playpen Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return RunAsDaemon()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "daemon",
		Short: "launch Playpen Daemon in the background (sudo)",
		Args:  cobra.ExactArgs(0),
		RunE:  launchDaemon,
	})

	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func launchDaemon(ccmd *cobra.Command, _ []string) error {
	if os.Geteuid() != 0 {
		fmt.Println("Playpen Daemon must be launched as root.")
		fmt.Printf("  sudo %s\n", ccmd.CommandPath())
		return errors.New("root privileges required")
	}
	fmt.Println("Launching Playpen Daemon", displayVersion)

	cmd := exec.Command(os.Args[0], "daemon-foreground")
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
