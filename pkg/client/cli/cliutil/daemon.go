package cliutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	//nolint:depguard // Because we won't ever .Wait() for the process and we'd turn off
	// logging, using dexec would just be extra overhead.
	"os/exec"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

var ErrNoDaemon = errors.New("telepresence root daemon is not running")

func launchDaemon(ctx context.Context, dnsIP string) error {
	fmt.Println("Launching Telepresence Root Daemon")

	// Ensure that the logfile is present before the daemon starts so that it isn't created with
	// root permissions.
	logDir, err := filelocation.AppUserLogDir(ctx)
	if err != nil {
		return err
	}
	logFile := filepath.Join(logDir, "daemon.log")
	if _, err := os.Stat(logFile); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err = os.MkdirAll(logDir, 0700); err != nil {
			return err
		}
		fh, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_ = fh.Close()
	}

	configDir, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return err
	}

	args := []string{client.GetExe(), "daemon-foreground", logDir, configDir, dnsIP}
	if os.Geteuid() != 0 {
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
			fmt.Printf("Need root privileges to run: %s\n", logging.ShellString(args[0], args[1:]))
			// `sudo` won't be able to read the password from the terminal when we run
			// it with Setpgid=true, so do a pre-flight `sudo --validate` to read the
			// password, and then enforce that being re-used by passing
			// `--non-interactive`.
			pwCmd := dexec.CommandContext(ctx, "sudo", "--validate")
			pwCmd.DisableLogging = true
			if err := pwCmd.Run(); err != nil {
				return err
			}
		}
		args = append([]string{"sudo", "--non-interactive", "--preserve-env"}, args...)
	}

	cmd := exec.Command(args[0], args[1:]...)
	// Process must live in a process group of its own to prevent
	// getting affected by <ctrl-c> in the terminal
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", logging.ShellString(args[0], args[1:]), err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("%s: %w", logging.ShellString(args[0], args[1:]), err)
	}

	return nil
}

// WithDaemon (1) ensures that the daemon is running, (2) establishes a connection to it, and (3)
// runs the given function with that connection.
//
// Nested calls to WithDaemon will reuse the outer connection.
func WithDaemon(ctx context.Context, dnsIP string, fn func(context.Context, daemon.DaemonClient) error) error {
	return withDaemon(ctx, true, dnsIP, fn)
}

// WithStartedDaemon is like WithDaemon, but returns ErrNoDaemon if the daemon is not already
// running, rather than starting it.
func WithStartedDaemon(ctx context.Context, fn func(context.Context, daemon.DaemonClient) error) error {
	return withDaemon(ctx, false, "", fn)
}

type daemonStartedCtxKey struct{}

func withDaemon(ctx context.Context, maybeStart bool, dnsIP string, fn func(context.Context, daemon.DaemonClient) error) error {
	type daemonConnCtxKey struct{}
	if untyped := ctx.Value(daemonConnCtxKey{}); untyped != nil {
		conn := untyped.(*grpc.ClientConn)
		daemonClient := daemon.NewDaemonClient(conn)
		if ctx.Value(daemonStartedCtxKey{}).(bool) {
			ctx = context.WithValue(ctx, daemonStartedCtxKey{}, false)
		}
		return fn(ctx, daemonClient)
	}

	var conn *grpc.ClientConn
	started := false
	for {
		var err error
		conn, err = client.DialSocket(ctx, client.DaemonSocketName)
		if err == nil {
			break
		}
		if errors.Is(err, os.ErrNotExist) {
			err = ErrNoDaemon
			if maybeStart {
				if err := launchDaemon(ctx, dnsIP); err != nil {
					return fmt.Errorf("failed to launch the daemon service: %w", err)
				}

				if err := client.WaitUntilSocketAppears("daemon", client.DaemonSocketName, 10*time.Second); err != nil {
					logDir, _ := filelocation.AppUserLogDir(ctx)
					return fmt.Errorf("daemon service did not start (see %q for more info)", filepath.Join(logDir, "daemon.log"))
				}

				maybeStart = false
				started = true
				continue
			}
		}
		return err
	}
	defer conn.Close()
	ctx = context.WithValue(ctx, daemonConnCtxKey{}, conn)
	ctx = context.WithValue(ctx, daemonStartedCtxKey{}, started)
	daemonClient := daemon.NewDaemonClient(conn)

	return fn(ctx, daemonClient)
}

// DidLaunchDaemon returns whether WithDaemon launched the daemon or merely connected to a running
// instance.  If there are nested calls to WithDaemon, it returns the answer for the inner-most
// call; even if the outer-most call launches the daemon false will be returned.
func DidLaunchDaemon(ctx context.Context) bool {
	launched, _ := ctx.Value(daemonStartedCtxKey{}).(bool)
	return launched
}

func QuitDaemon(ctx context.Context) error {
	err := WithStartedDaemon(ctx, func(ctx context.Context, daemonClient daemon.DaemonClient) error {
		fmt.Print("Telepresence Root Daemon quitting...")
		_, err := daemonClient.Quit(ctx, &empty.Empty{})
		return err
	})
	if err == nil {
		err = client.WaitUntilSocketVanishes("daemon", client.DaemonSocketName, 5*time.Second)
	}
	if err != nil {
		if errors.Is(err, ErrNoDaemon) {
			fmt.Println("Telepresence Root Daemon is already stopped")
			return nil
		}
		return err
	}
	fmt.Println(" done")
	return nil
}
