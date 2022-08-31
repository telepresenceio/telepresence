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

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	ftp "github.com/datawire/go-fuseftp/pkg/server"
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

// AppEnvironment returns the environment visible to this agent together with environment variables
// explicitly declared for the app container and minus the environment variables provided by this
// config.
func AppEnvironment(ctx context.Context, ag *agentconfig.Container) (map[string]string, error) {
	osEnv := dos.Environ(ctx)
	prefix := agentconfig.EnvPrefixApp + ag.EnvPrefix
	fullEnv := make(map[string]string, len(osEnv))

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

// SftpServer creates a listener on the next available port, writes that port on the
// given channel, and then starts accepting connections on that port. Each connection
// starts a sftp-server that communicates with that connection using its stdin and stdout.
func SftpServer(ctx context.Context, sftpPortCh chan<- uint16) error {
	defer close(sftpPortCh)

	// start an sftp-server for remote sshfs mounts
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp4", ":0")
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

func Main(ctx context.Context, args ...string) error {
	dlog.Infof(ctx, "Traffic Agent %s", version.Version)

	// Handle configuration
	config, err := LoadConfig(ctx)
	if err != nil {
		return err
	}

	info := &rpc.AgentInfo{
		Name:      config.AgentConfig().AgentName,
		PodIp:     config.PodIP(),
		Product:   "telepresence",
		Version:   version.Version,
		Namespace: config.AgentConfig().Namespace,
	}

	// Select initial mechanism
	mechanisms := []*rpc.AgentInfo_Mechanism{
		{
			Name:    "tcp",
			Product: "telepresence",
			Version: version.Version,
		},
	}
	info.Mechanisms = mechanisms

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	if config.AgentConfig().TracingPort != 0 {
		tracer, err := tracing.NewTraceServer(ctx, "traffic-agent", OtelResources(ctx, config)...)
		if err != nil {
			return err
		}
		g.Go("tracer-grpc", func(c context.Context) error {
			return tracer.ServeGrpc(c, config.AgentConfig().TracingPort)
		})
		defer tracer.Shutdown(ctx)
	}

	sftpPortCh := make(chan uint16)
	ftpPortCh := make(chan uint16)
	if config.HasMounts(ctx) {
		g.Go("sftp-server", func(ctx context.Context) error {
			return SftpServer(ctx, sftpPortCh)
		})
		g.Go("ftp-server", func(ctx context.Context) error {
			return ftp.Start(ctx, config.PodIP(), agentconfig.ExportsMountPoint, ftpPortCh)
		})
	} else {
		close(sftpPortCh)
		close(ftpPortCh)
		dlog.Info(ctx, "Not starting sftp-server because there's nothing to mount")
	}

	// Talk to the Traffic Manager
	g.Go("client", func(ctx context.Context) error {
		ac := config.AgentConfig()
		gRPCAddress := fmt.Sprintf("%s:%v", ac.ManagerHost, ac.ManagerPort)

		// Don't reconnect more than once every five seconds
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		state := NewSimpleState(config)
		if err := state.WaitForFtpPort(ctx, ftpPortCh); err != nil {
			return err
		}
		if err := state.WaitForSftpPort(ctx, sftpPortCh); err != nil {
			return err
		}

		// Manage the forwarders
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
				g.Go(fmt.Sprintf("forward-%s:%d", cn.Name, cp), func(ctx context.Context) error {
					return fwd.Serve(tunnel.WithPool(ctx, tunnel.NewPool()), nil)
				})
				cnMountPoint := filepath.Join(agentconfig.ExportsMountPoint, filepath.Base(cn.MountPoint))
				state.AddInterceptState(NewInterceptState(state, fwd, ics, cnMountPoint, env))
			}
		}

		if ac.APIPort != 0 {
			dgroup.ParentGroup(ctx).Go("API-server", func(ctx context.Context) error {
				return restapi.NewServer(state.AgentState()).ListenAndServe(ctx, int(ac.APIPort))
			})
		}

		for {
			if err := TalkToManager(ctx, gRPCAddress, info, state); err != nil {
				dlog.Info(ctx, err)
			}

			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	})

	// Wait for exit
	return g.Wait()
}
