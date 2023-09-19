package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
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
	ud := daemon.GetUserClient(ctx)
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
	if _, err = ud.Quit(ctx, &emptypb.Empty{}); !ud.Remote() && (err == nil || status.Code(err) == codes.Unavailable) {
		_ = socket.WaitUntilVanishes("user daemon", socket.UserDaemonPath(ctx), 5*time.Second)
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
	if daemon.GetSession(ctx).Started {
		defer func() {
			_ = Disconnect(ctx, false)
		}()
	}
	return proc.Run(dos.WithStdio(ctx, cmd), nil, args[0], args[1:]...)
}

func launchConnectorDaemon(ctx context.Context, connectorDaemon string, required bool) (context.Context, *daemon.UserClient, error) {
	cr := daemon.GetRequest(ctx)

	// Try dialing the host daemon using the well known socket.
	conn, err := socket.Dial(ctx, socket.UserDaemonPath(ctx))
	if err == nil {
		if cr.Docker {
			return ctx, nil, errcat.User.New("option --docker cannot be used as long as a daemon is running on the host. Try telepresence quit -s")
		}
		return ctx, newUserDaemon(conn, nil), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ctx, nil, errcat.NoDaemonLogs.New(err)
	}

	// Check if a running daemon can be discovered.
	ctx = docker.EnableClient(ctx)
	conn, daemonID, err := docker.DiscoverDaemon(ctx, cr.Use)
	if err == nil {
		return ctx, newUserDaemon(conn, daemonID), nil
	}
	var infoMatchErr daemon.InfoMatchError
	if errors.As(err, &infoMatchErr) {
		return ctx, nil, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		dlog.Debug(ctx, err.Error())
	}

	if !required {
		return ctx, nil, ErrNoUserDaemon
	}

	if cr.Docker {
		daemonID, err = daemon.IdentifierFromFlags(cr.Name, cr.KubeFlags)
		if err != nil {
			return ctx, nil, errcat.NoDaemonLogs.New(err)
		}
		conn, err = docker.LaunchDaemon(ctx, daemonID)
		if err != nil {
			return ctx, nil, errcat.NoDaemonLogs.New(err)
		}
		return ctx, newUserDaemon(conn, daemonID), nil
	}

	fmt.Fprintln(output.Info(ctx), "Launching Telepresence User Daemon")
	if err = ensureAppUserCacheDirs(ctx); err != nil {
		return ctx, nil, err
	}
	if err = ensureAppUserConfigDir(ctx); err != nil {
		return ctx, nil, err
	}
	args := []string{connectorDaemon, "connector-foreground"}
	if cr.UserDaemonProfilingPort > 0 {
		args = append(args, "--pprof", strconv.Itoa(int(cr.UserDaemonProfilingPort)))
	}
	if err = proc.StartInBackground(false, args...); err != nil {
		return ctx, nil, errcat.NoDaemonLogs.Newf("failed to launch the connector service: %w", err)
	}
	if err = socket.WaitUntilAppears("connector", socket.UserDaemonPath(ctx), 10*time.Second); err != nil {
		return ctx, nil, errcat.NoDaemonLogs.Newf("connector service did not start: %w", err)
	}
	conn, err = socket.Dial(ctx, socket.UserDaemonPath(ctx))
	if err != nil {
		return ctx, nil, err
	}
	return ctx, newUserDaemon(conn, nil), nil
}

func newUserDaemon(conn *grpc.ClientConn, daemonID *daemon.Identifier) *daemon.UserClient {
	return &daemon.UserClient{
		ConnectorClient: connector.NewConnectorClient(conn),
		Conn:            conn,
		DaemonID:        daemonID,
	}
}

