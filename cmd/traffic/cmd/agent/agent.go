package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	ftp "github.com/datawire/go-ftpserver"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var DisplayName = "OSS Traffic Agent" //nolint:gochecknoglobals // extension point

// AppEnvironment returns the environment visible to this agent together with environment variables
// explicitly declared for the app container and minus the environment variables provided by this
// config.
func AppEnvironment(ctx context.Context, mounts types.MountPolicies, ag *agentconfig.Container) (map[string]string, error) {
	osEnv := dos.Environ(ctx)
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
	return fullEnv, nil
}

// sftpServer creates a listener on the next available port, writes that port on the
// given channel, and then starts accepting connections on that port. Each connection
// starts a sftp-server that communicates with that connection using its stdin and stdout.
func sftpServer(ctx context.Context, sftpPortCh chan<- uint16) error {
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

	dlog.Infof(ctx, "Listening at: %s", l.Addr())
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() == nil {
				return fmt.Errorf("listener on sftp-server connection failed: %v", err)
			}
			return nil
		}
		go func() {
			s, err := sftp.NewServer(conn)
			if err != nil {
				dlog.Error(ctx, err)
			}
			dlog.Debugf(ctx, "Serving sftp connection from %s", conn.RemoteAddr())
			if err = s.Serve(); err != nil {
				if !errors.Is(err, io.EOF) {
					dlog.Errorf(ctx, "sftp server completed with error %v", err)
				}
			}
		}()
	}
}

func Main(ctx context.Context, _ ...string) error {
	debug.SetTraceback("single")
	dlog.Infof(ctx, "Traffic Agent %s", version.Version)

	ctx, cancel := context.WithCancel(ctx)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, unix.SIGTERM)
	defer func() {
		signal.Stop(sigs)
		cancel()
	}()

	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Handle configuration
	config, err := LoadConfig(ctx)
	if err != nil {
		return fmt.Errorf("unable to load config: %w", err)
	}

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		SoftShutdownTimeout: 10 * time.Second, // Agent must be able to depart.
	})
	s := NewState(config)
	info, err := StartServices(ctx, g, config, s)
	if err != nil {
		return err
	}

	// Talk to the Traffic Manager
	g.Go("sidecar", func(ctx context.Context) error {
		return sidecar(ctx, s, info)
	})

	// Wait for exit
	return g.Wait()
}

func sidecar(ctx context.Context, s State, info *rpc.AgentInfo) error {
	// Manage the forwarders
	ac := s.AgentConfig()
	for _, cn := range ac.Containers {
		ci := info.Containers[cn.Name]
		s.AddContainerState(cn.Name, NewContainerState(s, cn, ci.MountPoint, ci.Environment))

		// Group the container's intercepts by agent port
		icStates := make(map[types.PortAndProto][]*agentconfig.Intercept, len(cn.Intercepts))
		for _, ic := range cn.Intercepts {
			ap := ic.AgentPort
			if cn.Replace == agentconfig.ReplacePolicyContainer {
				// Listen to replaced container's original port.
				ap = ic.ContainerPort
			}
			k := types.PortAndProto{Port: ap, Proto: ic.Protocol}
			icStates[k] = append(icStates[k], ic)
		}

		for pp, ics := range icStates {
			ic := ics[0] // They all have the same protocol container port, so the first one will do.
			var fwd forwarder.Interceptor
			var cp uint16
			if cn.Replace == agentconfig.ReplacePolicyIntercept {
				if ic.TargetPortNumeric {
					// We must differentiate between connections originating from the agent's forwarder to the container
					// port and those from other sources. The former should not be routed back, while the latter should
					// always be routed to the agent. We do this by using a proxy port that will be recognized by the
					// iptables filtering in our init-container.
					cp = ac.ProxyPort(ic)
				} else {
					cp = ic.ContainerPort
				}
				// Redirect non-intercepted traffic to the pod, so that injected sidecars that hijack the ports for
				// incoming connections will continue to work.
				targetHost := s.PodIP()
				fwd = forwarder.NewInterceptor(pp, tunnel.AgentToProxied, targetHost, cp)
			} else {
				fwd = forwarder.NewInterceptor(pp, tunnel.AgentToClient, "", 0)
				cp = ic.ContainerPort
			}

			dgroup.ParentGroup(ctx).Go(fmt.Sprintf("forward-%s", iputil.JoinHostPort(cn.Name, cp)), func(ctx context.Context) error {
				return fwd.Serve(tunnel.WithPool(ctx, tunnel.NewPool()), nil)
			})
			s.AddInterceptState(s.NewInterceptState(fwd, NewInterceptTarget(ics), cn.Name))
		}
	}
	TalkToManagerLoop(ctx, s, info)
	return nil
}

