package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec" //nolint:depguard // We want no logging and no soft-context signal handling
	"os/signal"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

// Start will start the given executable with given args and env, and return the command. The signals are
// dispatched as appropriate for the given platform (SIGTERM and SIGINT on Unix platforms
// and os.Interrupt on Windows).
func Start(ctx context.Context, env map[string]string, exe string, args ...string) (*dexec.Cmd, error) {
	cmd := CommandContext(ctx, exe, args...)
	cmd.DisableLogging = true
	cmd.Stdout = dos.Stdout(ctx)
	cmd.Stderr = dos.Stderr(ctx)
	cmd.Stdin = dos.Stdin(ctx)
	cmd.Env = dos.Environ(ctx)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	dlog.Debug(ctx, shellquote.ShellString(exe, args))
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

// CreateNewProcessGroup ensures that the process uses a process group of its own to prevent
// it getting affected by <ctrl-c> in the terminal.
func CreateNewProcessGroup(cmd *dexec.Cmd) {
	createNewProcessGroup(cmd.Cmd)
}

func KillProcessGroup(ctx context.Context, cmd *exec.Cmd, signal os.Signal) {
	killProcessGroup(ctx, cmd, signal)
}

// Run will run the given executable with given args and env, wait for it to terminate, and return
// the result. The run will dispatch signals as appropriate for the given platform (SIGTERM and SIGINT on Unix platforms
// and os.Interrupt on Windows).
func Run(ctx context.Context, env map[string]string, exe string, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd, err := Start(ctx, env, exe, args...)
	if err != nil {
		return err
	}
	return Wait(ctx, cancel, cmd)
}

func StartInBackground(includeEnv bool, args ...string) error {
	return startInBackground(includeEnv, args...)
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

// CacheAdmin will ensure that the current process is able to invoke subprocesses with admin rights
// without having to ask for the password again. This is needed among other things to make sure the
// integration tests can see that a password is being asked for.
func CacheAdmin(ctx context.Context, prompt string) error {
	// These logs will get picked up by the test-reporter to make sure the user is asked for the password.
	dlog.Info(ctx, "Asking for admin credentials")
	defer dlog.Info(ctx, "Admin credentials acquired")
	return cacheAdmin(ctx, prompt)
}
