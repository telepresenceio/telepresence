package cliutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

var ErrNoNetwork = errors.New("telepresence network is not established")

func launchDaemon(ctx context.Context) error {
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

	return proc.StartInBackgroundAsRoot(ctx, client.GetExe(), "daemon-foreground", logDir, configDir)
}

// WithNetwork (1) ensures that the daemon is running, (2) establishes a connection to it, and (3)
// runs the given function with that connection.
//
// Nested calls to WithNetwork will reuse the outer connection.
func WithNetwork(ctx context.Context, fn func(context.Context, daemon.DaemonClient) error) error {
	return withNetwork(ctx, true, fn)
}

// WithStartedNetwork is like WithNetwork, but returns ErrNoNetwork if the daemon is not already
// running, rather than starting it.
func WithStartedNetwork(ctx context.Context, fn func(context.Context, daemon.DaemonClient) error) error {
	return withNetwork(ctx, false, fn)
}

func withNetwork(ctx context.Context, maybeStart bool, fn func(context.Context, daemon.DaemonClient) error) error {
	type daemonConnCtxKey struct{}
	if untyped := ctx.Value(daemonConnCtxKey{}); untyped != nil {
		conn := untyped.(*grpc.ClientConn)
		daemonClient := daemon.NewDaemonClient(conn)
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
			err = ErrNoNetwork
			if maybeStart {
				if err = launchDaemon(ctx); err != nil {
					return fmt.Errorf("failed to launch the daemon service: %w", err)
				}

				if err = client.WaitUntilSocketAppears("daemon", client.DaemonSocketName, 10*time.Second); err != nil {
					return fmt.Errorf("daemon service did not start: %w", err)
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

	daemonClient := daemon.NewDaemonClient(conn)
	if !started {
		// Ensure that the already running daemon has the correct version
		if err := versionCheck(ctx, "Root", "", daemonClient); err != nil {
			return err
		}
	}

	return fn(ctx, daemonClient)
}

type quitting struct{}

// Disconnect shuts down a session in the root daemon. When it shuts down, it will tell the connector to shut down.
func Disconnect(ctx context.Context, quitUserDaemon, quitRootDaemon bool) (err error) {
	ctx = context.WithValue(ctx, quitting{}, true)
	defer func() {
		// Ensure the connector is killed even if daemon isn't running.  If the daemon already
		// shut down the connector, then this is a no-op.
		if cerr := UserDaemonDisconnect(ctx, quitUserDaemon); !(cerr == nil || errors.Is(err, ErrNoUserDaemon)) {
			if err == nil {
				err = cerr
			} else {
				fmt.Fprintf(os.Stderr, "Error when quitting connector: %v\n", cerr)
			}
		}
		if err == nil && quitRootDaemon {
			err = client.WaitUntilSocketVanishes("root daemon", client.DaemonSocketName, 5*time.Second)
		}
	}()
	fmt.Print("Telepresence Network ")
	err = WithStartedNetwork(ctx, func(ctx context.Context, daemonClient daemon.DaemonClient) (err error) {
		defer func() {
			if err == nil {
				fmt.Println("done")
			}
		}()
		if quitRootDaemon {
			fmt.Print("quitting...")
		} else {
			var ds *daemon.DaemonStatus
			if ds, err = daemonClient.Status(ctx, &empty.Empty{}); err != nil {
				return err
			}
			if ds.OutboundConfig == nil {
				return ErrNoNetwork
			}
			fmt.Print("disconnecting...")
			if _, err = daemonClient.Disconnect(ctx, &empty.Empty{}); status.Code(err) != codes.Unimplemented {
				// nil or not unimplemented
				return err
			}
			// Disconnect is not implemented so daemon predates 2.4.9. Force a quit
		}
		_, err = daemonClient.Quit(ctx, &empty.Empty{})
		return err
	})
	if errors.Is(err, ErrNoNetwork) {
		if quitRootDaemon {
			fmt.Println("had already quit")
		} else {
			fmt.Println("is already disconnected")
		}
		err = nil
	}
	return err
}
