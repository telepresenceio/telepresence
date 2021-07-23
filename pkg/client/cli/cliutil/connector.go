package cliutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	//nolint:depguard // Because we won't ever .Wait() for the process and we'd turn off
	// logging, using dexec would just be extra overhead.
	"os/exec"

	"google.golang.org/grpc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

var ErrNoConnector = errors.New("telepresence user daemon is not running")

func launchConnector() error {
	fmt.Println("Launching Telepresence User Daemon")
	args := []string{client.GetExe(), "connector-foreground"}

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

// WithConnector (1) ensures that the connector is running, (2) establishes a connection to it, and
// (3) runs the given function with that connection.
//
// It streams to stdout any messages that the connector wants us to display to the user (which
// WithConnector listens for via the UserNotifications gRPC call).  WithConnector does NOT make the
// "Connect" gRPC call or any other gRPC call except for UserNotifications.
//
// Nested calls to WithConnector will reuse the outer connection.
func WithConnector(ctx context.Context, fn func(context.Context, connector.ConnectorClient) error) error {
	return withConnector(ctx, true, fn)
}

// WithStartedConnector is like WithConnector, but returns ErrNoConnector if the connector is not
// already running, rather than starting it.
func WithStartedConnector(ctx context.Context, fn func(context.Context, connector.ConnectorClient) error) error {
	return withConnector(ctx, false, fn)
}

type connectorConnCtxKey struct{}
type connectorStartedCtxKey struct{}

func withConnector(ctx context.Context, maybeStart bool, fn func(context.Context, connector.ConnectorClient) error) error {
	if untyped := ctx.Value(connectorConnCtxKey{}); untyped != nil {
		conn := untyped.(*grpc.ClientConn)
		connectorClient := connector.NewConnectorClient(conn)
		return fn(ctx, connectorClient)
	}

	var conn *grpc.ClientConn
	started := false
	for {
		var err error
		conn, err = client.DialSocket(ctx, client.ConnectorSocketName)
		if err == nil {
			break
		}
		if errors.Is(err, os.ErrNotExist) {
			err = ErrNoConnector
			if maybeStart {
				if err := launchConnector(); err != nil {
					return fmt.Errorf("failed to launch the connector service: %w", err)
				}

				if err := client.WaitUntilSocketAppears("connector", client.ConnectorSocketName, 10*time.Second); err != nil {
					logDir, _ := filelocation.AppUserLogDir(ctx)
					return fmt.Errorf("connector service did not start (see %q for more info)", filepath.Join(logDir, "connector.log"))
				}

				maybeStart = false
				started = true
				continue
			}
		}
		return err
	}
	defer conn.Close()
	ctx = context.WithValue(ctx, connectorConnCtxKey{}, conn)
	ctx = context.WithValue(ctx, connectorStartedCtxKey{}, started)
	connectorClient := connector.NewConnectorClient(conn)

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		ShutdownOnNonError: true,
		DisableLogging:     true,
	})

	grp.Go("stdio", func(ctx context.Context) error {
		stream, err := connectorClient.UserNotifications(ctx, &empty.Empty{})
		if err != nil {
			return err
		}
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				if grpcStatus.Code(err) == grpcCodes.Canceled {
					return nil
				}
				return err
			}
			fmt.Println(strings.TrimRight(msg.Message, "\n"))
		}
	})
	grp.Go("main", func(ctx context.Context) error {
		return fn(ctx, connectorClient)
	})

	return grp.Wait()
}

// DidLaunchConnector returns whether WithConnector launched the connector or merely connected to a
// running instance.  If there are nested calls to WithConnector, it returns the answer for the
// inner-most call; even if the outer-most call launches the connector false will be returned.
func DidLaunchConnector(ctx context.Context) bool {
	launched, _ := ctx.Value(connectorStartedCtxKey{}).(bool)
	return launched
}

func QuitConnector(ctx context.Context) error {
	err := WithStartedConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		fmt.Print("Telepresence User Daemon quitting...")
		_, err := connectorClient.Quit(ctx, &empty.Empty{})
		return err
	})
	if err == nil {
		err = client.WaitUntilSocketVanishes("connector", client.ConnectorSocketName, 5*time.Second)
	}
	if err != nil {
		if errors.Is(err, ErrNoConnector) {
			fmt.Println("Telepresence User Daemon is already stopped")
			return nil
		}
		return err
	}
	fmt.Println(" done")
	return nil
}
