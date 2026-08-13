package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	ftp "github.com/telepresenceio/go-ftpserver"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/sftpserver"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var DisplayName = "OSS Traffic Agent" //nolint:gochecknoglobals // extension point

// AppEnvironment returns the environment visible to this agent together with environment variables
// explicitly declared for the app container and minus the environment variables provided by this
// config.
func AppEnvironment(ctx context.Context, ag *agentconfig.Container) (map[string]string, error) {
	return appEnvironment(dos.Environ(ctx), ag), nil
}

// appEnvironment applies the app-prefix, skip-key, and mount-derived transformations to osEnv
// (each entry in "KEY=VALUE" form) for the container described by ag.
func appEnvironment(osEnv []string, ag *agentconfig.Container) map[string]string {
	prefix := agentconfig.EnvPrefixApp + ag.EnvPrefix
	fullEnv := make(map[string]string, len(osEnv))

	// Keys that aren't useful when running on the local machine.
	skipKeys := map[string]bool{
		"HOME":                     true,
		"PATH":                     true,
		"HOSTNAME":                 true,
		agentconfig.EnvAgentConfig: true,
	}

	// Add prefixed variables separately last, so that we can
	// ensure that they have higher precedence.
	for _, env := range osEnv {
		if !(strings.HasPrefix(env, agentconfig.EnvPrefix) || strings.HasPrefix(env, prefix)) {
			pair := strings.SplitN(env, "=", 2)
			if len(pair) == 2 {
				k := pair[0]
				if _, skip := skipKeys[k]; !skip {
					fullEnv[k] = pair[1]
				}
			}
		}
	}
	for _, env := range osEnv {
		if strings.HasPrefix(env, prefix) {
			pair := strings.SplitN(env, "=", 2)
			if len(pair) == 2 {
				k := pair[0][len(prefix):]
				fullEnv[k] = pair[1]
			}
		}
	}
	fullEnv[agentconfig.EnvInterceptContainer] = ag.Name
	mounts := ag.Mounts
	if len(mounts) > 0 {
		var localMounts, remoteMounts []string
		for path, policy := range mounts {
			switch policy {
			case types.MountPolicyIgnore:
			case types.MountPolicyRemote, types.MountPolicyRemoteReadOnly:
				remoteMounts = append(remoteMounts, path)
			case types.MountPolicyLocal:
				localMounts = append(localMounts, path)
			}
		}
		if len(localMounts) > 0 {
			sort.Strings(localMounts)
			fullEnv[agentconfig.EnvLocalMounts] = strings.Join(localMounts, ":")
		}
		if len(remoteMounts) > 0 {
			sort.Strings(remoteMounts)
			fullEnv[agentconfig.EnvInterceptMounts] = strings.Join(remoteMounts, ":")
		}
	}
	return fullEnv
}

// sftpServer creates a listener on the next available port, writes that port on the
// given channel, and then starts accepting connections on that port. Each connection is
// screened by serveSftpConn's tunnel-source gate and, once past that, served by a
// sftpserver.Server confined to agentconfig.ExportsMountPoint and
// agentconfig.MountPrefixApp.
func sftpServer(ctx context.Context, sftpPortCh chan<- uint16, auth *fileShareAuth, podIP netip.Addr) error {
	defer close(sftpPortCh)

	// start an sftp-server for remote sshfs mounts
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", ":0")
	if err != nil {
		return err
	}

	// Accept doesn't actually return when the context is cancelled so
	// it's explicitly closed here.
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	ap, err := iputil.SplitToIPPort(l.Addr())
	if err != nil {
		return err
	}
	sftpPortCh <- ap.Port()

	srv, err := sftpserver.New(agentconfig.ExportsMountPoint, agentconfig.MountPrefixApp)
	if err != nil {
		return err
	}

	clog.Infof(ctx, "Listening at: %s", l.Addr())
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() == nil {
				return fmt.Errorf("listener on sftp-server connection failed: %v", err)
			}
			return nil
		}
		go serveSftpConn(ctx, conn, srv, auth, podIP)
	}
}

// serveSftpConn gates conn on its source address before serving it: every legitimate
// consumer reaches this port through the telepresence tunnel, which the agent itself
// dials, so its connections carry the pod's own source address (or loopback); anything
// else is a direct connection that bypassed the tunnel, refused once auth.enforcing()
// and served as before otherwise. See fromOwnPod.
func serveSftpConn(ctx context.Context, conn net.Conn, srv *sftpserver.Server, auth *fileShareAuth, podIP netip.Addr) {
	defer conn.Close()
	clog.Debugf(ctx, "Serving sftp connection from %s", conn.RemoteAddr())

	if auth.enforcing() && !fromOwnPod(conn.RemoteAddr(), podIP) {
		clog.Warnf(ctx, "sftp: closing direct connection from %s; a direct connection to the SFTP port requires the tunnel",
			conn.RemoteAddr())
		return
	}
	if err := srv.Serve(ctx, conn); err != nil && !errors.Is(err, io.EOF) {
		clog.Errorf(ctx, "sftp server completed with error %v", err)
	}
}

