package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cenkalti/backoff/v4"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

var (
	ErrNoUserDaemon = errors.New("telepresence user daemon is not running")
	ErrNoRootDaemon = errors.New("telepresence root daemon is not running")
)

type ConnectError struct {
	error
	code connector.ConnectInfo_ErrType
}

func (ce *ConnectError) Code() connector.ConnectInfo_ErrType {
	return ce.code
}

func (ce *ConnectError) Unwrap() error {
	return ce.error
}

//nolint:gochecknoglobals // extension point
var QuitDaemonFuncs = []func(context.Context){
	quitHostConnector, quitDockerDaemons,
}

func quitHostConnector(ctx context.Context) {
	udCtx, err := ExistingHostDaemon(ctx, nil)
	if err != nil {
		if !os.IsNotExist(err) {
			dlog.Errorf(ctx, "unable to quit existing user daemon: %v", err)
		}
		return
	}
	ud := daemon.GetUserClient(udCtx)
	_, _ = ud.Quit(ctx, &emptypb.Empty{})
	_ = ud.Conn.Close()
	_ = socket.WaitUntilVanishes("user daemon", socket.UserDaemonPath(ctx), 5*time.Second)

	// User daemon is responsible for killing the root daemon, but we kill it here too to cater for
	// the fact that the user daemon might have been killed ungracefully.
	if waitErr := socket.WaitUntilVanishes("root daemon", socket.RootDaemonPath(ctx), 5*time.Second); waitErr != nil {
		quitRootDaemon(ctx)
	}
}

func quitDockerDaemons(ctx context.Context) {
	infos, err := daemon.LoadInfos(ctx)
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	for _, info := range infos {
		udCtx, err := ExistingDaemon(ctx, info)
		if err != nil {
			dlog.Error(ctx, err)
			continue
		}
		ud := daemon.GetUserClient(udCtx)
		_, _ = ud.Quit(ctx, &emptypb.Empty{})
		_ = ud.Conn.Close()
	}
	if err = daemon.WaitUntilAllVanishes(ctx, 5*time.Second); err != nil {
		dlog.Error(ctx, err)
		_ = daemon.DeleteAllInfos(ctx)
	}
}

func ExistingDaemon(ctx context.Context, info *daemon.Info) (context.Context, error) {
	var err error
	var conn *grpc.ClientConn
	if info.InDocker && !proc.RunningInContainer() {
		// The host relies on that the daemon has exposed a port to localhost
		conn, err = docker.ConnectDaemon(ctx, fmt.Sprintf(":%d", info.DaemonPort))
		if err != nil {
			return ctx, err
		}
		return newUserDaemon(ctx, conn, info.DaemonID())
	}
	return ExistingHostDaemon(ctx, info.DaemonID())
}

func ExistingHostDaemon(ctx context.Context, id *daemon.Identifier) (context.Context, error) {
	// Try dialing the host daemon using the well-known socket.
	socketName := socket.UserDaemonPath(ctx)
	conn, err := socket.Dial(ctx, socketName, false)
	if err == nil {
		ctx, err = newUserDaemon(ctx, conn, id)
		if err != nil {
			// User daemon is not responding. Make an attempt to delete the lingering socket.
			if rmErr := os.Remove(socketName); rmErr != nil {
				err = fmt.Errorf("%v; remove of unresponsive socket failed: %v", err, rmErr)
			}
		}
	}
	return ctx, err
}

// Quit shuts down all daemons.
func Quit(ctx context.Context) {
	stdout := output.Out(ctx)
	ioutil.Print(stdout, "Telepresence Daemons quitting...")
	for _, quitFunc := range QuitDaemonFuncs {
		quitFunc(ctx)
	}
	ioutil.Println(stdout, "done")
}

