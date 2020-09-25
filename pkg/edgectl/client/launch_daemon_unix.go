// +build !windows

package client

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
)

// LaunchDaemon will launch the daemon responsible for doing the intercepts. Only the root
// user can do this.
func LaunchDaemon(ccmd *cobra.Command, _ []string) error {
	if os.Geteuid() != 0 {
		// TODO: Attempt a sudo instead of reporting error
		fmt.Println("Edge Control Daemon must be launched as root.")
		fmt.Printf("\n  sudo %s\n\n", ccmd.CommandPath())
		return errors.New("root privileges required")
	}
	quitLegacyDaemon()

	fmt.Println("Launching Edge Control Daemon", edgectl.DisplayVersion())

	dns, _ := ccmd.Flags().GetString("dns")
	fallback, _ := ccmd.Flags().GetString("fallback")

	cmd := exec.Command(edgectl.GetExe(), "daemon-foreground", dns, fallback)
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
		if IsServerRunning() {
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
		fmt.Printf("Take a look at %s for more information.\n", edgectl.Logfile)
		return errors.New("launch failed")
	}
	return nil
}

// DaemonWorks returns whether the daemon can function on this platform
func DaemonWorks() bool {
	return true
}

const legacySocketName = "/var/run/edgectl.socket"

// quitLegacyDaemon ensures that an older version of the daemon quits and remove the old socket.
func quitLegacyDaemon() {
	_, err := os.Stat(legacySocketName)
	if pe, ok := err.(*os.PathError); ok && os.IsNotExist(pe) {
		return // no legacy daemon is running
	}
	if conn, err := net.Dial("unix", legacySocketName); err == nil {
		defer conn.Close()

		io.WriteString(conn, `{"Args": ["edgectl", "quit"], "APIVersion": 1}`)
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			fmt.Printf("Legacy daemon: %s\n", scanner.Text())
		}
	}
}
