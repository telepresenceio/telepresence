//go:build !windows

package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec" //nolint:depguard // We want no logging and no soft-context signal handling
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

const SIGTERM = unix.SIGTERM

var CommandContext = exec.CommandContext //nolint:gochecknoglobals // OS-specific function replacement

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

func startInBackgroundAsRoot(ctx context.Context, args ...string) error {
	if isAdmin() {
		return startInBackground(false, args...)
	}
	// Run sudo with a prompt explaining why root credentials are needed.
	const promptContext = "telepresence network daemon"
	err := exec.Command("sudo", append([]string{"-b", "-p", fmt.Sprintf("[sudo: %s] Password: ", promptContext)}, args...)...).Run()
	if err == nil {
		return nil
	}
	// Are we using sudo-rs?
	out, err2 := exec.Command("sudo", "--version").CombinedOutput()
	if err2 != nil || !strings.Contains(string(out), "sudo-rs") {
		// return the original error
		return err
	}
	// sudo-rs does not support the "-b" (background) option. We need it to start a shell command that backgrounds the executable
	// once the user has entered their password.
	cmdString := shellquote.ShellArgsString(slices.Insert(args, 0, "setsid")) + "</dev/null >/dev/null 2>&1 &"
	return exec.Command("sudo", "-p", promptContext, "sh", "-c", cmdString).Run()
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
