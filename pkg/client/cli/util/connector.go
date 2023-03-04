package util

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

var (
	ErrNoUserDaemon     = errors.New("telepresence user daemon is not running")
	ErrNoRootDaemon     = errors.New("telepresence root daemon is not running")
	ErrNoTrafficManager = errors.New("telepresence traffic manager is not connected")
)

func UserDaemonDisconnect(ctx context.Context, quitDaemons bool) (err error) {
	stdout := output.Out(ctx)
	fmt.Fprint(stdout, "Telepresence Daemons ")
	ud := GetUserDaemon(ctx)
	if ud == nil {
		fmt.Fprintln(stdout, "have already quit")
		return ErrNoUserDaemon
	}
	defer func() {
		if err == nil {
			fmt.Fprintln(stdout, "done")
		}
	}()

	if quitDaemons {
		fmt.Fprint(stdout, "quitting...")
	} else {
		fmt.Fprint(stdout, "disconnecting...")
		if _, err = ud.Disconnect(ctx, &emptypb.Empty{}); status.Code(err) != codes.Unimplemented {
			// nil or not unimplemented
			return err
		}
		// Disconnect is not implemented so daemon predates 2.4.9. Force a quit
	}
	if _, err = ud.Quit(ctx, &emptypb.Empty{}); err == nil || status.Code(err) == codes.Unavailable {
		err = socket.WaitUntilVanishes("user daemon", socket.ConnectorName, 5*time.Second)
	}
	if err != nil && status.Code(err) == codes.Unavailable {
		if quitDaemons {
			fmt.Fprintln(stdout, "have already quit")
		} else {
			fmt.Fprintln(stdout, "are already disconnected")
		}
		err = nil
	}
	return err
}

func RunConnect(cmd *cobra.Command, args []string) error {
	if err := InitCommand(cmd); err != nil {
		return err
	}
	if len(args) == 0 {
		return nil
	}
	ctx := cmd.Context()
	if GetSession(ctx).Started {
		defer func() {
			_ = Disconnect(ctx, false)
		}()
	}
	return proc.Run(dos.WithStdio(ctx, cmd), nil, args[0], args[1:]...)
}

