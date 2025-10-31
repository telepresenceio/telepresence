package connect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
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
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	daemon2 "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/teleroute"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
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

// maybeComposeDown performs a `docker compose down -f <compose file>` and also deletes `<compose file>` if it exists.
// A full docker compose down is necessary because the created containers are dependent on the network represented by
// the connection that is about to close.
func maybeComposeDown(ctx context.Context, info *daemon.Info) {
	if info.ComposeFile == "" {
		return
	}
	defer func() {
		err := os.Remove(info.ComposeFile)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()
	progress.Stop(ctx)
	err := proc.StdCommand(ctx, docker.Exe, "compose", "--file", info.ComposeFile, "down", "--remove-orphans", "--volumes").Run()
	if err != nil {
		dlog.Error(ctx, err)
	}
	progress.Start(ctx, "Quitting")
}

func quitHostConnector(ctx context.Context) {
	udCtx, err := ExistingHostDaemon(ctx, &daemon.Info{})
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			progress.Errorf(ctx, "unable to quit existing user daemon: %v", err)
		}
		return
	}
	ud := daemon.MustGetUserClient(udCtx)
	progress.Working(ctx, "Quitting")
	_, _ = ud.Quit(ctx, &emptypb.Empty{})
	_ = ud.Close()
	_ = socket.WaitUntilVanishes("user daemon", socket.UserDaemonPath(ctx), 5*time.Second)

	// User daemon is responsible for killing the root daemon, but we kill it here too to cater for
	// the fact that the user daemon might have been killed ungracefully.
	if waitErr := socket.WaitUntilVanishes("root daemon", socket.RootDaemonPath(ctx), 5*time.Second); waitErr != nil {
		quitRootDaemon(ctx)
	}
	progress.PrintDone(ctx, "Quit")
}

func quitDockerDaemons(ctx context.Context) {
	infos, err := daemon.LoadInfos(ctx)
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	for _, info := range infos {
		maybeComposeDown(ctx, info)
		ctx := progress.WithEventId(ctx, info.DaemonID().Name)
		progress.Working(ctx, "Quitting")
		udCtx, err := ExistingDaemon(ctx, info)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				progress.Error(ctx, err.Error())
			}
			continue
		}
		ud := daemon.MustGetUserClient(udCtx)
		_, _ = ud.Quit(ctx, &emptypb.Empty{})
		_ = ud.Close()
		maybeDeleteNetwork(ctx, ud.DaemonInfo())
		progress.PrintDone(ctx, "Quit")
	}
	if err = daemon.WaitUntilAllVanishes(ctx, 5*time.Second); err != nil {
		dlog.Error(ctx, err)
		_ = daemon.DeleteAllInfos(ctx)
	}
}

func EnsureUserDaemon(ctx context.Context, required bool) (rc context.Context, err error) {
	cr := daemon.GetRequest(ctx)
	daemonID, err := daemon.IdentifierFromFlags(ctx, cr.Name, cr.KubeFlags, cr.KubeconfigData, cr.Docker)
	if err != nil {
		return ctx, err
	}

	ctx = progress.WithEventId(ctx, daemonID.Name)
	launched := false
	defer func() {
		if err == nil && required && !(proc.IsAdmin() || daemon.MustGetUserClient(rc).Containerized()) {
			// The RootDaemon must be started if the UserDaemon was started
			err = EnsureRootDaemonRunning(ctx)
		}
		if err != nil && !(errors.Is(err, ErrNoUserDaemon) && !required) {
			err = progress.MaybeWriteError(ctx, err)
		} else if launched {
			progress.PrintDone(ctx, "Launched Daemon")
		}
	}()

	if daemon.GetUserClient(ctx) != nil {
		return ctx, nil
	}
	rc, launched, err = launchConnectorDaemon(ctx, daemonID, client.GetExe(ctx), required)
	return rc, err
}

func EnsureSession(ctx context.Context, useLine string, required bool) (context.Context, error) {
	if daemon.GetSession(ctx) != nil {
		return ctx, nil
	}

	rq := daemon.GetRequest(ctx)
	s, err := connectSession(ctx, useLine, rq, required)
	if err != nil {
		return ctx, err
	}
	if s == nil {
		return ctx, nil
	}

	if s.Started && s.Containerized() {
		rootCfg, err := s.GetRootClientConfig()
		if err != nil {
			return ctx, err
		}
		if len(rootCfg.Routing().Subnets) > 0 {
			ctx = docker.EnableClient(ctx)
			err = createTelerouteNetwork(ctx, s.DaemonInfo())
			if err != nil {
				return ctx, err
			}
		}
	}

	for _, pm := range rq.LocalReroutes {
		err = ResolveLocalReroute(ctx, s, pm)
		if err != nil {
			return ctx, err
		}
	}

	for _, pm := range rq.RemoteReroutes {
		err = ResolveRemoteReroute(ctx, s, pm)
		if err != nil {
			return ctx, err
		}
	}
	return daemon.WithSession(ctx, s), nil
}

