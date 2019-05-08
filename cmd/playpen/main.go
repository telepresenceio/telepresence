package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is inserted at build using --ldflags -X
var Version = "(unknown version)"

const socketName = "/tmp/playpen.sock"
const logfile = "/tmp/playpen.log"
const apiVersion = 1

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
			fmt.Printf("playpen client v%s\n", Version)
			doVersion()
		},
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "server",
		Short: "launch Playpen Daemon",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			runAsDaemon()
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
