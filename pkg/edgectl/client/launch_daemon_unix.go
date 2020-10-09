// +build !windows

package client

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
)

// LaunchDaemon will launch the daemon responsible for doing the network overrides. Only the root
// user can do this.
func LaunchDaemon(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		// TODO: Attempt a sudo instead of reporting error
		return fmt.Errorf(`Edge Control Daemon must be launched as root.

 sudo %s
`, cmd.CommandPath())
	}
	dns, _ := cmd.Flags().GetString("dns")
	fallback, _ := cmd.Flags().GetString("fallback")
	ds, err := newDaemonState(cmd.OutOrStdout(), dns, fallback)
	defer ds.disconnect()
	if err == nil {
		return errors.New("Daemon already started")
	}
	_, err = ds.EnsureState()
	return err
}

// DaemonWorks returns whether the daemon can function on this platform
func DaemonWorks() bool {
	return true
}

const legacySocketName = "/var/run/edgectl.socket"

// quitLegacyDaemon ensures that an older version of the daemon quits and removes the old socket.
func quitLegacyDaemon(out io.Writer) {
	if !edgectl.SocketExists(legacySocketName) {
		return // no legacy daemon is running
	}
	if conn, err := net.Dial("unix", legacySocketName); err == nil {
		defer conn.Close()

		io.WriteString(conn, `{"Args": ["edgectl", "quit"], "APIVersion": 1}`)
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			fmt.Fprintf(out, "Legacy daemon: %s\n", scanner.Text())
		}
	}
}

func runAsRoot(exe string, args []string) error {
	if os.Geteuid() != 0 {
		args = append([]string{"-n", "-E", exe}, args...)
		exe = "sudo"
	}
	return start(exe, args, false, nil, nil, nil)
}

func start(exe string, args []string, wait bool, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = stdin
	var err error
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("%s %s: %v\n", exe, strings.Join(args, " "), err)
	}
	if !wait {
		return nil
	}

	// Ensure that SIGINT and SIGTERM are propagated to the child process
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		if sig == nil {
			return
		}
		_ = cmd.Process.Signal(sig)
	}()
	s, err := cmd.Process.Wait()
	if err != nil {
		return fmt.Errorf("%s %s: %v\n", exe, strings.Join(args, " "), err)
	}

	sigCh <- nil
	exitCode := s.ExitCode()
	if exitCode != 0 {
		return fmt.Errorf("%s %s: exited with %d\n", exe, strings.Join(args, " "), exitCode)
	}
	return nil
}
