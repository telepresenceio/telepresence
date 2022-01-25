package cliutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

var ErrNoUserDaemon = errors.New("telepresence user daemon is not running")
var ErrNoTrafficManager = errors.New("telepresence traffic manager is not connected")

// WithConnector (1) ensures that the connector is running, (2) establishes a connection to it, and
// (3) runs the given function with that connection.
//
// It streams to stdout any messages that the connector wants us to display to the user (which
// WithConnector listens for via the UserNotifications gRPC call).  WithConnector does NOT make the
// "Connect" gRPC call or any other gRPC call except for UserNotifications.
//
// Nested calls to WithConnector will reuse the outer connection.
func WithConnector(ctx context.Context, fn func(context.Context, connector.ConnectorClient) error) error {
	return withConnector(ctx, true, true, fn)
}

// WithStartedConnector is like WithConnector, but returns ErrNoUserDaemon if the connector is not
// already running, rather than starting it.
func WithStartedConnector(ctx context.Context, withNotify bool, fn func(context.Context, connector.ConnectorClient) error) error {
	return withConnector(ctx, false, withNotify, fn)
}

type connectorConnCtxKey struct{}

func withConnector(ctx context.Context, maybeStart bool, withNotify bool, fn func(context.Context, connector.ConnectorClient) error) error {
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
			err = ErrNoUserDaemon
			if maybeStart {
				fmt.Println("Launching Telepresence User Daemon")
				if err = proc.StartInBackground("/Users/josecortes/go/src/github.com/datawire/telepresence2-proprietary/telepresence-pro", "connector-foreground"); err != nil {
					return fmt.Errorf("failed to launch the connector service: %w", err)
				}

				if err = client.WaitUntilSocketAppears("connector", client.ConnectorSocketName, 10*time.Second); err != nil {
					return fmt.Errorf("connector service did not start: %w", err)
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
	connectorClient := connector.NewConnectorClient(conn)
	if !started {
		// Ensure that the already running daemon has the correct version
		if err := versionCheck(ctx, "User", connectorClient); err != nil {
			return err
		}
	}
	if !withNotify {
		return fn(ctx, connectorClient)
	}

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

func UserDaemonDisconnect(ctx context.Context, quitUserDaemon bool) error {
	fmt.Print("Telepresence Traffic Manager ")
	err := WithStartedConnector(ctx, false, func(ctx context.Context, connectorClient connector.ConnectorClient) (err error) {
		defer func() {
			if err == nil {
				fmt.Println("done")
			}
		}()
		if quitUserDaemon {
			fmt.Print("quitting...")
		} else {
			var ci *connector.ConnectInfo
			if ci, err = connectorClient.Status(ctx, &empty.Empty{}); err != nil {
				return err
			}
			if ci.Error == connector.ConnectInfo_DISCONNECTED {
				return ErrNoUserDaemon
			}
			fmt.Print("disconnecting...")
			if _, err = connectorClient.Disconnect(ctx, &empty.Empty{}); status.Code(err) != codes.Unimplemented {
				// nil or not unimplemented
				return err
			}
			// Disconnect is not implemented so daemon predates 2.4.9. Force a quit
		}
		if _, err = connectorClient.Quit(ctx, &empty.Empty{}); err == nil || grpcStatus.Code(err) == grpcCodes.Unavailable {
			err = client.WaitUntilSocketVanishes("user daemon", client.ConnectorSocketName, 5*time.Second)
		}
		return err
	})
	if err != nil && (errors.Is(err, ErrNoUserDaemon) || grpcStatus.Code(err) == grpcCodes.Unavailable) {
		if quitUserDaemon {
			fmt.Println("had already quit")
		} else {
			fmt.Println("is already disconnected")
		}
		err = nil
	}
	return err
}
