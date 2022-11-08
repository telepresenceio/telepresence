package util

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func launchDaemon(ctx context.Context) error {
	fmt.Fprintln(output.Info(ctx), "Launching Telepresence Root Daemon")

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
		if err = os.MkdirAll(logDir, 0o700); err != nil {
			return err
		}
		fh, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_ = fh.Close()
	}

	configDir, err := ensureAppUserConfigDir(ctx)
	if err != nil {
		return err
	}
	return proc.StartInBackgroundAsRoot(ctx, client.GetExe(), "daemon-foreground", logDir, configDir)
}

// EnsureRootDaemonRunning ensures that the daemon is running.
func EnsureRootDaemonRunning(ctx context.Context) error {
	running, err := client.IsRunning(ctx, client.DaemonSocketName)
	if err != nil || running {
		return err
	}
	if err = launchDaemon(ctx); err != nil {
		return fmt.Errorf("failed to launch the daemon service: %w", err)
	}
	if err = client.WaitUntilRunning(ctx, "daemon", client.DaemonSocketName, 10*time.Second); err != nil {
		return fmt.Errorf("daemon service did not start: %w", err)
	}
	return nil
}

// Disconnect shuts down a session in the root daemon. When it shuts down, it will tell the connector to shut down.
func Disconnect(ctx context.Context, quitDaemons bool) error {
	err := UserDaemonDisconnect(ctx, quitDaemons)
	if errors.Is(err, ErrNoUserDaemon) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("error when quitting connector: %w", err)
	}
	if quitDaemons {
		// User daemon is responsible for killing the root daemon, but we kill it here too to cater for
		// the fact that the user daemon might have been killed ungracefully.
		if err = client.WaitUntilSocketVanishes("root daemon", client.DaemonSocketName, 5*time.Second); err != nil {
			var conn *grpc.ClientConn
			if conn, err = client.DialSocket(ctx, client.DaemonSocketName); err == nil {
				if _, err = daemon.NewDaemonClient(conn).Quit(ctx, &empty.Empty{}); err != nil {
					err = fmt.Errorf("error when quitting root daemon: %w", err)
				}
			}
		}
	}
	return err
}

func ensureAppUserConfigDir(ctx context.Context) (string, error) {
	configDir, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return "", errcat.NoDaemonLogs.New(err)
	}
	if err = os.MkdirAll(configDir, 0o700); err != nil {
		return "", errcat.NoDaemonLogs.Newf("unable to ensure that config directory %q exists: %w", configDir, err)
	}
	return configDir, nil
}
