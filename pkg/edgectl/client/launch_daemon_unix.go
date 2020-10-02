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
	"time"

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
	started, err := ensureDaemonRunning(dns, fallback)
	if err != nil {
		return err
	}
	if !started {
		return errors.New("Daemon already started")
	}
	return nil
}

func ensureDaemonRunning(dns, fallback string) (bool, error) {
	quitLegacyDaemon()

	if assertDaemonStarted() == nil {
		// Daemon is already running
		return false, nil
	}

	fmt.Println("Launching Edge Control Daemon", edgectl.DisplayVersion())

	err := runAsRoot(edgectl.GetExe(), []string{"daemon-foreground", dns, fallback})
	if err != nil {
		return false, errors.Wrap(err, "failed to launch the server")
	}

	for count := 0; count < 40; count++ {
		if IsServerRunning() {
			return true, nil
		}
		if count == 4 {
			fmt.Println("Waiting for daemon to start...")
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false, fmt.Errorf("Daemon service did not come up!\nTake a look at %s for more information.", edgectl.Logfile)
}

// DaemonWorks returns whether the daemon can function on this platform
func DaemonWorks() bool {
	return true
}

const legacySocketName = "/var/run/edgectl.socket"

// quitLegacyDaemon ensures that an older version of the daemon quits and removes the old socket.
func quitLegacyDaemon() {
	if !edgectl.SocketExists(legacySocketName) {
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
