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

var failedToConnect = `Failed to connect to the daemon. Is it still running?
The daemon's log output in ` + logfile + ` may have more information.
Start the daemon using "sudo edgectl daemon" if it is not running.
`
var displayVersion = fmt.Sprintf("v%s (api v%d)", Version, apiVersion)
var edgectl string

func main() {
	// Figure out our executable and save it
	func() {
		var err error
		edgectl, err = os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Internal error: %v", err)
			os.Exit(1)
		}
	}()

	// Notice early if we're impersonating Teleproxy
	if len(os.Args) == 3 && os.Args[1] == "teleproxy" {
		func() {
			var err error
			switch os.Args[2] {
			case "intercept":
				err = RunAsTeleproxyIntercept()
			case "bridge":
				err = RunAsTeleproxyBridge()
			default:
				return // Allow normal CLI error handling to proceed
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v", err)
				os.Exit(1)
			}
			os.Exit(0)
		}()
	}

	// Let the daemon take care of everything if possible
	failedToConnectErr := mainViaDaemon()

	// Couldn't reach the daemon. Try to handle things locally.
	// ...

	rootCmd := &cobra.Command{
		Use:          "edgectl",
		Short:        "Edge Control (daemon unavailable)",
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
		Args:         cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println(failedToConnect)
			return failedToConnectErr
		},
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Show program's version number and exit",
		Args:  cobra.ExactArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println(failedToConnect)
			fmt.Println("Client", displayVersion)
			fmt.Println()
			return failedToConnectErr
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:    "daemon-foreground",
		Short:  "Launch Edge Control Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return RunAsDaemon()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "daemon",
		Short: "Launch Edge Control Daemon in the background (sudo)",
		Args:  cobra.ExactArgs(0),
		RunE:  launchDaemon,
	})
	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + "\n" + failedToConnect)

	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
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
