package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	manager "github.com/datawire/telepresence2/pkg/rpc"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
)

// runner contains all parameters needed in order to run an intercepted command.
type runner struct {
	connector.ConnectRequest
	manager.CreateInterceptRequest
	DNS             string
	Fallback        string
	RemoveIntercept string
	List            bool
	NoWait          bool
	Quit            bool
	Status          bool
	Version         bool
}

// run will ensure that an intercept is in place and then execute the command given by args[0]
// and the arguments starting at args[1:].
func (p *runner) run(cmd *cobra.Command, args []string) error {
	switch {
	case p.List:
		return listIntercepts(cmd, []string{})
	case p.Quit:
		return quit(cmd, []string{})
	case p.Status:
		return status(cmd, []string{})
	case p.RemoveIntercept != "":
		return removeIntercept(cmd, []string{p.RemoveIntercept})
	case p.Version:
		return printVersion(cmd, []string{})
	}

	doWithResource := func(context string) error {
		switch {
		case p.NoWait:
			return nil
		case len(args) == 0:
			return p.startSubshell(cmd, context)
		default:
			return start(args[0], args[1:], true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		}
	}

	if p.CreateInterceptRequest.InterceptSpec.Name != "" {
		if err := prepareIntercept(&p.CreateInterceptRequest); err != nil {
			return err
		}
		return p.runWithIntercept(cmd, func(is *interceptState) error { return doWithResource(is.cs.info.ClusterContext) })
	}
	return p.runWithConnector(cmd, func(cs *connectorState) error { return doWithResource(cs.info.ClusterContext) })
}

func (p *runner) startSubshell(cmd *cobra.Command, ctx string) error {
	exe := os.Getenv("SHELL")
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Starting a %s subshell\n", exe)
	return start(exe, []string{"i"}, true, cmd.InOrStdin(), out, cmd.ErrOrStderr())
}

func (p *runner) runWithDaemon(cmd *cobra.Command, f func(ds *daemonState) error) error {
	ds, err := newDaemonState(cmd, p.DNS, p.Fallback)
	if err != nil && err != errDaemonIsNotRunning {
		return err
	}
	return client.WithEnsuredState(ds, p.NoWait, func() error { return f(ds) })
}

func (p *runner) runWithConnector(cmd *cobra.Command, f func(cs *connectorState) error) error {
	return p.runWithDaemon(cmd, func(ds *daemonState) error {
		p.InterceptEnabled = true
		cs, err := newConnectorState(ds.grpc, &p.ConnectRequest, cmd)
		if err != nil && err != errConnectorIsNotRunning {
			return err
		}
		return client.WithEnsuredState(cs, p.NoWait, func() error { return f(cs) })
	})
}

func (p *runner) runWithIntercept(cmd *cobra.Command, f func(is *interceptState) error) error {
	return p.runWithConnector(cmd, func(cs *connectorState) error {
		is := newInterceptState(cs, &p.CreateInterceptRequest, cmd)
		return client.WithEnsuredState(is, p.NoWait, func() error { return f(is) })
	})
}

func runAsRoot(exe string, args []string) error {
	if os.Geteuid() != 0 {
		if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
			fmt.Printf("Need root privileges to run %q\n", client.ShellString(exe, args))
			if err = exec.Command("sudo", "true").Run(); err != nil {
				return err
			}
		}
		args = append([]string{"-n", "-E", exe}, args...)
		exe = "sudo"
	}
	return start(exe, args, false, nil, nil, nil)
}

func start(exe string, args []string, wait bool, stdin io.Reader, stdout, stderr io.Writer, env ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = stdin
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if !wait {
		// Process must live in a process group of its own to prevent
		// getting affected by <ctrl-c> in the terminal
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	var err error
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("%s: %v", client.ShellString(exe, args), err)
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
		return fmt.Errorf("%s: %v", client.ShellString(exe, args), err)
	}

	sigCh <- nil
	exitCode := s.ExitCode()
	if exitCode != 0 {
		return fmt.Errorf("%s %s: exited with %d", exe, strings.Join(args, " "), exitCode)
	}
	return nil
}
