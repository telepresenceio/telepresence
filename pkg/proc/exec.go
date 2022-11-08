package proc

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

type Stdio interface {
	InOrStdin() io.Reader
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}

// Start will start the given executable with given args and env,, and return the command. The signals are
// dispatched as appropriate for the given platform (SIGTERM and SIGINT on Unix platforms
// and os.Interrupt on Windows).
func Start(ctx context.Context, env map[string]string, io Stdio, exe string, args ...string) (*dexec.Cmd, error) {
	cmd := CommandContext(ctx, exe, args...)
	cmd.DisableLogging = true
	cmd.Stdout = io.OutOrStdout()
	cmd.Stderr = io.ErrOrStderr()
	cmd.Stdin = io.InOrStdin()
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", shellquote.ShellString(exe, args), err)
	}
	return cmd, nil
}

// Wait will wait for the Process of the command to finish.
// If cancel is not nil, Wait will listen for os signals and call cancel when it
// receives one.
func Wait(ctx context.Context, cancel context.CancelFunc, cmd *dexec.Cmd) error {
	p := cmd.Process
	if p == nil {
		return nil
	}

	// Ensure that appropriate signals terminates the context.
	if cancel != nil {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, SignalsToForward...)
		defer func() {
			signal.Stop(sigCh)
			close(sigCh)
		}()
		go func() {
			select {
			case <-ctx.Done():
			case sig := <-sigCh:
				if sig == nil {
					return
				}
				_ = Terminate(p)
				cancel()
			}
		}()
	}

	s, err := p.Wait()
	if err != nil {
		return fmt.Errorf("%s: %w", shellquote.ShellString(cmd.Path, cmd.Args), err)
	}

	exitCode := s.ExitCode()
	if exitCode != 0 {
		return fmt.Errorf("%s: exited with %d", shellquote.ShellString(cmd.Path, cmd.Args), exitCode)
	}
	return nil
}

// Run will run the given executable with given args and env, wait for it to terminate, and return
// the result. The run will dispatch signals as appropriate for the given platform (SIGTERM and SIGINT on Unix platforms
// and os.Interrupt on Windows).
func Run(ctx context.Context, env map[string]string, io Stdio, exe string, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd, err := Start(ctx, env, io, exe, args...)
	if err != nil {
		return err
	}
	return Wait(ctx, cancel, cmd)
}

func StartInBackground(args ...string) error {
	return startInBackground(args...)
}

func StartInBackgroundAsRoot(ctx context.Context, args ...string) error {
	return startInBackgroundAsRoot(ctx, args...)
}

func IsAdmin() bool {
	return isAdmin()
}

func Terminate(p *os.Process) error {
	return terminate(p)
}