// Disconnect disconnects from a session in the user daemon.
func Disconnect(ctx context.Context) {
	if ud := daemon.GetUserClient(ctx); ud == nil {
		ioutil.Println(output.Out(ctx), "Not connected")
	} else {
		_, err := ud.Disconnect(ctx, &emptypb.Empty{})
		switch {
		case err == nil:
			ioutil.Println(output.Out(ctx), "Disconnected")
		case status.Code(err) == codes.Unavailable:
			ioutil.Println(output.Out(ctx), "Not connected")
		default:
			ioutil.Printf(output.Err(ctx), "failed to disconnect: %v\n", err)
		}
	}
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
		defer Disconnect(ctx)
	}
	return proc.Run(dos.WithStdio(ctx, cmd), nil, args[0], args[1:]...)
}

// DiscoverDaemon searches the daemon cache for an entry corresponding to the given name. A connection
// to that daemon is returned if such an entry is found.
func DiscoverDaemon(ctx context.Context, match *regexp.Regexp, daemonID *daemon.Identifier) (context.Context, error) {
	cr := daemon.GetRequest(ctx)
	if match == nil && !cr.Implicit {
		match = regexp.MustCompile(`\A` + regexp.QuoteMeta(daemonID.Name) + `\z`)
	}
	info, err := daemon.LoadMatchingInfo(ctx, match)
	if err != nil {
		if os.IsNotExist(err) && !cr.Docker {
			// Try dialing the host daemon using the well-known socket.
			if conn, sockErr := socket.Dial(ctx, socket.UserDaemonPath(ctx), false); sockErr == nil {
				return newUserDaemon(ctx, conn, daemonID)
			}
		}
		return ctx, err
	}
	if len(cr.ExposedPorts) > 0 && !slices.Equal(info.ExposedPorts, cr.ExposedPorts) {
		return ctx, errcat.User.New("exposed ports differ. Please quit and reconnect")
	}
	return ExistingDaemon(ctx, info)
}

func launchConnectorDaemon(ctx context.Context, connectorDaemon string, required bool) (context.Context, error) {
	cr := daemon.GetRequest(ctx)
	cliInContainer := proc.RunningInContainer()
	daemonID, err := daemon.IdentifierFromFlags(ctx, cr.Name, cr.KubeFlags, cr.KubeconfigData, cr.Docker || cliInContainer)
	if err != nil {
		return ctx, err
	}

	// Try dialing the host daemon using the well known socket.
	ctx, err = DiscoverDaemon(ctx, cr.Use, daemonID)
	if err == nil {
		ud := daemon.GetUserClient(ctx)
		if ud.Containerized() && !cliInContainer {
			ctx = docker.EnableClient(ctx)
			cr.Docker = true
		}
		if ud.Containerized() == (cr.Docker || cliInContainer) {
			return ctx, nil
		}
		// A daemon running on the host does not fulfill a request for a containerized daemon. They can
		// coexist though.
		err = os.ErrNotExist
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ctx, errcat.NoDaemonLogs.New(err)
	}
	if !required {
		return ctx, ErrNoUserDaemon
	}

	ioutil.Println(output.Info(ctx), "Launching Telepresence User Daemon")
	if err = ensureAppUserCacheDirs(ctx); err != nil {
		return ctx, err
	}
	if err = ensureAppUserConfigDir(ctx); err != nil {
		return ctx, err
	}

	var conn *grpc.ClientConn
	if cr.Docker && !cliInContainer {
		// Ensure that the logfile is present before the daemon starts so that it isn't created with
		// permissions from the docker container.
		logDir := filelocation.AppUserLogDir(ctx)
		logFile := filepath.Join(logDir, "connector.log")
		if _, err := os.Stat(logFile); err != nil {
			if !os.IsNotExist(err) {
				return ctx, err
			}
			fh, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY, 0o666)
			if err != nil {
				return ctx, err
			}
			_ = fh.Close()
		}
		ctx = docker.EnableClient(ctx)
		conn, err = docker.LaunchDaemon(ctx, daemonID)
	} else {
		args := []string{connectorDaemon, "connector-foreground"}
		if cr.UserDaemonProfilingPort > 0 {
			args = append(args, "--pprof", strconv.Itoa(int(cr.UserDaemonProfilingPort)))
		}
		if cliInContainer && os.Getuid() == 0 {
			// No use having multiple daemons when running as root in docker.
			hn, err := os.Hostname()
			if err != nil {
				hn = "unknown"
			}
			args = append(args, "--embed-network")
			args = append(args, "--name", "docker-"+hn)
		}
		err = daemon.SaveInfo(ctx,
			&daemon.Info{
				InDocker:     cliInContainer,
				DaemonPort:   0,
				Name:         daemonID.Name,
				KubeContext:  daemonID.KubeContext,
				Namespace:    daemonID.Namespace,
				ExposedPorts: cr.ExposedPorts,
				Hostname:     cr.Hostname,
			}, daemonID.InfoFileName())
		if err != nil {
			return ctx, err
		}
		defer func() {
			if err != nil {
				_ = daemon.DeleteInfo(ctx, daemonID.InfoFileName())
			}
		}()

		if err = proc.StartInBackground(false, args...); err != nil {
			return ctx, errcat.NoDaemonLogs.Newf("failed to launch the connector service: %w", err)
		}
		conn, err = socket.Dial(ctx, socket.UserDaemonPath(ctx), true)
	}
	if err != nil {
		return ctx, err
	}
	return newUserDaemon(ctx, conn, daemonID)
}

