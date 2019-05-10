package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"time"

	"github.com/spf13/cobra"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

const socketName = "/var/run/playpen.socket"
const logfile = "/tmp/playpen.log"
const apiVersion = 1

var displayVersion = fmt.Sprintf("v%s (api v%d)", Version, apiVersion)

func main() {
	rootCmd := &cobra.Command{
		Use: "playpen [command]",
		Run: func(cmd *cobra.Command, args []string) {
			doStatus()
		},
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "show program's version number and exit",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("playpen client %s\n", displayVersion)
			doVersion()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:    "server",
		Short:  "launch Playpen Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		Run: func(cmd *cobra.Command, args []string) {
			runAsDaemon()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "start-server",
		Short: "launch Playpen Daemon in the background (sudo)",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			launchDaemon()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "show connectivity status",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			doStatus()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "connect",
		Short: "connect to a cluster",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			doConnect()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "disconnect",
		Short: "disconnect from the connected cluster",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			doDisconnect()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "quit",
		Short: "tell Playpen Daemon to quit (for upgrades)",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			doQuit()
		},
	})

	err := rootCmd.Execute()
	if err != nil {
		os.Exit(2)
	}
}

func launchDaemon() {
	if isServerRunning() {
		fmt.Println("It looks like the server is already running.")
		fmt.Printf("Take a look at %s for more information.\n", logfile)
		os.Exit(1)
	}
	if os.Geteuid() != 0 {
		fmt.Println("Playpen Daemon must be launched as root.")
		fmt.Println("  sudo playpen start-server") // FIXME: Use cmd.Blah
		os.Exit(1)
	}
	fmt.Printf("Launching Playpen Daemon %s...\n", displayVersion)

	cmd := exec.Command(os.Args[0], "server")
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.ExtraFiles = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err := cmd.Start()
	if err != nil {
		fmt.Printf("Failed to launch the server: %v\n", err)
		os.Exit(1)
	}

	success := false
	for count := 0; count < 40; count++ {
		if isServerRunning() {
			success = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !success {
		fmt.Println("Server did not come up!")
		fmt.Printf("Take a look at %s for more information.\n", logfile)
		os.Exit(1)
	}
}
