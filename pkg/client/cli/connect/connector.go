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

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	daemonRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/teleroute"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	tpGrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

var ErrNoUserDaemon = errors.New("telepresence user daemon is not running")

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
			clog.Error(ctx, err)
		}
	}()
	progress.Stop(ctx)
	err := proc.StdCommand(ctx, docker.Exe, "compose", "--file", info.ComposeFile, "down", "--remove-orphans", "--volumes").Run()
	if err != nil {
		clog.Error(ctx, err)
	}
	progress.Start(ctx, "Quitting")
}

func findHostConnectorInfo(ctx context.Context) (*daemon.Info, error) {
	il := daemon.NewUserInfoLoader(ctx)
	infos, err := il.LoadInfos()
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if !info.InDocker() {
			return info, nil
		}
	}
	return nil, fs.ErrNotExist
}

func quitHostConnector(ctx context.Context) {
	info, err := findHostConnectorInfo(ctx)
	if err == nil {
		ctx, err = ExistingDaemon(ctx, info)
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			progress.Errorf(ctx, "unable to quit existing user daemon: %v", err)
		}
		return
	}
	ud := daemon.MustGetUserClient(ctx)
	progress.Working(ctx, "Quitting")
	qr, err := ud.Quit(ctx, &emptypb.Empty{})
	if err != nil {
		progress.Errorf(ctx, "failed to quit user daemon: %v", tpGrpc.FromGRPC(err))
		return
	}
	_ = ud.Close()
	if !qr.RootDaemonWillContinue {
		// User daemon is responsible for killing the root daemon, but we kill it here too to cater for
		// the fact that the user daemon might have been killed ungracefully.
		if conn, err := daemon.DialRootDaemon(ctx, false); err == nil {
			if _, err = daemonRpc.NewDaemonClient(conn).Quit(ctx, &emptypb.Empty{}); err != nil {
				clog.Errorf(ctx, "error when quitting root daemon: %v", err)
			}
			_ = conn.Close()
		}
	}
	progress.PrintDone(ctx, "Quit")
}