func Main(ctx context.Context, _ ...string) error {
	debug.SetTraceback("single")
	clog.Infof(ctx, "Traffic Agent %s", version.Version)

	return sigctx.DoWithSignalHandler(ctx, func(ctx context.Context) error {
		// Handle configuration
		config, err := LoadConfig(ctx)
		if err != nil {
			return fmt.Errorf("unable to load config: %w", err)
		}

		g := log.NewGroup(ctx)
		s, err := NewState(ctx, config)
		if err != nil {
			return err
		}
		info, err := StartServices(g, config, s)
		if err != nil {
			return err
		}

		certsReadyCh := make(chan struct{})
		s.TLSManager().StartWatchers(g, certsReadyCh)

		g.Go("sidecar", func(ctx context.Context) error {
			<-certsReadyCh
			return Sidecar(g, s, info)
		})

		// Wait for exit
		return g.Wait()
	})
}

func Sidecar(g log.Group, s State, info *rpc.AgentInfo) error {
	// Manage the forwarders
	ac := s.AgentConfig()
	for _, cn := range ac.Containers {
		ci := info.Containers[cn.Name]
		cs := s.NewContainerState(s, cn, ci.MountPoint, ci.Environment)
		s.AddContainerState(cn.Name, cs)
		for pp, ics := range MakeInterceptStates(cn) {
			cs.AddPortHandler(g, pp, ics)
		}
	}
	TalkToManagerLoop(g, s, info)
	return nil
}

func MakeInterceptStates(cn *agentconfig.Container) map[types.PortAndProto]agentconfig.InterceptTarget {
	// Group the container's intercepts by agent port
	icStates := make(map[types.PortAndProto]agentconfig.InterceptTarget, len(cn.Intercepts))
	for _, ic := range cn.Intercepts {
		ap := ic.AgentPort
		if cn.Replace == agentconfig.ReplacePolicyContainer {
			// Listen to the replaced container's original port.
			ap = ic.ContainerPort
		}
		k := types.PortAndProto{Port: ap, Proto: ic.Protocol}
		icStates[k] = append(icStates[k], ic)
	}
	return icStates
}

