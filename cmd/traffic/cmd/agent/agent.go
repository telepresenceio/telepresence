package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	ftp "github.com/datawire/go-ftpserver"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var DisplayName = "OSS Traffic Agent" //nolint:gochecknoglobals // extension point

// AppEnvironment returns the environment visible to this agent together with environment variables
// explicitly declared for the app container and minus the environment variables provided by this
// config.
func AppEnvironment(ctx context.Context, ag *agentconfig.Container) (map[string]string, error) {
	osEnv := dos.Environ(ctx)
	prefix := agentconfig.EnvPrefixApp + ag.EnvPrefix
	fullEnv := make(map[string]string, len(osEnv))

	// Keys that aren't useful when running on the local machine.
	skipKeys := map[string]bool{
		"HOME":     true,
		"PATH":     true,
		"HOSTNAME": true,
	}

	// Add prefixed variables separately last, so that we can
	// ensure that they have higher precedence.
	for _, env := range osEnv {
		if !strings.HasPrefix(env, agentconfig.EnvPrefix) {
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
	if len(ag.Mounts) > 0 {
		fullEnv[agentconfig.EnvInterceptMounts] = strings.Join(ag.Mounts, ":")
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

	_, sftpPort, err := iputil.SplitToIPPort(l.Addr())
	if err != nil {
		return err
	}
	sftpPortCh <- sftpPort

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
	dlog.Infof(ctx, "Traffic Agent %s", version.Version)

	// Handle configuration
	config, err := LoadConfig(ctx)
	if err != nil {
		return err
	}

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	s := NewSimpleState(config)
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

func sidecar(ctx context.Context, s SimpleState, info *rpc.AgentInfo) error {
	// Manage the forwarders
	ac := s.AgentConfig()
	for _, cn := range ac.Containers {
		env, err := AppEnvironment(ctx, cn)
		if err != nil {
			return err
		}
		// Group the containers intercepts by agent port
		icStates := make(map[agentconfig.PortAndProto][]*agentconfig.Intercept, len(cn.Intercepts))
		for _, ic := range cn.Intercepts {
			k := agentconfig.PortAndProto{Port: ic.AgentPort, Proto: ic.Protocol}
			icStates[k] = append(icStates[k], ic)
		}

		for pp, ics := range icStates {
			cp := ics[0].ContainerPort // They all have the same protocol container port, so the first one will do
			lisAddr, err := pp.Addr()
			if err != nil {
				return err
			}
			fwd := forwarder.NewInterceptor(lisAddr, "127.0.0.1", cp)
			dgroup.ParentGroup(ctx).Go(fmt.Sprintf("forward-%s", iputil.JoinHostPort(cn.Name, cp)), func(ctx context.Context) error {
				return fwd.Serve(tunnel.WithPool(ctx, tunnel.NewPool()), nil)
			})
			cnMountPoint := filepath.Join(agentconfig.ExportsMountPoint, filepath.Base(cn.MountPoint))
			s.AddInterceptState(s.NewInterceptState(fwd, NewInterceptTarget(ics), cnMountPoint, env))
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
			dlog.Info(ctx, err)
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
	if ac.TracingPort != 0 {
		g.Go("tracer-grpc", func(c context.Context) error {
			tracer, err := tracing.NewTraceServer(c, "traffic-agent", OtelResources(c, config)...)
			if err != nil {
				return err
			}
			defer func() {
				c, cancel := context.WithTimeout(context.WithoutCancel(c), time.Second)
				tracer.Shutdown(c)
				cancel()
			}()
			return tracer.ServeGrpc(c, ac.TracingPort)
		})

		grpcOpts = []grpc.ServerOption{
			grpc.StatsHandler(otelgrpc.NewServerHandler()),
		}
	}

	grpcPortCh := make(chan uint16)
	g.Go("tunneling", func(ctx context.Context) error {
		defer close(grpcPortCh)
		lc := net.ListenConfig{}
		grpcListener, err := lc.Listen(ctx, "tcp", ":")
		if err != nil {
			return err
		}
		defer func() {
			_ = grpcListener.Close()
		}()
		grpcAddress := grpcListener.Addr().(*net.TCPAddr)
		grpcPortCh <- uint16(grpcAddress.Port)

		dlog.Debugf(ctx, "Listener opened on %s", grpcAddress)

		grpcHandler := grpc.NewServer(grpcOpts...)
		agent.RegisterAgentServer(grpcHandler, srv)
		sc := &dhttp.ServerConfig{Handler: grpcHandler}
		dlog.Info(ctx, "gRPC server started")
		if err = sc.Serve(ctx, grpcListener); err != nil && ctx.Err() != nil {
			err = nil // Normal shutdown
		}
		return err
	})

	sftpPortCh := make(chan uint16)
	ftpPortCh := make(chan uint16)
	if config.HasMounts(ctx) {
		g.Go("sftp-server", func(ctx context.Context) error {
			return sftpServer(ctx, sftpPortCh)
		})
		g.Go("ftp-server", func(ctx context.Context) error {
			if iputil.IsIpV6Addr(config.PodIP()) {
				return ftp.Start(ctx, "", agentconfig.ExportsMountPoint, ftpPortCh)
			} else {
				return ftp.Start(ctx, config.PodIP(), agentconfig.ExportsMountPoint, ftpPortCh)
			}
		})
	} else {
		close(sftpPortCh)
		close(ftpPortCh)
		dlog.Info(ctx, "Not starting sftp-server because there's nothing to mount")
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

	return &rpc.AgentInfo{
		Name:      config.AgentConfig().AgentName,
		Namespace: config.AgentConfig().Namespace,
		PodName:   config.PodName(),
		PodIp:     config.PodIP(),
		ApiPort:   int32(grpcPort),
		Product:   "telepresence",
		Version:   version.Version,
		Mechanisms: []*rpc.AgentInfo_Mechanism{
			{
				Name:    "tcp",
				Product: "telepresence",
				Version: version.Version,
			},
		},
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