func EnsureUserDaemon(ctx context.Context, required bool) (context.Context, error) {
	var err error
	defer func() {
		// The RootDaemon must be started if the UserClient was started
		if err == nil {
			err = ensureRootDaemonRunning(ctx)
		}
	}()

	if daemon.GetUserClient(ctx) != nil {
		return ctx, nil
	}
	var ud *daemon.UserClient
	if addr := client.GetEnv(ctx).UserDaemonAddress; addr != "" {
		// Assume that the user daemon is running and connect to it using the given address instead of using a socket.
		// NOTE: The UserDaemonAddress does not imply that the daemon runs in Docker
		var conn *grpc.ClientConn
		conn, err = grpc.DialContext(ctx, addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithNoProxy(),
			grpc.WithBlock(),
			grpc.FailOnNonTempDialError(true))
		if err == nil {
			ud = newUserDaemon(conn, nil)
		}
	} else {
		ctx, ud, err = launchConnectorDaemon(ctx, client.GetExe(), required)
	}
	if err != nil {
		return ctx, err
	}
	return daemon.WithUserClient(ctx, ud), nil
}

func ensureDaemonVersion(ctx context.Context) error {
	// Ensure that the already running daemon has the correct version
	return versionCheck(ctx, client.GetExe(), daemon.GetUserClient(ctx))
}

func EnsureSession(ctx context.Context, useLine string, required bool) (context.Context, error) {
	if daemon.GetSession(ctx) != nil {
		return ctx, nil
	}
	s, err := connectSession(ctx, useLine, daemon.GetUserClient(ctx), daemon.GetRequest(ctx), required)
	if err != nil {
		return ctx, err
	}
	if s == nil {
		return ctx, nil
	}
	if dns := s.Info.GetDaemonStatus().GetOutboundConfig().GetDns(); dns != nil && dns.Error != "" {
		ioutil.Printf(output.Err(ctx), "Warning: %s\n", dns.Error)
	}
	return daemon.WithSession(ctx, s), nil
}

func connectSession(ctx context.Context, useLine string, userD *daemon.UserClient, request *daemon.Request, required bool) (*daemon.Session, error) {
	var ci *connector.ConnectInfo
	var err error
	if userD.Remote() {
		// We instruct the remote daemon to modify its KUBECONFIG.
		delete(request.Environment, "KUBECONFIG")
		delete(request.Environment, "-KUBECONFIG")
	}
	cat := errcat.Unknown

	session := func(ci *connector.ConnectInfo, started bool) *daemon.Session {
		// Update the request from the connect info.
		request.KubeFlags = ci.KubeFlags
		request.ManagerNamespace = ci.ManagerNamespace
		request.Name = ci.ConnectionName
		return &daemon.Session{
			UserClient: *userD,
			Info:       ci,
			Started:    started,
		}
	}

	connectResult := func(ci *connector.ConnectInfo) (*daemon.Session, error) {
		var msg string
		switch ci.Error {
		case connector.ConnectInfo_UNSPECIFIED:
			fmt.Fprintf(output.Info(ctx), "Connected to context %s, namespace %s (%s)\n", ci.ClusterContext, ci.Namespace, ci.ClusterServer)
			return session(ci, true), nil
		case connector.ConnectInfo_ALREADY_CONNECTED:
			return session(ci, false), nil
		case connector.ConnectInfo_MUST_RESTART:
			msg = "Cluster configuration changed, please quit telepresence and reconnect"
		default:
			msg = ci.ErrorText
			if ci.ErrorCategory != 0 {
				cat = errcat.Category(ci.ErrorCategory)
			}
		}
		return nil, cat.Newf("connector.Connect: %s", msg)
	}

	if request.Implicit {
		// implicit calls use the current Status instead of passing flags and mapped namespaces.
		if ci, err = userD.Status(ctx, &empty.Empty{}); err != nil {
			return nil, err
		}
		if ci.Error != connector.ConnectInfo_DISCONNECTED {
			return connectResult(ci)
		}
		if required {
			_, _ = fmt.Fprintf(output.Info(ctx),
				`Warning: You are executing the %q command without a preceding "telepresence connect", causing an implicit `+
					"connect to take place. The implicit connect behavior is deprecated and will be removed in a future release.\n",
				useLine)
		}
	}

	if !required {
		return nil, nil
	}

	if ci, err = userD.Connect(ctx, &request.ConnectRequest); err != nil {
		return nil, err
	}
	return connectResult(ci)
}