func ResolveLocalReroute(ctx context.Context, ds *daemon.Session, pm string) (err error) {
	ix := strings.IndexByte(pm, ':')
	if ix < 1 {
		return fmt.Errorf("invalid port mapping %s", pm)
	}
	localPort, err := types.ParsePortAndProto(pm[:ix])
	if err != nil {
		return fmt.Errorf("invalid port mapping %s: local port %w", pm, err)
	}
	hostPort, err := resolveHostPort(ctx, ds, localPort.Proto, pm[ix+1:])
	if err != nil {
		return err
	}
	hpb, err := hostPort.MarshalBinary()
	if err != nil {
		return err
	}
	_, err = ds.RerouteLocalPort(ctx, &daemon2.ReroutePortRequest{
		DstHostPort: hpb,
		SrcPort:     uint32(localPort.Port),
	})
	return err
}

func ResolveRemoteReroute(ctx context.Context, ds *daemon.Session, pm string) (err error) {
	ix := strings.LastIndexByte(pm, ':')
	if ix < 3 {
		return fmt.Errorf("invalid port mapping %s", pm)
	}
	newPort, err := types.ParsePortAndProto(pm[ix+1:])
	if err != nil {
		return fmt.Errorf("invalid port mapping %s: new port %w", pm, err)
	}
	hostPort, err := resolveHostPort(ctx, ds, newPort.Proto, pm[:ix])
	if err != nil {
		return err
	}
	hpb, err := hostPort.MarshalBinary()
	if err != nil {
		return err
	}
	_, err = ds.RerouteRemotePort(ctx, &daemon2.ReroutePortRequest{
		DstHostPort: hpb,
		SrcPort:     uint32(newPort.Port),
	})
	return err
}

func resolveHostPort(ctx context.Context, ds *daemon.Session, proto types.Proto, hostPortStr string) (hostPort types.AddrPortProto, err error) {
	hostPort.Proto = proto
	hostPort.AddrPort, err = netip.ParseAddrPort(hostPortStr)
	if err == nil {
		return hostPort, nil
	}
	// Resolve host and port name
	ix := strings.LastIndexByte(hostPortStr, ':')
	if ix < 1 {
		return hostPort, fmt.Errorf("invalid port mapping %s", hostPortStr)
	}
	portStr := hostPortStr[ix+1:]
	if hostPort.Proto != types.ProtoTCP {
		portStr = fmt.Sprintf("%s%c%s", portStr, types.ProtoSeparator, hostPort.Proto)
	}
	rsp, err := ds.ResolvePort(ctx, &daemon2.ResolvePortRequest{
		Host: hostPortStr[:ix],
		Port: portStr,
	})
	if err != nil {
		return hostPort, err
	}
	err = hostPort.UnmarshalBinary(rsp.HostPort)
	return hostPort, err
}

func ExistingDaemon(ctx context.Context, info *daemon.Info) (context.Context, error) {
	var err error
	var conn *grpc.ClientConn
	if info.InDocker() {
		// The host relies on that the daemon has exposed a port to localhost
		var addr netip.Addr
		if client.GetConfig(ctx).Docker().EnableIPv6 {
			addr = netip.IPv6Loopback()
		} else {
			addr = netip.AddrFrom4([4]byte{127, 0, 0, 1})
		}
		conn, err = docker.ConnectDaemon(ctx, netip.AddrPortFrom(addr, info.DaemonPort))
		if err != nil {
			return ctx, err
		}
		return newUserDaemon(ctx, conn, info)
	}
	return ExistingHostDaemon(ctx, info)
}

