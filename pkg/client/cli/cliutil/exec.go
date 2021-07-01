package cliutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	//nolint:depguard // TODO: switch this stuff over to dexec
	"os/exec"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
)

func EnvPairs(env map[string]string) []string {
	pairs := make([]string, len(env))
	i := 0
	for k, v := range env {
		pairs[i] = k + "=" + v
		i++
	}
	return pairs
}

func Start(ctx context.Context, exe string, args []string, wait bool, stdin io.Reader, stdout, stderr io.Writer, env ...string) error {
	if !wait {
		// The context should not kill it if cancelled
		ctx = dcontext.WithoutCancel(ctx)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
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
		return fmt.Errorf("%s: %w", logging.ShellString(exe, args), err)
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
		return fmt.Errorf("%s: %w", logging.ShellString(exe, args), err)
	}

	sigCh <- nil
	exitCode := s.ExitCode()
	if exitCode != 0 {
		return fmt.Errorf("%s %s: exited with %d", exe, strings.Join(args, " "), exitCode)
	}
	return nil
}