// getConnectorVersion is the first call to the user daemon, so a backoff is used here to trap errors
// caused during the initial state change of the connection.
func getConnectorVersion(ctx context.Context, cc connector.ConnectorClient) (*common.VersionInfo, error) {
	tos := client.GetConfig(ctx).Timeouts()
	b := backoff.ExponentialBackOff{
		InitialInterval:     500 * time.Millisecond,
		RandomizationFactor: backoff.DefaultRandomizationFactor,
		Multiplier:          backoff.DefaultMultiplier,
		MaxInterval:         2 * time.Second,
		MaxElapsedTime:      tos.Get(client.TimeoutTrafficManagerAPI),
		Stop:                backoff.Stop,
		Clock:               backoff.SystemClock,
	}
	b.Reset()
	var vi *common.VersionInfo
	err := backoff.Retry(func() (err error) {
		vi, err = cc.Version(ctx, &emptypb.Empty{})
		return err
	}, &b)
	return vi, err
}

func newUserDaemon(ctx context.Context, conn *grpc.ClientConn, daemonID *daemon.Identifier) (context.Context, error) {
	cc := connector.NewConnectorClient(conn)
	vi, err := getConnectorVersion(ctx, cc)
	if err != nil {
		return ctx, err
	}
	v, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return ctx, fmt.Errorf("unable to parse version obtained from connector daemon: %w", err)
	}
	ctx = daemon.WithUserClient(ctx, &daemon.UserClient{
		ConnectorClient: cc,
		Conn:            conn,
		DaemonID:        daemonID,
		Version:         v,
		Name:            vi.Name,
		Executable:      vi.Executable,
	})
	return ctx, nil
}

func EnsureUserDaemon(ctx context.Context, required bool) (rc context.Context, err error) {
	defer func() {
		if err == nil && required && !daemon.GetUserClient(rc).Containerized() {
			// The RootDaemon must be started if the UserDaemon was started
			err = ensureRootDaemonRunning(ctx)
		}
	}()

	if daemon.GetUserClient(ctx) != nil {
		return ctx, nil
	}
	return launchConnectorDaemon(ctx, client.GetExe(ctx), required)
}