func ExistingHostDaemon(ctx context.Context, info *daemon.Info) (context.Context, error) {
	// Try dialing the host daemon using the well-known socket.
	socketName := socket.UserDaemonPath(ctx)
	conn, err := socket.Dial(ctx, socketName, false)
	if err == nil {
		ctx, err = newUserDaemon(ctx, conn, info)
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
	progress.Start(ctx, "Quitting")
	defer progress.Stop(ctx)
	for _, quitFunc := range QuitDaemonFuncs {
		quitFunc(ctx)
	}
}

// Disconnect disconnects from a session in the user daemon.
func Disconnect(ctx context.Context) {
	progress.Start(ctx, "Disconnecting")
	defer progress.Stop(ctx)
	if ud := daemon.GetUserClient(ctx); ud == nil {
		progress.PrintDone(progress.WithEventId(ctx, "daemon"), "Not connected")
	} else {
		id := ud.DaemonID()
		if id == nil {
			progress.PrintDone(progress.WithEventId(ctx, "daemon"), "Not connected")
			return
		}
		maybeComposeDown(ctx, ud.DaemonInfo())
		ctx = progress.WithEventId(ctx, id.Name)
		progress.Working(ctx, "Disconnecting")
		_, err := ud.Disconnect(ctx, &emptypb.Empty{})
		switch {
		case err == nil:
			progress.PrintDone(ctx, "Disconnected")
			maybeDeleteNetwork(ctx, ud.DaemonInfo())
		case status.Code(err) == codes.Unavailable:
			progress.PrintDone(ctx, "Not connected")
		default:
			_ = progress.MaybeWriteError(ctx, fmt.Errorf("failed to disconnect: %v", err))
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
	if daemon.MustGetSession(ctx).Started {
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
		if errors.Is(err, fs.ErrNotExist) && !cr.Docker {
			// Try dialing the host daemon using the well-known socket. If we find one, the daemon is running,
			// but it is not yet connected.
			if conn, sockErr := socket.Dial(ctx, socket.UserDaemonPath(ctx), false); sockErr == nil {
				// Provide a daemon.Info that reflects the expected connection. It will be adjusted when
				// the connection attempt succeeds.
				return newUserDaemon(ctx, conn, &daemon.Info{
					Name:         daemonID.Name,
					KubeContext:  daemonID.KubeContext,
					Namespace:    daemonID.Namespace,
					ExposedPorts: cr.ExposedPorts,
					Hostname:     cr.Hostname,
				})
			}
		}
		return ctx, err
	}
	if len(cr.ExposedPorts) > 0 && !slices.Equal(info.ExposedPorts, cr.ExposedPorts) {
		return ctx, errcat.User.New("exposed ports differ. Please quit and reconnect")
	}
	return ExistingDaemon(ctx, info)
}

func launchDockerDaemon(ctx context.Context, daemonID *daemon.Identifier, cr *daemon.Request) (context.Context, *daemon.Info, *grpc.ClientConn, error) {
	// Ensure that the logfile is present before the daemon starts so that it isn't created with
	// permissions from the docker container.
	logDir := filelocation.AppUserLogDir(ctx)
	logFile := filepath.Join(logDir, "connector.log")
	if _, err := os.Stat(logFile); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return ctx, nil, nil, err
		}
		fh, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY, 0o666)
		if err != nil {
			return ctx, nil, nil, err
		}
		_ = fh.Close()
	}
	ctx = docker.EnableClient(ctx)
	_, err := docker.EnsureNetworkPlugin(ctx)
	if err != nil {
		return ctx, nil, nil, err
	}

	// An initialized kubernetes interface is required by LaunchDaemon, because it is necessary
	// when checking if the containerized daemon is connecting to a k3s control plane node.
	ctx, kc, err := k8s.NewKubeconfig(ctx, cr.KubeFlags, cr.ManagerNamespace)
	if err != nil {
		return ctx, nil, nil, err
	}
	var ki *kubernetes.Clientset
	ki, err = kubernetes.NewForConfig(kc.RestConfig)
	if err != nil {
		return ctx, nil, nil, err
	}
	info, conn, err := docker.LaunchDaemon(k8sapi.WithK8sInterface(ctx, ki), daemonID)
	return ctx, info, conn, err
}

func launchHostDaemon(ctx context.Context, daemonID *daemon.Identifier, connectorDaemon string, cr *daemon.Request) (context.Context, *daemon.Info, *grpc.ClientConn, error) {
	args := []string{connectorDaemon, client.UserDaemonName, "--" + global.FlagConfig, client.GetConfigFile(ctx)}
	if cr.UserDaemonProfilingPort > 0 {
		args = append(args, "--pprof", strconv.Itoa(int(cr.UserDaemonProfilingPort)))
	}
	if proc.IsAdmin() {
		// No use having multiple daemons when running as root.
		args = append(args, "--embed-network")
	}
	dlog.Debugf(ctx, "Creating daemon info file %s (runs on host, or both CLI and daemon runs in container)", daemonID.Name)
	info := &daemon.Info{
		DaemonPort:   0,
		Name:         daemonID.Name,
		KubeContext:  daemonID.KubeContext,
		Namespace:    daemonID.Namespace,
		ExposedPorts: cr.ExposedPorts,
		Hostname:     cr.Hostname,
	}
	err := daemon.SaveInfo(ctx, info, daemonID.InfoFileName())
	if err != nil {
		return ctx, nil, nil, err
	}
	defer func() {
		if err != nil {
			file := daemonID.InfoFileName()
			dlog.Debugf(ctx, "Deleting daemon info %s due to launch error: %v", file, err)
			_ = daemon.DeleteInfo(ctx, file)
		}
	}()

	if err = proc.StartInBackground(false, args...); err != nil {
		return ctx, nil, nil, errcat.NoDaemonLogs.Newf("failed to launch the connector service: %w", err)
	}
	conn, err := socket.Dial(ctx, socket.UserDaemonPath(ctx), true)
	return ctx, info, conn, err
}