func launchConnectorDaemon(ctx context.Context, connectorDaemon string, required bool) (*UserDaemon, error) {
	cr := connect.GetRequest(ctx)
	conn, err := socket.Dial(ctx, socket.ConnectorName)
	if err == nil {
		if cr != nil && cr.Docker {
			return nil, errcat.User.New("option --docker cannot be used as long as a daemon is running on the host. Try telepresence quit -s")
		}
		return newUserDaemon(conn, false), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Check if a running daemon can be discovered.
	name, err := daemonName(ctx)
	if err != nil {
		return nil, err
	}
	conn, err = docker.DiscoverDaemon(ctx, name)
	if err == nil {
		return newUserDaemon(conn, true), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if cr != nil && cr.Docker {
		if required {
			conn, err = docker.LaunchDaemon(ctx, name, cr.KubeFlags["KUBECONFIG"])
			if err != nil {
				return nil, err
			}
			return newUserDaemon(conn, true), nil
		}
		return nil, ErrNoUserDaemon
	}

	if !required {
		return nil, ErrNoUserDaemon
	}

	fmt.Fprintln(output.Info(ctx), "Launching Telepresence User Daemon")
	if _, err = ensureAppUserConfigDir(ctx); err != nil {
		return nil, err
	}
	if err = proc.StartInBackground(connectorDaemon, "connector-foreground"); err != nil {
		return nil, fmt.Errorf("failed to launch the connector service: %w", err)
	}
	if err = socket.WaitUntilAppears("connector", socket.ConnectorName, 10*time.Second); err != nil {
		return nil, fmt.Errorf("connector service did not start: %w", err)
	}
	conn, err = socket.Dial(ctx, socket.ConnectorName)
	if err != nil {
		return nil, err
	}
	return newUserDaemon(conn, false), nil
}

func daemonName(ctx context.Context) (string, error) {
	var flags map[string]string
	if cr := connect.GetRequest(ctx); cr != nil {
		flags = cr.KubeFlags
	}
	contextName, _, err := client.CurrentContext(ctx, flags)
	if err != nil {
		return "", fmt.Errorf("failed to establish what Kubernetes context to use: %w", err)
	}
	return contextName, nil
}

type UserDaemon struct {
	connector.ConnectorClient
	Conn   *grpc.ClientConn
	Remote bool
}

func newUserDaemon(conn *grpc.ClientConn, remote bool) *UserDaemon {
	return &UserDaemon{
		ConnectorClient: connector.NewConnectorClient(conn),
		Conn:            conn,
		Remote:          remote,
	}
}

type Session struct {
	UserDaemon
	Info    *connector.ConnectInfo
	Started bool
}

func ensureUserDaemon(ctx context.Context, required bool) (context.Context, error) {
	if _, ok := ctx.Value(userDaemonKey{}).(*UserDaemon); ok {
		return ctx, nil
	}
	var ud *UserDaemon
	if addr := client.GetEnv(ctx).UserDaemonAddress; addr != "" {
		// Assume that the user daemon is running and connect to it using the given address instead of using a socket.
		conn, err := grpc.DialContext(ctx, addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithNoProxy(),
			grpc.WithBlock(),
			grpc.FailOnNonTempDialError(true))
		if err != nil {
			return ctx, err
		}
		ud = newUserDaemon(conn, true)
	} else {
		var err error
		ud, err = launchConnectorDaemon(ctx, client.GetExe(), required)
		if err != nil {
			return ctx, err
		}
	}
	return context.WithValue(ctx, userDaemonKey{}, ud), nil
}

func ensureDaemonVersion(ctx context.Context) error {
	// Ensure that the already running daemon has the correct version
	return versionCheck(ctx, client.GetExe(), GetUserDaemon(ctx))
}

func ensureSession(ctx context.Context, required bool) (context.Context, error) {
	if _, ok := ctx.Value(sessionKey{}).(*Session); ok {
		return ctx, nil
	}
	s, err := connectSession(ctx, GetUserDaemon(ctx), connect.GetRequest(ctx), required)
	if err != nil {
		return ctx, err
	}
	if s == nil {
		return ctx, nil
	}
	return context.WithValue(ctx, sessionKey{}, s), nil
}

func connectSession(ctx context.Context, userD *UserDaemon, request *connect.Request, required bool) (*Session, error) {
	var ci *connector.ConnectInfo
	var err error
	if request == nil {
		// implicit calls use the current Status instead of passing flags and mapped namespaces.
		ci, err = userD.Status(ctx, &empty.Empty{})
	} else {
		if !required {
			return nil, nil
		}
		request.AddKubeconfigEnv()
		ci, err = userD.Connect(ctx, &request.ConnectRequest)
	}
	if err != nil {
		return nil, err
	}

	var msg string
	cat := errcat.Unknown
	switch ci.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(output.Info(ctx), "Connected to context %s (%s)\n", ci.ClusterContext, ci.ClusterServer)
		return &Session{
			UserDaemon: *userD,
			Info:       ci,
			Started:    true,
		}, nil
	case connector.ConnectInfo_ALREADY_CONNECTED:
		return &Session{
			UserDaemon: *userD,
			Info:       ci,
			Started:    false,
		}, nil
	case connector.ConnectInfo_DISCONNECTED:
		if !required {
			return nil, nil
		}
		if request != nil {
			return nil, ErrNoTrafficManager
		}
		// The attempt is implicit, i.e. caused by direct invocation of another command without a
		// prior call to connect. So we make it explicit here without flags
		return connectSession(ctx, userD, &connect.Request{}, required)
	case connector.ConnectInfo_MUST_RESTART:
		msg = "Cluster configuration changed, please quit telepresence and reconnect"
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED, connector.ConnectInfo_CLUSTER_FAILED, connector.ConnectInfo_DAEMON_FAILED:
		msg = ci.ErrorText
		if ci.ErrorCategory != 0 {
			cat = errcat.Category(ci.ErrorCategory)
		}
	}
	return nil, cat.Newf("connector.Connect: %s", msg)
}
