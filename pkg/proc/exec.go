package proc

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	//nolint:depguard // TODO: Switch Run() over to dexec.
	"os/exec"

	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
)

func Run(ctx context.Context, exe string, args []string, env map[string]string) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var err error
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", logging.ShellString(exe, args), err)
	}

	// Ensure that signals are propagated to the child process
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, signalsToForward...)
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

func StartInBackground(args ...string) error {
	return startInBackground(args...)
}

func StartInBackgroundAsRoot(ctx context.Context, args ...string) error {
	return startInBackgroundAsRoot(ctx, args...)
}
