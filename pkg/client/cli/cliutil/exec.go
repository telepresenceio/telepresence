package cliutil

import (
	"context"
	"fmt"
	"os"
	"strings"

	//nolint:depguard // TODO: switch this stuff over to dexec
	"os/exec"

	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
)

// Run will run the given executable with given args and env, wait for it to terminate, and return
// the result. The run will dispatch signals as appropriate for the given platform (SIGTERM and SIGINT on Unix platforms
// and os.Interrupt on Windows).
func Run(ctx context.Context, sCmd SafeCobraCommand, exe string, args []string, env map[string]string) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = sCmd.OutOrStdout()
	cmd.Stderr = sCmd.ErrOrStderr()
	cmd.Stdin = sCmd.InOrStdin()
	for k, v := range env {
		cmd.Env = append(os.Environ(), k+"="+v)
	}

	var err error
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", logging.ShellString(exe, args), err)
	}

	// Ensure that interrupt is propagated to the child process
	sigCh := signalNotifications()
	defer close(sigCh)
	go func() {
		sig := <-sigCh
		if sig == nil {
			return
		}
		_ = cmd.Process.Signal(sig)
	}()

	s, err := cmd.Process.Wait()
	if err != nil {
		return fmt.Errorf("%s: %v", logging.ShellString(exe, args), err)
	}

	exitCode := s.ExitCode()
	if exitCode != 0 {
		return fmt.Errorf("%s %s: exited with %d", exe, strings.Join(args, " "), exitCode)
	}
	return nil
}

// Background will start the given exe as a detach process.
func Background(ctx context.Context, exe string, args []string) error {
	return background(ctx, exe, args)
}

// BackgroundAsRoot will start the given exe as a detach process with elevated privileges.
func BackgroundAsRoot(ctx context.Context, exe string, args []string) error {
	return backgroundAsRoot(ctx, exe, args)
}