func launchConnectorDaemon(ctx context.Context, daemonID *daemon.Identifier, connectorDaemon string, required bool) (context.Context, bool, error) {
	cr := daemon.GetRequest(ctx)

	// Try dialing the host daemon using the well-known socket.
	ctx, err := DiscoverDaemon(ctx, cr.Use, daemonID)
	if err == nil {
		ud := daemon.MustGetUserClient(ctx)
		if ud.Containerized() {
			ctx = docker.EnableClient(ctx)
			cr.Docker = true
		}
		if ud.Containerized() == cr.Docker {
			return ctx, false, nil
		}
		// A daemon running on the host does not fulfill a request for a containerized daemon. They can
		// coexist though.
		err = os.ErrNotExist
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ctx, false, errcat.NoDaemonLogs.New(err)
	}
	if !required {
		return ctx, false, ErrNoUserDaemon
	}

	ctx = progress.WithEventId(ctx, daemonID.Name)
	progress.Working(ctx, "Launching Daemon")

	if err = ensureAppUserCacheDirs(ctx); err != nil {
		return ctx, false, err
	}
	if err = ensureAppUserConfigDir(ctx); err != nil {
		return ctx, false, err
	}

	var conn *grpc.ClientConn
	var info *daemon.Info
	if cr.Docker {
		ctx, info, conn, err = launchDockerDaemon(ctx, daemonID, cr)
	} else {
		ctx, info, conn, err = launchHostDaemon(ctx, daemonID, connectorDaemon, cr)
	}
	if err != nil {
		return ctx, false, err
	}
	ctx, err = newUserDaemon(ctx, conn, info)
	return ctx, err == nil, err
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
	}, backoff.WithContext(&b, ctx))
	return vi, err
}

func newUserDaemon(ctx context.Context, conn *grpc.ClientConn, info *daemon.Info) (context.Context, error) {
	vi, err := getConnectorVersion(ctx, connector.NewConnectorClient(conn))
	if err != nil {
		return ctx, err
	}
	v, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return ctx, fmt.Errorf("unable to parse version obtained from connector daemon: %w", err)
	}
	ctx = daemon.WithUserClient(ctx, daemon.NewUserClientFunc(conn, info, v, vi.Name, vi.Executable))
	ctx = progress.WithEventId(ctx, info.Name)
	return ctx, nil
}

func ensureDaemonVersion(ctx context.Context) error {
	// Ensure that the already running daemon has the correct version
	return versionCheck(ctx, client.GetExe(ctx))
}

// warn if the version diff between cli and manager is > 3 or if there's an OSS/Enterprise mismatch.
func warnMngrVersion(ctx context.Context, ci *connector.ConnectInfo) error {
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

	const maxDiff = uint64(3)
	if diff > maxDiff {
		progress.Warningf(ctx,
			"The Traffic Manager version (%s) is more than %v minor versions diff from client version (%s), please consider upgrading.",
			mv.Version, maxDiff, client.Version())
	} else if diff > 0 {
		dlog.Debugf(ctx, "Diff between client and manager minor versions: %d", diff)
	}

	cv := ci.Version
	if strings.HasPrefix(cv.Name, "OSS ") && !strings.HasPrefix(mv.Name, "OSS ") {
		progress.Warningf(ctx,
			"You are using the OSS client %s to connect to an enterprise traffic manager %s. Please consider installing an\n"+
				"enterprise client from getambassador.io, or use \"telepresence helm install\" to install an OSS traffic-manager.",
			cv.Version,
			mv.Version)
	}
	return nil
}

