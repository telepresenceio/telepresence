package cliutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type connectRequest struct{}

var (
	ErrNoUserDaemon     = errors.New("telepresence user daemon is not running")
	ErrNoRootDaemon     = errors.New("telepresence root daemon is not running")
	ErrNoTrafficManager = errors.New("telepresence traffic manager is not connected")
)

func WithConnectionRequest(ctx context.Context, rq *connector.ConnectRequest) context.Context {
	return context.WithValue(ctx, connectRequest{}, rq)
}

func GetConnectRequest(ctx context.Context) *connector.ConnectRequest {
	if cr, ok := ctx.Value(connectRequest{}).(*connector.ConnectRequest); ok {
		return cr
	}
	return nil
}

type userDaemonKey struct{}

func GetUserDaemon(ctx context.Context) *UserDaemon {
	if ud, ok := ctx.Value(userDaemonKey{}).(*UserDaemon); ok {
		return ud
	}
	return nil
}

type sessionKey struct{}

func GetSession(ctx context.Context) *Session {
	if s, ok := ctx.Value(sessionKey{}).(*Session); ok {
		return s
	}
	return nil
}

func InitCommand(cmd *cobra.Command) (err error) {
	ctx := cmd.Context()
	as := cmd.Annotations

	if v, ok := as[ann.Session]; ok {
		as[ann.UserDaemon] = v
		as[ann.VersionCheck] = ann.Required
	}
	if _, ok := as[ann.Notifications]; ok {
		as[ann.VersionCheck] = ann.Required
	}

	if as[ann.RootDaemon] == ann.Required {
		if err = EnsureRootDaemonRunning(ctx); err != nil {
			return err
		}
	}
	if v := as[ann.UserDaemon]; v == ann.Optional || v == ann.Required {
		if ctx, err = ensureUserDaemon(ctx, v == ann.Required); err != nil {
			if v == ann.Optional && err == ErrNoUserDaemon {
				// This is OK, but further initialization is not possible
				err = nil
			}
			return err
		}

		// RootDaemon == Optional means that the RootDaemon must be started if
		// the UserDaemon was started
		if as[ann.RootDaemon] == ann.Optional {
			if err = EnsureRootDaemonRunning(ctx); err != nil {
				return err
			}
		}
	} else {
		// The rest requires a user daemon
		return nil
	}
	if as[ann.VersionCheck] == ann.Required {
		if err = ensureDaemonVersion(ctx); err != nil {
			return err
		}
	}

	if v := as[ann.Session]; v == ann.Optional || v == ann.Required {
		if ctx, err = ensureSession(ctx, v == ann.Required); err != nil {
			return err
		}
	}
	if as[ann.Notifications] == ann.Required {
		if err = ensureNotifications(ctx); err != nil {
			return err
		}
	}
	cmd.SetContext(ctx)
	return nil
}

func UserDaemonDisconnect(ctx context.Context, quitDaemons bool) (err error) {
	stdout, _ := output.Structured(ctx)
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
		if _, err = ud.Disconnect(ctx, &empty.Empty{}); status.Code(err) != codes.Unimplemented {
			// nil or not unimplemented
			return err
		}
		// Disconnect is not implemented so daemon predates 2.4.9. Force a quit
	}
	if _, err = ud.Quit(ctx, &empty.Empty{}); err == nil || grpcStatus.Code(err) == grpcCodes.Unavailable {
		err = client.WaitUntilSocketVanishes("user daemon", client.ConnectorSocketName, 5*time.Second)
	}
	if err != nil && grpcStatus.Code(err) == grpcCodes.Unavailable {
		if quitDaemons {
			fmt.Fprintln(stdout, "have already quit")
		} else {
			fmt.Fprintln(stdout, "are already disconnected")
		}
		err = nil
	}
	return err
}

func AddKubeconfigEnv(cr *connector.ConnectRequest) {
	// Certain options' default are bound to the connector daemon process; this is notably true of the kubeconfig file(s) to use,
	// and since those files can be specified, both as a --kubeconfig flag and in the KUBECONFIG setting, and since the flag won't
	// accept multiple path entries, we need to pass the environment setting to the connector daemon so that it can set it every
	// time it receives a new config.
	if cfg, ok := os.LookupEnv("KUBECONFIG"); ok {
		if cr.KubeFlags == nil {
			cr.KubeFlags = make(map[string]string)
		}
		cr.KubeFlags["KUBECONFIG"] = cfg
	}
}

func launchConnectorDaemon(ctx context.Context, connectorDaemon string, required bool) (conn *grpc.ClientConn, err error) {
	conn, err = client.DialSocket(ctx, client.ConnectorSocketName)
	if errors.Is(err, os.ErrNotExist) {
		err = ErrNoUserDaemon
		if required {
			stdout, _ := output.Structured(ctx)
			fmt.Fprintln(stdout, "Launching Telepresence User Daemon")
			if _, err = ensureAppUserConfigDir(ctx); err != nil {
				return nil, err
			}
			if err = proc.StartInBackground(connectorDaemon, "connector-foreground"); err != nil {
				return nil, fmt.Errorf("failed to launch the connector service: %w", err)
			}
			if err = client.WaitUntilSocketAppears("connector", client.ConnectorSocketName, 10*time.Second); err != nil {
				return nil, fmt.Errorf("connector service did not start: %w", err)
			}
			conn, err = client.DialSocket(ctx, client.ConnectorSocketName)
		}
	}
	return conn, err
}

type UserDaemon struct {
	connector.ConnectorClient
	Conn *grpc.ClientConn
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
	conn, err := launchConnectorDaemon(ctx, client.GetExe(), required)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, userDaemonKey{}, &UserDaemon{
		Conn:            conn,
		ConnectorClient: connector.NewConnectorClient(conn),
	}), nil
}

func ensureDaemonVersion(ctx context.Context) error {
	// Ensure that the already running daemon has the correct version
	return versionCheck(ctx, client.GetExe(), GetUserDaemon(ctx))
}

func ensureNotifications(ctx context.Context) error {
	cc := GetUserDaemon(ctx)
	stdout, _ := output.Structured(ctx)
	stream, err := cc.UserNotifications(ctx, &empty.Empty{})
	if err != nil {
		return err
	}
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			fmt.Fprintln(stdout, strings.TrimRight(msg.Message, "\n"))
		}
	}()
	return nil
}

func ensureSession(ctx context.Context, required bool) (context.Context, error) {
	if _, ok := ctx.Value(sessionKey{}).(*Session); ok {
		return ctx, nil
	}
	s, err := connect(ctx, GetUserDaemon(ctx), GetConnectRequest(ctx), required)
	if err != nil {
		return ctx, err
	}
	if s == nil {
		return ctx, nil
	}
	return context.WithValue(ctx, sessionKey{}, s), nil
}

func connect(ctx context.Context, userD *UserDaemon, request *connector.ConnectRequest, required bool) (*Session, error) {
	var ci *connector.ConnectInfo
	var err error
	if request == nil {
		// implicit calls use the current Status instead of passing flags and mapped namespaces.
		ci, err = userD.Status(ctx, &empty.Empty{})
	} else {
		if !required {
			return nil, nil
		}
		AddKubeconfigEnv(request)
		ci, err = userD.Connect(ctx, request)
	}
	if err != nil {
		return nil, err
	}

	var msg string
	cat := errcat.Unknown
	switch ci.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		stdout, _ := output.Structured(ctx)
		fmt.Fprintf(stdout, "Connected to context %s (%s)\n", ci.ClusterContext, ci.ClusterServer)
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
		return connect(ctx, userD, &connector.ConnectRequest{}, required)
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