func ensureDaemonVersion(ctx context.Context) error {
	// Ensure that the already running daemon has the correct version
	return versionCheck(ctx, client.GetExe(ctx))
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
	if userD.Containerized() && !proc.RunningInContainer() {
		patcher.AnnotateConnectRequest(&request.ConnectRequest, docker.TpCache, userD.DaemonID.KubeContext)
	}
	session := func(ci *connector.ConnectInfo, started bool) *daemon.Session {
		// Update the request from the connect info.
		request.KubeFlags = ci.KubeFlags
		request.ManagerNamespace = ci.ManagerNamespace
		request.Name = ci.ConnectionName
		userD.DaemonID = &daemon.Identifier{
			Name:          ci.ConnectionName,
			KubeContext:   ci.ClusterContext,
			Namespace:     ci.Namespace,
			Containerized: userD.Containerized(),
		}
		return &daemon.Session{
			UserClient: *userD,
			Info:       ci,
			Started:    started,
		}
	}

	// warn if the version diff between cli and manager is > 3 or if there's an OSS/Enterprise mismatch.
	warnMngrVersion := func(ci *connector.ConnectInfo) error {
		mv := ci.ManagerVersion

		// remove leading v from semver
		mSemver, err := semver.Parse(strings.TrimPrefix(mv.Version, "v"))
		if err != nil {
			return err
		}

		cliSemver := client.Semver()

		var diff uint64
		if cliSemver.Minor > mSemver.Minor {
			diff = cliSemver.Minor - mSemver.Minor
		} else {
			diff = mSemver.Minor - cliSemver.Minor
		}

		maxDiff := uint64(3)
		if diff > maxDiff {
			ioutil.Printf(output.Info(ctx),
				"The Traffic Manager version (%s) is more than %v minor versions diff from client version (%s), please consider upgrading.\n",
				mv.Version, maxDiff, client.Version())
		}

		cv := ci.Version
		if strings.HasPrefix(cv.Name, "OSS ") && !strings.HasPrefix(mv.Name, "OSS ") {
			ioutil.Printf(output.Info(ctx),
				"You are using the OSS client %s to connect to an enterprise traffic manager %s. Please consider installing an\n"+
					"enterprise client from getambassador.io, or use \"telepresence helm install\" to install an OSS traffic-manager\n",
				cv.Version,
				mv.Version)
		}
		return nil
	}

	connectResult := func(ci *connector.ConnectInfo) (*daemon.Session, error) {
		var msg string
		cat := errcat.Unknown
		switch ci.Error {
		case connector.ConnectInfo_UNSPECIFIED:
			ioutil.Printf(output.Info(ctx), "Connected to context %s, namespace %s (%s)\n", ci.ClusterContext, ci.Namespace, ci.ClusterServer)
			err := warnMngrVersion(ci)
			if err != nil {
				dlog.Error(ctx, err)
			}
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
		return nil, &ConnectError{error: cat.Newf("connector.Connect: %s", msg), code: ci.Error}
	}

	if request.Implicit {
		// implicit calls use the current Status instead of passing flags and mapped namespaces.
		if ci, err = userD.Status(ctx, &emptypb.Empty{}); err != nil {
			return nil, err
		}
		if ci.Error != connector.ConnectInfo_DISCONNECTED {
			return connectResult(ci)
		}
		if required {
			ioutil.Printf(output.Info(ctx),
				`Warning: You are executing the %q command without a preceding "telepresence connect", causing an implicit `+
					"connect to take place. The implicit connect behavior is deprecated and will be removed in a future release.\n",
				useLine)
		}
	}

	if !required {
		return nil, nil
	}

	if !userD.Containerized() {
		daemonID := userD.DaemonID
		err = daemon.SaveInfo(ctx,
			&daemon.Info{
				InDocker:     false,
				Name:         daemonID.Name,
				KubeContext:  daemonID.KubeContext,
				Namespace:    daemonID.Namespace,
				ExposedPorts: request.ExposedPorts,
				Hostname:     request.Hostname,
			}, daemonID.InfoFileName())
		if err != nil {
			return nil, errcat.NoDaemonLogs.New(err)
		}
	}
	if ci, err = userD.Connect(ctx, &request.ConnectRequest); err != nil {
		if !userD.Containerized() {
			_ = daemon.DeleteInfo(ctx, userD.DaemonID.InfoFileName())
		}
		return nil, err
	}
	return connectResult(ci)
}
