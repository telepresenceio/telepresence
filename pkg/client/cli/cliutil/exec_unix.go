// +build !windows

package cliutil

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	//nolint:depguard // Because we won't ever .Wait() for the process and we'd turn off
	// logging, using dexec would just be extra overhead.
	"os/exec"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func background(ctx context.Context, exe string, args []string) error {
	// The context should not kill it if cancelled
	ctx = dcontext.WithoutCancel(ctx)
	cmd := exec.CommandContext(ctx, exe, args...)

	// Ensure that the processes uses a process group of its own to prevent
	// it getting affected by <ctrl-c> in the terminal
	cmd.SysProcAttr = &unix.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", logging.ShellString(exe, args), err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("%s: %w", logging.ShellString(exe, args), err)
	}
	return nil
}

func backgroundAsRoot(ctx context.Context, exe string, args []string) error {
	if !proc.IsAdmin() {
		// If we're going to be prompting for the `sudo` password, we want to first provide
		// the user with some info about exactly what we're prompting for.  We don't want to
		// use `sudo`'s `--prompt` flag for this because (1) we don't want it to be
		// re-displayed if they typo their password, and (2) it might be ignored anyway
		// depending on `passprompt_override` in `/etc/sudoers`.  So we'll do a pre-flight
		// `sudo --non-interactive true` to decide whether to display it.
		needPwCmd := dexec.CommandContext(ctx, "sudo", "--non-interactive", "true")
		needPwCmd.DisableLogging = true
		if err := needPwCmd.Run(); err != nil {
			fmt.Printf("Need root privileges to run: %s\n", logging.ShellString(exe, args))
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
		args = append([]string{"--non-interactive", "--preserve-env", exe}, args...)
		exe = "sudo"
	}
	return Background(ctx, exe, args)
}

func signalNotifications() chan os.Signal {
	// Ensure that SIGINT and SIGTERM are propagated to the child process
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, unix.SIGINT, unix.SIGTERM)
	return sigCh
}