func TalkToManagerLoop(ctx context.Context, s State, info *rpc.AgentInfo) {
	ac := s.AgentConfig()
	gRPCAddress := fmt.Sprintf("%s:%v", ac.ManagerHost, ac.ManagerPort)

	// Don't reconnect more than every 5s
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		err := TalkToManager(ctx, gRPCAddress, info, s)
		if err != nil {
			switch status.Code(err) {
			case codes.AlreadyExists, codes.Aborted, codes.Canceled:
				// This won't change, so abort here.
				return
			}
			clog.Errorf(ctx, "error talking to traffic-manager: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func advertisedInterceptTargets(ac *agentconfig.Sidecar) []*rpc.AgentInfo_InterceptTarget {
	interceptTargets := make([]*rpc.AgentInfo_InterceptTarget, 0)
	seenTargets := make(map[string]struct{})
	for _, cn := range ac.Containers {
		for _, ic := range cn.Intercepts {
			if ic.ServiceUID == "" {
				continue
			}
			key := fmt.Sprintf("%s/%d/%s/%s/%d",
				ic.ServiceUID, ic.ServicePort, ic.Protocol, cn.Name, ic.ContainerPort)
			if _, ok := seenTargets[key]; ok {
				continue
			}
			seenTargets[key] = struct{}{}
			interceptTargets = append(interceptTargets, &rpc.AgentInfo_InterceptTarget{
				ServiceUid:      string(ic.ServiceUID),
				ServiceName:     ic.ServiceName,
				ServicePortName: ic.ServicePortName,
				ServicePort:     int32(ic.ServicePort),
				Protocol:        ic.Protocol.String(),
				ContainerName:   cn.Name,
				ContainerPort:   int32(ic.ContainerPort),
			})
		}
	}
	sort.Slice(interceptTargets, func(i, j int) bool {
		a, b := interceptTargets[i], interceptTargets[j]
		if a.ServiceUid != b.ServiceUid {
			return a.ServiceUid < b.ServiceUid
		}
		if a.ServicePort != b.ServicePort {
			return a.ServicePort < b.ServicePort
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		if a.ContainerName != b.ContainerName {
			return a.ContainerName < b.ContainerName
		}
		return a.ContainerPort < b.ContainerPort
	})
	return interceptTargets
}

func StartServices(g log.Group, config Config, srv State) (*rpc.AgentInfo, error) {
	ac := config.AgentConfig()

	svc := server.New(clog.WithGroup(g, "tunneling"), grpc.KeepaliveParams(keepalive.ServerParameters{
		Time:    ac.ClientConnectionTTL,
		Timeout: 20 * time.Second,
	}))
	agent.RegisterAgentServer(svc, srv)
	srv.SetGRPCServer(svc)

	grpcPortCh := make(chan uint16)
	g.Go("tunneling", func(ctx context.Context) error {
		defer close(grpcPortCh)
		lc := net.ListenConfig{}
		grpcListener, err := lc.Listen(ctx, "tcp", ":")
		if err != nil {
			return err
		}
		grpcAddress := grpcListener.Addr().(*net.TCPAddr)
		grpcPortCh <- uint16(grpcAddress.Port)

		clog.Debugf(ctx, "Listener opened on %s", grpcAddress)
		clog.Debugf(ctx, "Serving client connections using idle TTL %s", ac.ClientConnectionTTL)
		return server.Serve(ctx, svc, grpcListener)
	})

	sftpPortCh := make(chan uint16)
	ftpPortCh := make(chan uint16)
	if config.HasRemoteMounts() {
		g.Go("sftp-server", func(ctx context.Context) error {
			return sftpServer(ctx, sftpPortCh, srv.FileShareAuth(), config.PodIP())
		})
		g.Go("ftp-server", func(ctx context.Context) error {
			publicHost := ""
			if !config.PodIP().Is6() {
				publicHost = config.PodIP().String()
			}
			// MountPrefixApp is the only tree the exports symlinks may lead into;
			// see addAppMounts, which creates them.
			return ftp.StartWithValidator(ctx, publicHost, agentconfig.ExportsMountPoint, ftpPortCh,
				srv.FileShareAuth().validatePassword, agentconfig.MountPrefixApp)
		})
	} else {
		close(sftpPortCh)
		close(ftpPortCh)
		clog.Info(g, "Not starting ftp and sftp servers because there's nothing to mount")
	}
	grpcPort, err := waitForPort(g, grpcPortCh)
	if err != nil {
		return nil, err
	}
	ftpPort, err := waitForPort(g, ftpPortCh)
	if err != nil {
		return nil, err
	}
	sftpPort, err := waitForPort(g, sftpPortCh)
	if err != nil {
		return nil, err
	}
	srv.SetFileSharingPorts(ftpPort, sftpPort)

	if ac.APIPort != 0 {
		g.Go("API-server", func(ctx context.Context) error {
			return restapi.NewServer(srv.AgentState()).ListenAndServe(ctx, int(ac.APIPort))
		})
	}

	containers := make(map[string]*rpc.AgentInfo_ContainerInfo, len(ac.Containers))
	for _, cn := range ac.Containers {
		appMounts := cn.Mounts
		env, err := config.AppEnviron(g, cn)
		if err != nil {
			return nil, err
		}
		containers[cn.Name] = &rpc.AgentInfo_ContainerInfo{
			Environment: env,
			MountPoint:  filepath.Join(agentconfig.ExportsMountPoint, filepath.Base(cn.MountPoint)),
			Mounts:      appMounts.ToRPC(),
		}
	}
	interceptTargets := advertisedInterceptTargets(ac)

	return &rpc.AgentInfo{
		Name:      ac.AgentName,
		Namespace: ac.Namespace,
		Kind:      string(ac.WorkloadKind),
		PodName:   config.PodName(),
		PodIp:     config.PodIP().String(),
		PodUid:    string(config.PodUID()),
		NodeAgent: config.NodeAgent(),
		ApiPort:   int32(grpcPort),
		FtpPort:   int32(ftpPort),
		SftpPort:  int32(sftpPort),
		QuicPort:  int32(ac.QuicPort),
		Product:   "telepresence",
		Version:   version.Version,
		Mechanisms: []*rpc.AgentInfo_Mechanism{
			{
				Name:    "tcp",
				Product: "telepresence",
				Version: version.Version,
			},
			{
				Name:    "http",
				Product: "telepresence",
				Version: version.Version,
			},
		},
		Containers:       containers,
		InterceptTargets: interceptTargets,
	}, nil
}

func waitForPort(ctx context.Context, ch <-chan uint16) (uint16, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case port := <-ch:
		return port, nil
	}
}
