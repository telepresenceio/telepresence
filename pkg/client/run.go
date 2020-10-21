package client

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/common"
	"github.com/datawire/telepresence2/pkg/rpc"
)

var RunHelp = `telepresence run is a shorthand command for starting the daemon, connecting to the traffic
manager, adding an intercept, running a command, and then removing the intercept,
disconnecting, and quitting the daemon.

The command ensures that only those resources that were acquired are cleaned up. This
means that the daemon will not quit if it was already started, no disconnect will take
place if the connection was already established, and the intercept will not be removed
if it was already added.

Unless the daemon is already started, an attempt will be made to start it. This will
involve a call to sudo unless this command is run as root (not recommended).

Run a command:
    telepresence run -d hello -n example-url -t 9000 -- <command> arguments...
`

// RunInfo contains all parameters needed in order to run an intercepted command.
type RunInfo struct {
	rpc.ConnectRequest
	rpc.InterceptRequest
	DNS      string
	Fallback string
}

// RunCommand will ensure that an intercept is in place and then execute the command given by args[0]
// and the arguments starting at args[1:].
func (ri *RunInfo) RunCommand(cmd *cobra.Command, args []string) error {
	// Fail early if intercept args are inconsistent
	ri.InterceptRequest.Namespace = ri.ConnectRequest.Namespace // resolve struct ambiguity
	if err := prepareIntercept(cmd, &ri.InterceptRequest); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	return ri.runWithIntercept(out, func(is *interceptState) error {
		return start(args[0], args[1:], true, cmd.InOrStdin(), out, cmd.OutOrStderr())
	})
}

// RunShell will ensure that a daemon and a connector is started and then start a shell.
func (ri *RunInfo) RunShell(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	exe := os.Getenv("SHELL")
	return ri.runWithConnector(out, func(cs *connectorState) error {
		return start(exe, nil, true, cmd.InOrStdin(), out, cmd.OutOrStderr())
	})
}

func (ri *RunInfo) runWithDaemon(out io.Writer, f func(ds *daemonState) error) error {
	ds, err := newDaemonState(out, ri.DNS, ri.Fallback)
	if err != nil && err != daemonIsNotRunning {
		return err
	}
	return common.WithEnsuredState(ds, func() error { return f(ds) })
}

func (ri *RunInfo) runWithConnector(out io.Writer, f func(cs *connectorState) error) error {
	return ri.runWithDaemon(out, func(ds *daemonState) error {
		ri.InterceptEnabled = true
		cs, err := newConnectorState(ds.grpc, &ri.ConnectRequest, out)
		if err != nil && err != connectorIsNotRunning {
			return err
		}
		return common.WithEnsuredState(cs, func() error { return f(cs) })
	})
}

func (ri *RunInfo) runWithIntercept(out io.Writer, f func(is *interceptState) error) error {
	return ri.runWithConnector(out, func(cs *connectorState) error {
		is := newInterceptState(cs.grpc, &ri.InterceptRequest, out)
		return common.WithEnsuredState(is, func() error { return f(is) })
	})
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
	if !wait {
		// Process must live in a process group of its own to prevent
		// getting affected by <ctrl-c> in the terminal
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	var err error
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("%s %s: %v\n", exe, strings.Join(args, " "), err)
	}
	if !wait {
		_ = cmd.Process.Release()
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
