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
	if _, err = ud.Quit(ctx, &emptypb.Empty{}); err == nil || status.Code(err) == codes.Unavailable {
		err = socket.WaitUntilVanishes("user daemon", socket.UserDaemonPath(ctx), 5*time.Second)
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

func launchConnectorDaemon(ctx context.Context, connectorDaemon string, required bool) (*daemon.UserClient, error) {
	cr := daemon.GetRequest(ctx)
	conn, err := socket.Dial(ctx, socket.UserDaemonPath(ctx))
	if err == nil {
		if cr.Docker {
			return nil, errcat.User.New("option --docker cannot be used as long as a daemon is running on the host. Try telepresence quit -s")
		}
		return newUserDaemon(conn, false), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errcat.NoDaemonLogs.New(err)
	}

	// Check if a running daemon can be discovered.
	name, err := contextName(ctx)
	if err != nil {
		return nil, err
	}
	conn, err = docker.DiscoverDaemon(ctx, name)
	if err == nil {
		return newUserDaemon(conn, true), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errcat.NoDaemonLogs.New(err)
	}
	if cr.Docker {
		if required {
			conn, err = docker.LaunchDaemon(ctx, name)
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
		return nil, errcat.NoDaemonLogs.New(err)
	}
	args := []string{connectorDaemon, "connector-foreground"}
	if cr.UserDaemonProfilingPort > 0 {
		args = append(args, "--pprof", strconv.Itoa(int(cr.UserDaemonProfilingPort)))
	}
	if err = proc.StartInBackground(false, args...); err != nil {
		return nil, errcat.NoDaemonLogs.Newf("failed to launch the connector service: %w", err)
	}
	if err = socket.WaitUntilAppears("connector", socket.UserDaemonPath(ctx), 10*time.Second); err != nil {
		return nil, errcat.NoDaemonLogs.Newf("connector service did not start: %w", err)
	}
	conn, err = socket.Dial(ctx, socket.UserDaemonPath(ctx))
	if err != nil {
		return nil, err
	}
	return newUserDaemon(conn, false), nil
}

func contextName(ctx context.Context) (string, error) {
	var flags map[string]string
	if cr := daemon.GetRequest(ctx); cr != nil {
		flags = cr.KubeFlags
	}
	name, _, err := client.CurrentContext(flags)
	if err != nil {
		return "", err
	}
	return name, nil
}

func newUserDaemon(conn *grpc.ClientConn, remote bool) *daemon.UserClient {
	return &daemon.UserClient{
		ConnectorClient: connector.NewConnectorClient(conn),
		Conn:            conn,
		Remote:          remote,
	}
}

func ensureUserDaemon(ctx context.Context, required bool) (context.Context, error) {
	if daemon.GetUserClient(ctx) != nil {
		return ctx, nil
	}
	var ud *daemon.UserClient
	if addr := client.GetEnv(ctx).UserDaemonAddress; addr != "" {
		// Assume that the user daemon is running and connect to it using the given address instead of using a socket.
		// NOTE: The UserDaemonAddress does not imply that the daemon runs in Docker
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
	return daemon.WithUserClient(ctx, ud), nil
}

func ensureDaemonVersion(ctx context.Context) error {
	// Ensure that the already running daemon has the correct version
	return versionCheck(ctx, client.GetExe(), daemon.GetUserClient(ctx))
}

func ensureSession(ctx context.Context, required bool) (context.Context, error) {
	if daemon.GetSession(ctx) != nil {
		return ctx, nil
	}
	s, err := connectSession(ctx, daemon.GetUserClient(ctx), daemon.GetRequest(ctx), required)
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

func connectSession(ctx context.Context, userD *daemon.UserClient, request *daemon.Request, required bool) (*daemon.Session, error) {
	var ci *connector.ConnectInfo
	var err error
	if userD.Remote {
		// We never pass on KUBECONFIG to a remote daemon.
		delete(request.KubeFlags, "KUBECONFIG")
	}
	cat := errcat.Unknown
	if request.Implicit {
		// implicit calls use the current Status instead of passing flags and mapped namespaces.
		if ci, err = userD.Status(ctx, &empty.Empty{}); err != nil {
			return nil, err
		}
		switch ci.Error {
		case connector.ConnectInfo_ALREADY_CONNECTED:
			if cc, ok := request.KubeFlags["context"]; ok && cc != ci.ClusterContext {
				ci.Error = connector.ConnectInfo_MUST_RESTART
			} else {
				return &daemon.Session{
					UserClient: *userD,
					Info:       ci,
					Started:    true,
				}, nil
			}
		case connector.ConnectInfo_DISCONNECTED:
			// proceed with connect
		default:
			if ci.ErrorCategory != 0 {
				cat = errcat.Category(ci.ErrorCategory)
			}
			return nil, cat.Newf("connector.Status: %s", ci.Error)
		}
	}

	if !required {
		return nil, nil
	}
	if ci, err = userD.Connect(ctx, &request.ConnectRequest); err != nil {
		return nil, err
	}

	var msg string
	switch ci.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(output.Info(ctx), "Connected to context %s (%s)\n", ci.ClusterContext, ci.ClusterServer)
		return &daemon.Session{
			UserClient: *userD,
			Info:       ci,
			Started:    true,
		}, nil
	case connector.ConnectInfo_ALREADY_CONNECTED:
		return &daemon.Session{
			UserClient: *userD,
			Info:       ci,
			Started:    false,
		}, nil
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