func TalkToManagerLoop(ctx context.Context, s State, info *rpc.AgentInfo) {
	ac := s.AgentConfig()
	gRPCAddress := fmt.Sprintf("%s:%v", ac.ManagerHost, ac.ManagerPort)

	// Don't reconnect more than once every five seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		if err := TalkToManager(ctx, gRPCAddress, info, s); err != nil {
			switch status.Code(err) {
			case codes.AlreadyExists, codes.Aborted:
				// This won't change, so abort here.
				return
			}
			dlog.Error(ctx, err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func StartServices(ctx context.Context, g *dgroup.Group, config Config, srv State) (*rpc.AgentInfo, error) {
	var grpcOpts []grpc.ServerOption
	ac := config.AgentConfig()

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

		dlog.Debugf(ctx, "Listener opened on %s", grpcAddress)

		svc := server.New(ctx, grpcOpts...)
		agent.RegisterAgentServer(svc, srv)
		return server.Serve(ctx, svc, grpcListener)
	})

	sftpPortCh := make(chan uint16)
	ftpPortCh := make(chan uint16)
	if config.HasRemoteMounts() {
		g.Go("sftp-server", func(ctx context.Context) error {
			return sftpServer(ctx, sftpPortCh)
		})
		g.Go("ftp-server", func(ctx context.Context) error {
			publicHost := ""
			if !iputil.IsIpV6Addr(config.PodIP()) {
				publicHost = config.PodIP()
			}
			return ftp.Start(ctx, publicHost, agentconfig.ExportsMountPoint, ftpPortCh)
		})
	} else {
		close(sftpPortCh)
		close(ftpPortCh)
		dlog.Info(ctx, "Not starting ftp and sftp servers because there's nothing to mount")
	}
	grpcPort, err := waitForPort(ctx, grpcPortCh)
	if err != nil {
		return nil, err
	}
	ftpPort, err := waitForPort(ctx, ftpPortCh)
	if err != nil {
		return nil, err
	}
	sftpPort, err := waitForPort(ctx, sftpPortCh)
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
		env, err := AppEnvironment(ctx, appMounts, cn)
		if err != nil {
			return nil, err
		}
		containers[cn.Name] = &rpc.AgentInfo_ContainerInfo{
			Environment: env,
			MountPoint:  filepath.Join(agentconfig.ExportsMountPoint, filepath.Base(cn.MountPoint)),
			Mounts:      appMounts.ToRPC(),
		}
	}

	return &rpc.AgentInfo{
		Name:      ac.AgentName,
		Namespace: ac.Namespace,
		Kind:      string(ac.WorkloadKind),
		PodName:   config.PodName(),
		PodIp:     config.PodIP(),
		PodUid:    string(config.PodUID()),
		ApiPort:   int32(grpcPort),
		FtpPort:   int32(ftpPort),
		SftpPort:  int32(sftpPort),
		Product:   "telepresence",
		Version:   version.Version,
		Mechanisms: []*rpc.AgentInfo_Mechanism{
			{
				Name:    "tcp",
				Product: "telepresence",
				Version: version.Version,
			},
		},
		Containers: containers,
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
