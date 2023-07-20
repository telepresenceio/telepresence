//go:build !windows
// +build !windows

package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec" //nolint:depguard // We want no logging and no soft-context signal handling

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

const SIGTERM = unix.SIGTERM

var CommandContext = dexec.CommandContext //nolint:gochecknoglobals // OS-specific function replacement

var SignalsToForward = []os.Signal{unix.SIGINT, unix.SIGTERM} //nolint:gochecknoglobals // OS-specific constant list

func isAdmin() bool {
	return os.Geteuid() == 0
}

func startInBackground(includeEnv bool, args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	if includeEnv {
		cmd.Env = os.Environ()
	}

	createNewProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", shellquote.ShellString(args[0], args[1:]), err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("%s: %w", shellquote.ShellString(args[0], args[1:]), err)
	}

	return nil
}

func cacheAdmin(ctx context.Context, prompt string) error {
	// If we're going to be prompting for the `sudo` password, we want to first provide
	// the user with some info about exactly what we're prompting for.  We don't want to
	// use `sudo`'s `--prompt` flag for this because (1) we don't want it to be
	// re-displayed if they typo their password, and (2) it might be ignored anyway
	// depending on `passprompt_override` in `/etc/sudoers`.  So we'll do a pre-flight
	// `sudo --non-interactive true` to decide whether to display it.
	//
	// Note: Using `sudo --non-interactive --validate` does not work well in situations
	// where the user has configured `myuser ALL=(ALL:ALL) NOPASSWD: ALL` in the sudoers
	// file. Hence the use of `sudo --non-interactive true`. A plausible cause can be
	// found in the first comment here:
	// https://unix.stackexchange.com/questions/50584/why-sudo-timestamp-is-not-updated-when-nopasswd-is-set
	needPwCmd := dexec.CommandContext(ctx, "sudo", "--non-interactive", "true")
	needPwCmd.DisableLogging = true
	if err := needPwCmd.Run(); err != nil {
		if prompt != "" {
			fmt.Println(prompt)
		}
		pwCmd := dexec.CommandContext(ctx, "sudo", "true")
		pwCmd.DisableLogging = true
		if err := pwCmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func startInBackgroundAsRoot(ctx context.Context, args ...string) error {
	if !isAdmin() {
		// `sudo` won't be able to read the password from the terminal when we run
		// it with Setpgid=true, so do a pre-flight `sudo true` (i.e. cacheAdmin) to read the
		// password, and then enforce that being re-used by passing
		// `--non-interactive`.
		prompt := fmt.Sprintf("Need root privileges to run: %s", shellquote.ShellString(args[0], args[1:]))
		if err := CacheAdmin(ctx, prompt); err != nil {
			return err
		}
		args = append([]string{"sudo", "--non-interactive"}, args...)
	}

	return startInBackground(false, args...)
}

func terminate(p *os.Process) error {
	// SIGTERM makes it through a PTY, SIGINT doesn't. Not sure why that is.
	// thallgren
	return p.Signal(unix.SIGTERM)
}

func createNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &unix.SysProcAttr{
		Setpgid: true,
	}
}

func killProcessGroup(_ context.Context, cmd *exec.Cmd, signal os.Signal) {
	if p := cmd.Process; p != nil {
		_ = unix.Kill(-p.Pid, signal.(unix.Signal))
	}
}
