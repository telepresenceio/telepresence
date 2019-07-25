package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// adaptNoArgs adapts a no-argument function to fit Cobra's required signature
// by discarding the unnecessary arguments
func adaptNoArgs(fn func() error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		return fn()
	}
}

// unimplemented displays a message and returns
func unimplemented(cmd *cobra.Command, _ []string) error {
	fmt.Printf("%s is unimplemented...\n", cmd.CommandPath())
	return nil
}

func main() {
	rootCmd := &cobra.Command{
		Use:          "playpen",
		SilenceUsage: true, // https://github.com/spf13/cobra/issues/340
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("Running \"playpen status\". Use \"playpen help\" to get help.")
			return doStatus()
		},
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "show program's version number and exit",
		Args:  cobra.ExactArgs(0),
		RunE:  adaptNoArgs(doVersion),
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:    "server-debug",
		Short:  "launch Playpen Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		RunE:   adaptNoArgs(runAsDaemon),
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "start-server",
		Short: "launch Playpen Daemon in the background (sudo)",
		Args:  cobra.ExactArgs(0),
		RunE:  adaptNoArgs(launchDaemon),
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "show connectivity status",
		Args:  cobra.ExactArgs(0),
		RunE:  adaptNoArgs(doStatus),
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "connect [-- additional kubectl arguments...]",
		Short: "connect to a cluster",
		RunE:  doConnect,
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "disconnect",
		Short: "disconnect from the connected cluster",
		Args:  cobra.ExactArgs(0),
		RunE:  adaptNoArgs(doDisconnect),
	})
	rootCmd.AddCommand(&cobra.Command{
		Use:   "quit",
		Short: "tell Playpen Daemon to quit (for upgrades)",
		Args:  cobra.ExactArgs(0),
		RunE:  adaptNoArgs(doQuit),
	})

	interceptCmd := &cobra.Command{
		Use:   "intercept",
		Long:  "Manage deployment intercepts. An intercept arranges for a subset of requests to be diverted to the local machine.",
		Short: "manage deployment intercepts",
		RunE:  unimplemented,
	}
	interceptCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list current intercepts",
		Args:  cobra.ExactArgs(0),
		RunE:  adaptNoArgs(doListIntercepts),
	})
	interceptCmd.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "deactivate and remove an existent intercept",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			return doRemoveIntercept(name)
		},
	})
	intercept := InterceptInfo{}
	interceptAddCmd := &cobra.Command{
		Use:   "add DEPLOYMENT -t [HOST:]PORT -m HEADER=REGEX ...",
		Short: "add a deployment intercept",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			intercept.Deployment = args[0]
			if intercept.Name == "" {
				intercept.Name = fmt.Sprintf("cept-%d", time.Now().Unix())
			}

			var host, portStr string
			hp := strings.SplitN(intercept.TargetHost, ":", 2)
			if len(hp) < 2 {
				portStr = hp[0]
			} else {
				host = strings.TrimSpace(hp[0])
				portStr = hp[1]
			}
			if len(host) == 0 {
				host = "127.0.0.1"
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return errors.Wrapf(
					err,
					"Failed to parse %q as HOST:PORT",
					intercept.TargetHost,
				)
			}
			intercept.TargetHost = host
			intercept.TargetPort = port
			return doAddIntercept(&intercept)
		},
	}
	interceptAddCmd.Flags().StringVarP(&intercept.Name, "name", "n", "", "a name for this intercept")
	interceptAddCmd.Flags().StringVarP(&intercept.TargetHost, "target", "t", "", "the [HOST:]PORT to forward to")
	interceptAddCmd.MarkFlagRequired("target")
	interceptAddCmd.Flags().StringToStringVarP(&intercept.Patterns, "match", "m", nil, "match expression (HEADER=REGEX)")
	interceptAddCmd.MarkFlagRequired("match")

	interceptCmd.AddCommand(interceptAddCmd)
	rootCmd.AddCommand(interceptCmd)

	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func launchDaemon() error {
	if isServerRunning() {
		fmt.Println("It looks like the server is already running.")
		fmt.Printf("Take a look at %s for more information.\n", logfile)
		return errors.New("server is already running")
	}
	if os.Geteuid() != 0 {
		fmt.Println("Playpen Daemon must be launched as root.")
		fmt.Println("  sudo playpen start-server") // FIXME: Use cmd.Blah
		return errors.New("root privileges required")
	}
	fmt.Printf("Launching Playpen Daemon %s...\n", displayVersion)

	cmd := exec.Command(os.Args[0], "server-debug")
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
		time.Sleep(250 * time.Millisecond)
	}
	if !success {
		fmt.Println("Server did not come up!")
		fmt.Printf("Take a look at %s for more information.\n", logfile)
		return errors.New("launch failed")
	}
	return nil
}
