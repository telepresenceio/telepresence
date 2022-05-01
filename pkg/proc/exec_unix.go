//go:build !windows
// +build !windows

package proc

import (
	"context"
	"fmt"
	"os"

	//nolint:depguard // Because startInBackground{,AsRoot}() won't ever .Wait() for the process
	// and we'd turn off logging, using dexec would just be extra overhead.
	"os/exec"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

var CommandContext = dexec.CommandContext

var signalsToForward = []os.Signal{unix.SIGINT, unix.SIGTERM}

func isAdmin() bool {
	return os.Geteuid() == 0
}

func startInBackground(args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)

	// Ensure that the processes uses a process group of its own to prevent
	// it getting affected by <ctrl-c> in the terminal
	cmd.SysProcAttr = &unix.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", shellquote.ShellString(args[0], args[1:]), err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("%s: %w", shellquote.ShellString(args[0], args[1:]), err)
	}

	return nil
}

func startInBackgroundAsRoot(ctx context.Context, args ...string) error {
	if !isAdmin() {
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
			fmt.Printf("Need root privileges to run: %s\n", shellquote.ShellString(args[0], args[1:]))
			// `sudo` won't be able to read the password from the terminal when we run
			// it with Setpgid=true, so do a pre-flight `sudo true` to read the
			// password, and then enforce that being re-used by passing
			// `--non-interactive`.
			pwCmd := dexec.CommandContext(ctx, "sudo", "true")
			pwCmd.DisableLogging = true
			if err := pwCmd.Run(); err != nil {
				return err
			}
		}
		args = append([]string{"sudo", "--non-interactive"}, args...)
	}

	return startInBackground(args...)
}