func quitDockerDaemons(ctx context.Context) {
	il := daemon.NewUserInfoLoader(ctx)
	infos, err := il.LoadInfos()
	if err != nil {
		clog.Error(ctx, err)
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
}

func EnsureUserDaemon(ctx context.Context, required bool) (rc context.Context, err error) {
	cr := daemon.MustGetRequest(ctx)
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
	rc, launched, err = findOrLaunchConnectorDaemon(ctx, daemonID, client.GetExe(ctx), required)
	return rc, err
}

func EnsureSession(ctx context.Context, useLine string, required bool) (context.Context, error) {
	if daemon.GetSession(ctx) != nil {
		return ctx, nil
	}

	rq := daemon.MustGetRequest(ctx)
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
	_, err = ds.RerouteLocalPort(ctx, &daemonRpc.ReroutePortRequest{
		DstHostPort: hpb,
		SrcPort:     uint32(localPort.Port),
	})
	return tpGrpc.FromGRPC(err)
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
	_, err = ds.RerouteRemotePort(ctx, &daemonRpc.ReroutePortRequest{
		DstHostPort: hpb,
		SrcPort:     uint32(newPort.Port),
	})
	return tpGrpc.FromGRPC(err)
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
	rsp, err := ds.ResolvePort(ctx, &daemonRpc.ResolvePortRequest{
		Host: hostPortStr[:ix],
		Port: portStr,
	})
	if err != nil {
		return hostPort, tpGrpc.FromGRPC(err)
	}
	err = hostPort.UnmarshalBinary(rsp.HostPort)
	return hostPort, err
}

func ExistingDaemon(ctx context.Context, info *daemon.Info) (context.Context, error) {
	var conn *grpc.ClientConn
	var err error
	if info.InDocker() {
		// The host relies on that the daemon has exposed a port to localhost
		conn, err = docker.ConnectDaemon(ctx, info)
	} else {
		conn, err = daemon.DialUserDaemon(ctx, false)
	}
	if err != nil {
		return ctx, err
	}
	return newUserDaemon(ctx, conn, info)
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
			_ = progress.MaybeWriteError(ctx, fmt.Errorf("failed to disconnect: %w", tpGrpc.FromGRPC(err)))
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
	cr := daemon.MustGetRequest(ctx)
	if match == nil && !cr.Implicit {
		match = regexp.MustCompile(`\A` + regexp.QuoteMeta(daemonID.Name) + `\z`)
	}
	il := daemon.NewUserInfoLoader(ctx)
	info, err := il.LoadMatchingInfo(match)
	if err != nil {
		return ctx, err
	}
	if len(cr.ExposedPorts) > 0 && !slices.Equal(info.ExposedPorts, cr.ExposedPorts) {
		return ctx, errcat.User.New("exposed ports differ. Please quit and reconnect")
	}
	return ExistingDaemon(ctx, info)
}

func launchDockerDaemon(ctx context.Context, daemonID *daemon.Identifier, cr *daemon.Request) (context.Context, error) {
	// Ensure that the logfile is present before the daemon starts so that it isn't created with
	// permissions from the docker container.
	logDir := filelocation.AppUserLogDir(ctx)
	logFile := filepath.Join(logDir, "connector.log")
	if _, err := os.Stat(logFile); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return ctx, err
		}
		fh, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY, 0o666)
		if err != nil {
			return ctx, err
		}
		_ = fh.Close()
	}
	_, err := docker.EnsureNetworkPlugin(ctx)
	if err != nil {
		return ctx, err
	}

	// An initialized kubernetes interface is required by LaunchDaemon, because it is necessary
	// when checking if the containerized daemon is connecting to a k3s control plane node.
	kc, err := k8s.NewKubeconfig(ctx, false, cr.KubeFlags, cr.ManagerNamespace, cr.KubeconfigData)
	if err != nil {
		return ctx, err
	}
	var ki *kubernetes.Clientset
	ki, err = kubernetes.NewForConfig(kc.RestConfig)
	if err != nil {
		return ctx, err
	}
	ctx = kc.Context
	info, conn, err := docker.LaunchDaemon(k8sapi.WithK8sInterface(ctx, ki), daemonID)
	if err != nil {
		return ctx, err
	}
	return newUserDaemon(ctx, conn, info)
}

func launchHostDaemon(ctx context.Context, daemonID *daemon.Identifier, connectorDaemon string, cr *daemon.Request) (context.Context, error) {
	args := []string{connectorDaemon, client.UserDaemonName, "--" + global.FlagConfig, client.GetConfigFile(ctx)}
	if cr.UserDaemonProfilingPort > 0 {
		args = append(args, "--pprof", strconv.Itoa(int(cr.UserDaemonProfilingPort)))
	}
	if proc.IsAdmin() {
		// No use having multiple daemons when running as root.
		args = append(args, "--embed-network")
	}
	clog.Debugf(ctx, "Creating daemon info file %s (runs on host, or both CLI and daemon runs in container)", daemonID.Name)
	fp, err := ioutil.FreePortsTCP(1)
	if err != nil {
		return ctx, errcat.NoDaemonLogs.New(err)
	}
	info := &daemon.Info{
		DaemonPort:   fp[0].Port(),
		Name:         daemonID.Name,
		KubeContext:  daemonID.KubeContext,
		Namespace:    daemonID.Namespace,
		ExposedPorts: cr.ExposedPorts,
		Hostname:     cr.Hostname,
	}
	args = append(args, "--address", fmt.Sprintf(":%d", info.DaemonPort))
	fn := daemonID.InfoFileName()
	il := daemon.NewUserInfoLoader(ctx)
	err = il.SaveInfo(info, fn)
	if err != nil {
		return ctx, errcat.NoDaemonLogs.New(err)
	}

	defer func() {
		if err != nil {
			file := daemonID.InfoFileName()
			clog.Debugf(ctx, "Deleting daemon info %s due to launch error: %v", file, err)
			_ = il.DeleteInfo(file)
		}
	}()

	if err = proc.StartInBackground(false, args...); err != nil {
		return ctx, errcat.NoDaemonLogs.Errorf(err, "failed to launch the connector service")
	}
	conn, err := il.DialDaemon(ctx, true)
	if err != nil {
		return ctx, errcat.NoDaemonLogs.New(err)
	}
	return newUserDaemon(ctx, conn, info)
}

