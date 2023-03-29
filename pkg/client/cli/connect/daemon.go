package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func launchDaemon(ctx context.Context, cr *daemon.Request) error {
	fmt.Fprintln(output.Info(ctx), "Launching Telepresence Root Daemon")

	// Ensure that the logfile is present before the daemon starts so that it isn't created with
	// root permissions.
	logDir := filelocation.AppUserLogDir(ctx)
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
	args := []string{client.GetExe(), "daemon-foreground"}
	if cr != nil && cr.RootDaemonProfilingPort > 0 {
		args = append(args, "--pprof", strconv.Itoa(int(cr.RootDaemonProfilingPort)))
	}
	args = append(args, logDir, configDir)
	return proc.StartInBackgroundAsRoot(ctx, args...)
}

// ensureRootDaemonRunning ensures that the daemon is running.
func ensureRootDaemonRunning(ctx context.Context) error {
	if ud := daemon.GetUserClient(ctx); ud != nil && ud.Remote {
		// Never start root daemon when running remote
		return nil
	}
	cr := daemon.GetRequest(ctx)
	if cr != nil && cr.Docker {
		// Never start root daemon when connecting using a docker container.
		return nil
	}
	if addr := client.GetEnv(ctx).UserDaemonAddress; addr != "" {
		// Always assume that root daemon is running when a user daemon address is provided
		return nil
	}
	running, err := socket.IsRunning(ctx, socket.DaemonName)
	if err != nil || running {
		return err
	}
	if err = launchDaemon(ctx, cr); err != nil {
		return fmt.Errorf("failed to launch the daemon service: %w", err)
	}
	if err = socket.WaitUntilRunning(ctx, "daemon", socket.DaemonName, 10*time.Second); err != nil {
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
		if err = socket.WaitUntilVanishes("root daemon", socket.DaemonName, 5*time.Second); err != nil {
			var conn *grpc.ClientConn
			if conn, err = socket.Dial(ctx, socket.DaemonName); err == nil {
				if _, err = rpc.NewDaemonClient(conn).Quit(ctx, &empty.Empty{}); err != nil {
					err = fmt.Errorf("error when quitting root daemon: %w", err)
				}
			}
		}
	}
	return err
}

func ensureAppUserConfigDir(ctx context.Context) (string, error) {
	configDir := filelocation.AppUserConfigDir(ctx)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", errcat.NoDaemonLogs.Newf("unable to ensure that config directory %q exists: %w", configDir, err)
	}
	return configDir, nil
}