func connectResult(ctx context.Context, ci *connector.ConnectInfo, withProgress bool) (*daemon.Session, error) {
	var msg string
	cat := errcat.Unknown
	started := false
	switch ci.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		err := warnMngrVersion(ctx, ci)
		if err != nil {
			dlog.Error(ctx, err)
		}
		started = true
		fallthrough
	case connector.ConnectInfo_ALREADY_CONNECTED:
		if withProgress {
			msg := fmt.Sprintf("Connected to context %s, namespace %s (%s)", ci.ClusterContext, ci.Namespace, ci.ClusterServer)
			if ci.Error == connector.ConnectInfo_UNSPECIFIED {
				progress.PrintDone(ctx, msg)
			} else {
				progress.Done(ctx, msg)
			}
		}
		return &daemon.Session{Info: ci, Started: started}, nil
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

func connectSession(ctx context.Context, useLine string, request *daemon.Request, required bool) (session *daemon.Session, err error) {
	userD := daemon.MustGetUserClient(ctx)
	if userD.Containerized() {
		patcher.AnnotateConnectRequest(request.ConnectRequest, docker.TpCache, userD.DaemonID().KubeContext)
	}

	var ci *connector.ConnectInfo
	defer func() {
		if ci != nil {
			request.KubeFlags = ci.KubeFlags
			request.ManagerNamespace = ci.ManagerNamespace
			request.Name = ci.ConnectionName

			userD.SetConnectionInfo(ci.ConnectionName, ci.ClusterContext, ci.Namespace)
		}
		if session != nil {
			session.UserClient = userD
		}
	}()

	implicitConnect := false
	if request.Implicit {
		// implicit calls use the current Status instead of passing flags and mapped namespaces.
		if ci, err = userD.Status(ctx, &emptypb.Empty{}); err != nil {
			return nil, err
		}
		if ci.Error != connector.ConnectInfo_DISCONNECTED {
			return connectResult(ctx, ci, false)
		}
		if required {
			implicitConnect = true
		}
	}

	if !required {
		return nil, nil
	}

	daemonID := userD.DaemonID()
	progress.Workingf(ctx, "Connecting to context %s, namespace %s", daemonID.KubeContext, daemonID.Namespace)
	if implicitConnect {
		progress.Warningf(ctx,
			`Warning: You are executing the %q command without a preceding "telepresence connect", causing an implicit `+
				"connect to namespace %q. The implicit connect behavior is deprecated and will be removed in a future release.",
			useLine, daemonID.Namespace)
	}
	if ci, err = userD.Connect(ctx, request.ConnectRequest); err != nil {
		if !userD.Containerized() {
			file := userD.DaemonID().InfoFileName()
			dlog.Debugf(ctx, "Deleting daemon info %s due to connect error: %v", file, err)
			_ = daemon.DeleteInfo(ctx, file)
		}
		return nil, err
	}
	return connectResult(ctx, ci, true)
}

func createTelerouteNetwork(ctx context.Context, info *daemon.Info) error {
	// Make an attempt to create the network with IPv6 enabled. This will fail unless the user has enabled
	// IPv6 in /etc/docker/daemon.json.
	cn := info.Name
	cli, err := docker.GetClient(ctx)
	if err != nil {
		return errcat.NoDaemonLogs.New(err)
	}

	teleroutePlugin := docker.NetworkPluginName(ctx)
	teleroutePort := client.GetConfig(ctx).Grpc().TeleroutePort
	err = teleroute.CreateNetwork(ctx, info, cli, teleroutePlugin, teleroutePort)
	if err != nil && strings.Contains(err.Error(), fmt.Sprintf("%s already exists", cn)) && teleroute.IsTelerouteNetwork(ctx, cli, cn) {
		var disconnected []string
		disconnected, err = teleroute.RemoveNetwork(ctx, cli, cn)
		if err == nil {
			err = teleroute.CreateNetwork(ctx, info, cli, teleroutePlugin, teleroutePort)
			if err == nil {
				teleroute.ReconnectNetwork(ctx, cli, cn, disconnected)
			}
		}
	}
	if err != nil {
		return errcat.NoDaemonLogs.Newf("Unable to create network %s: %v", cn, err)
	}
	err = teleroute.NetworkGC(ctx, cli)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("Unable to garbage collect teleroute networks: %v", err)
	}
	return nil
}

func maybeDeleteNetwork(ctx context.Context, info *daemon.Info) {
	// Wait for container exit.
	ctx = docker.EnableClient(ctx)
	dc, err := docker.GetClient(ctx)
	if err == nil {
		err = docker.WaitForExit(ctx, dc, info.ContainerID, 3*time.Second)
		if err == nil {
			err = teleroute.NetworkGC(ctx, dc)
		}
	}
	if err != nil {
		dlog.Error(ctx, err)
	}
}
