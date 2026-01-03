package connect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func launchDaemon(ctx context.Context, cr *daemon.Request) (info *daemon.RootInfo, err error) {
	logFile := filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log")
	logFile, err = logging.ValidateLogFilePath(logFile)
	if err != nil {
		return nil, err
	}
	// Ensure that the logfile is present before the daemon starts so that it isn't created with
	// root permissions.
	if _, err = os.Stat(logFile); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, errcat.NoDaemonLogs.New(err)
		}
		fh, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, errcat.NoDaemonLogs.New(err)
		}
		_ = fh.Close()
	}
	cacheDir := filelocation.AppUserCacheDir(ctx)
	daemonDir := filepath.Join(cacheDir, "rootd")
	_ = os.MkdirAll(daemonDir, 0o777)

	fp, err := ioutil.FreePortsTCP(1)
	if err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}
	info = &daemon.RootInfo{DaemonPort: fp[0].Port()}
	err = daemon.NewRootInfoLoader(ctx, false).SaveInfo(info, daemon.InfoFileName)
	if err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}

	addr := fmt.Sprintf(":%d", info.DaemonPort)
	args := []string{client.GetExe(ctx), client.RootDaemonName, "--cache", cacheDir, "--config", client.GetConfigFile(ctx), "--logfile", logFile, "--address", addr}
	if cr != nil && cr.RootDaemonProfilingPort > 0 {
		args = append(args, "--pprof", strconv.Itoa(int(cr.RootDaemonProfilingPort)))
	}
	return info, proc.StartInBackgroundAsRoot(ctx, args...)
}

// EnsureRootDaemonRunning ensures that the daemon is running.
func EnsureRootDaemonRunning(ctx context.Context) error {
	cr := daemon.GetRequest(ctx)
	if cr != nil && cr.Docker {
		// Never start root daemon when connecting using a docker container.
		return nil
	}
	if addr := client.GetEnv(ctx).UserDaemonAddress; addr != "" {
		// Always assume that root daemon is running when a user daemon address is provided
		return nil
	}

	_, err := daemon.LoadRootServiceInfo(ctx)
	if err == nil {
		// Root daemon is running as a managed service.
		return nil
	}

	il := daemon.NewRootInfoLoader(ctx, false)
	_, err = il.LoadInfo(daemon.InfoFileName)
	if err != nil {
		_, err = launchDaemon(ctx, cr)
		if err != nil {
			return fmt.Errorf("failed to launch the daemon service: %w", err)
		}
	}

	// Wait for the root daemon to be ready
	var conn *grpc.ClientConn
	conn, err = il.DialDaemon(ctx, true)
	if err == nil {
		_ = conn.Close()
	}
	return err
}

func mkdir(dirType, path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errcat.NoDaemonLogs.Errorf(err, "unable to ensure that %s directory %q exists", dirType, path)
	}
	return nil
}

func ensureAppUserCacheDirs(ctx context.Context) error {
	cacheDir := filelocation.AppUserCacheDir(ctx)
	if err := mkdir("cache", filepath.Join(cacheDir, "daemons")); err != nil {
		return err
	}
	if err := mkdir("cache", filepath.Join(cacheDir, "sessions")); err != nil {
		return err
	}
	return nil
}

func ensureAppUserConfigDir(ctx context.Context) error {
	configDir := filelocation.AppUserConfigDir(ctx)
	err := mkdir("config", configDir)
	if err != nil {
		return err
	}
	_, err = client.InstallID(ctx)
	return err
}