func findOrLaunchConnectorDaemon(ctx context.Context, daemonID *daemon.Identifier, connectorDaemon string, required bool) (context.Context, bool, error) {
	cr := daemon.MustGetRequest(ctx)

	// Try dialing the host daemon using the well-known socket.
	ctx, err := DiscoverDaemon(ctx, cr.Use, daemonID)
	if err == nil {
		ud := daemon.MustGetUserClient(ctx)
		if ud.Containerized() {
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

	if cr.Docker {
		if client.GetConfig(ctx).Intercept().UseFtp {
			err = errcat.Silent.New("FTP is not supported when using Docker. Please set intercept.useFtp=false in your config.yml and try again.")
			progress.Error(ctx, err)
			return ctx, false, err
		}
		ctx, err = launchDockerDaemon(ctx, daemonID, cr)
	} else {
		ctx, err = launchHostDaemon(ctx, daemonID, connectorDaemon, cr)
	}
	return ctx, err == nil, err
}

// getConnectorVersion is the first call to the user daemon, so a backoff is used here to trap errors
// caused during the initial state change of the gRPC connection.
func getConnectorVersion(ctx context.Context, cc connector.ConnectorClient) (*common.VersionInfo, error) {
	b := backoff.NewExponentialBackOff(
		backoff.WithMaxElapsedTime(3*time.Second),
		backoff.WithInitialInterval(50*time.Millisecond),
		backoff.WithMaxInterval(time.Second))
	var vi *common.VersionInfo
	err := backoff.Retry(func() (err error) {
		quick, cancel := context.WithTimeout(ctx, 50*time.Millisecond) // This is a local call. Should be quick.
		defer cancel()
		vi, err = cc.Version(quick, &emptypb.Empty{})
		return tpGrpc.FromGRPC(err)
	}, backoff.WithContext(b, ctx))
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
		clog.Debugf(ctx, "Diff between client and manager minor versions: %d", diff)
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

func connectResult(ctx context.Context, ci *connector.ConnectInfo, withProgress bool) *daemon.Session {
	err := warnMngrVersion(ctx, ci)
	if err != nil {
		clog.Error(ctx, err)
	}
	if withProgress {
		progress.PrintDonef(ctx, "Connected to context %s, namespace %s (%s)", ci.ClusterContext, ci.Namespace, ci.ClusterServer)
	}
	return &daemon.Session{Info: ci, Started: ci.Initial}
}

func connectSession(ctx context.Context, useLine string, request *daemon.Request, required bool) (session *daemon.Session, err error) {
	userD := daemon.MustGetUserClient(ctx)
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
		ci, err = userD.Status(ctx, &emptypb.Empty{})
		if err == nil && ci.ManagerVersion == nil {
			// If the manager version is nil, the user daemon is not connected. This is the same
			// as it being unavailable when the request is implicit.
			err = status.Errorf(codes.Unavailable, "user daemon is not connected")
		}
		if err == nil {
			return connectResult(ctx, ci, false), nil
		}
		if status.Code(err) != codes.Unavailable {
			return nil, tpGrpc.FromGRPC(err)
		}
		err = nil
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
			clog.Debugf(ctx, "Deleting daemon info %s due to connect error: %v", file, err)
			_ = daemon.NewUserInfoLoader(ctx).DeleteInfo(file)
		}
		return nil, tpGrpc.FromGRPC(err)
	}
	return connectResult(ctx, ci, true), nil
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
	dc, err := docker.GetClient(ctx)
	if err == nil {
		err = docker.WaitForExit(ctx, dc, info.ContainerID, 3*time.Second)
		if err == nil {
			err = teleroute.NetworkGC(ctx, dc)
		}
	}
	if err != nil {
		clog.Error(ctx, err)
	}
}
