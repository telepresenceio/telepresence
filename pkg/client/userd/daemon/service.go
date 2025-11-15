package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/dlib/v2/dgroup"
	"github.com/telepresenceio/dlib/v2/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	authGrpc "github.com/telepresenceio/telepresence/v2/pkg/authenticator/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/pprof"
)

func help() string {
	return `The Telepresence User Daemon is a background component that manages a connection.

Launch the daemon with:
    telepresence connect

Examine the daemon's log output in
    ` + filepath.Join(filelocation.AppUserLogDir(context.Background()), "connector.log") + `
to troubleshoot problems.
`
}

// service represents the long-running state of the Telepresence User Daemon.
type service struct {
	context.Context
	rpc.UnsafeConnectorServer
	srv           *grpc.Server
	timedLogLevel log.TimedLevel
	fuseFTPError  error

	// The quit function that quits the server.
	quit func(sessionIsLocked bool)

	clientConfigLock sync.Mutex
	clientConfig     clientcmd.ClientConfig

	sessionLock    sync.RWMutex
	session        userd.Session
	sessionCancel  context.CancelFunc
	sessionRunning chan struct{}

	fuseFtpMgr remotefs.FuseFTPManager

	// Run root session in-process
	rootSessionInProc bool

	// The TCP address that the daemon listens to. Will be nil if the daemon listens to a unix socket.
	daemonAddress netip.AddrPort

	// Port where root daemon (or rather the embedded root daemon) starts the teleroute service.
	teleroutePort uint16
}

func (s *service) ClientConfig() (clientcmd.ClientConfig, error) {
	s.clientConfigLock.Lock()
	cc := s.clientConfig
	s.clientConfigLock.Unlock()
	if cc == nil {
		return nil, errors.New("user daemon has no client config")
	}
	return cc, nil
}

func NewService(ctx context.Context, cancel context.CancelFunc, cfg client.Config, srv *grpc.Server) userd.Service {
	return newService(ctx, cancel, cfg, srv)
}

func newService(ctx context.Context, cancel context.CancelFunc, cfg client.Config, srv *grpc.Server) *service {
	s := &service{
		Context:        ctx,
		srv:            srv,
		timedLogLevel:  log.NewTimedLevel(cfg.LogLevels().UserDaemon.String(), log.SetLevel),
		fuseFtpMgr:     remotefs.NewFuseFTPManager(),
		sessionRunning: make(chan struct{}),
	}
	close(s.sessionRunning)
	s.quit = func(sessionIsLocked bool) {
		cancel()
		var sessionRunning <-chan struct{}
		if sessionIsLocked {
			sessionRunning = s.sessionRunning
		} else {
			s.sessionLock.RLock()
			sessionRunning = s.sessionRunning
			s.sessionLock.RUnlock()
		}
		<-sessionRunning
	}
	if srv != nil {
		// The podd daemon never registers the gRPC servers
		rpc.RegisterConnectorServer(srv, s)
		authGrpc.RegisterAuthenticatorServer(srv, s)
	} else {
		s.rootSessionInProc = true
	}
	return s
}

func (s *service) ConnectorServer() rpc.ConnectorServer {
	return s
}

func (s *service) ListenerAddress(ctx context.Context) string {
	if s.daemonAddress.IsValid() {
		return s.daemonAddress.String()
	}
	return "unix:" + socket.UserDaemonPath(ctx)
}

func (s *service) FuseFTPMgr() remotefs.FuseFTPManager {
	return s.fuseFtpMgr
}

func (s *service) RootSessionInProcess() bool {
	return s.rootSessionInProc
}

func (s *service) TeleroutePort() uint16 {
	return s.teleroutePort
}

func (s *service) Server() *grpc.Server {
	return s.srv
}

const (
	nameFlag          = "name"
	addressFlag       = "address"
	embedNetworkFlag  = "embed-network"
	pprofFlag         = "pprof"
	teleroutePortFlag = "teleroute-port"
	logfileFlag       = "logfile"
)

// Command returns the CLI sub-command for "userd".
func Command(ctx context.Context) *cobra.Command {
	c := &cobra.Command{
		Use:    client.UserDaemonName,
		Short:  "Launch Telepresence User Daemon",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		Long:   help(),
		RunE:   run,
	}
	flags := c.Flags()
	flags.String(nameFlag, client.UserDaemonName, "Daemon name")
	flags.String(logfileFlag, filepath.Join(filelocation.AppUserLogDir(ctx), "connector.log"),
		`Log file to write to { <path to a file> | "stdout" | "stderr" | "-" (same as "stderr") }`)
	flags.String(addressFlag, "", "Address to listen to. Defaults to "+socket.UserDaemonPath(ctx))
	flags.Bool(embedNetworkFlag, false, "Embed network functionality in the user daemon. Requires capability NET_ADMIN")
	flags.Uint16(pprofFlag, 0, "start pprof server on the given port")
	flags.Uint16(teleroutePortFlag, 0, "start teleroute server on the given port")
	return c
}

func (s *service) configReload(c context.Context) error {
	// Ensure that the directory to watch exists.
	if err := os.MkdirAll(filepath.Dir(client.GetConfigFile(c)), 0o755); err != nil {
		return err
	}
	return client.WatchConfig(c, func(ctx context.Context) error {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session == nil {
			client.ReloadDaemonLogLevel(ctx)
		}
		return nil
	})
}

func runAliveAndCancellation(ctx context.Context, cancel context.CancelFunc, daemonID *daemon.Identifier, wg *sync.WaitGroup) {
	wg.Add(1)
	defer wg.Done()
	daemonInfoFile := daemonID.InfoFileName()
	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	g.Go(fmt.Sprintf("info-kicker-%s", daemonID), func(ctx context.Context) error {
		// Ensure that the daemon info file is kept recent. This tells clients that we're alive.
		return daemon.KeepInfoAlive(ctx, daemonInfoFile)
	})
	g.Go(fmt.Sprintf("info-watcher-%s", daemonID), func(ctx context.Context) error {
		// Cancel the session if the daemon info file is removed.
		return daemon.WatchInfos(ctx, func(ctx context.Context) error {
			ok, err := daemon.InfoExists(ctx, daemonInfoFile)
			if err == nil && !ok {
				dlog.Debugf(ctx, "info-watcher cancels everything because daemon info %s does not exist", daemonInfoFile)
				cancel()
			}
			return err
		}, daemonInfoFile)
	})
	if err := g.Wait(); err != nil {
		dlog.Error(ctx, err)
	}
}

// run is the main function when executing as the connector.
func run(cmd *cobra.Command, _ []string) error {
	err := global.InitConfig(cmd)
	if err != nil {
		return err
	}
	c := cmd.Context()
	cfg, err := client.LoadConfig(c)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	c = client.WithConfig(c, cfg)

	// Listen on domain unix domain socket or windows named pipe. The listener must be opened
	// before other tasks because the CLI client will only wait for a short period of time for
	// the connection/socket/pipe to appear before it gives up.
	var grpcListener net.Listener
	flags := cmd.Flags()
	if pprofPort, _ := flags.GetUint16(pprofFlag); pprofPort > 0 {
		go func() {
			if err := pprof.PprofServer(c, pprofPort); err != nil {
				dlog.Error(c, err)
			}
		}()
	}

	name, _ := flags.GetString(nameFlag)
	c = dgroup.WithGoroutineName(c, "/"+name)
	logFile := flags.Lookup(logfileFlag).Value.String()
	c, err = logging.InitContext(c, logFile, cfg.LogLevels().UserDaemon, logging.RotateDaily, true)
	if err != nil {
		return err
	}
	c = docker.EnableClient(c)

	rootSessionInProc, _ := flags.GetBool(embedNetworkFlag)
	var daemonAddress netip.AddrPort
	if addr, _ := flags.GetString(addressFlag); addr != "" {
		lc := net.ListenConfig{}
		if grpcListener, err = lc.Listen(c, "tcp", addr); err != nil {
			return err
		}
		daemonAddress = grpcListener.Addr().(interface{ AddrPort() netip.AddrPort }).AddrPort()
	} else {
		socketPath := socket.UserDaemonPath(c)
		dlog.Infof(c, "Starting socket listener for %s", socketPath)
		if grpcListener, err = socket.Listen(c, client.UserDaemonName, socketPath); err != nil {
			dlog.Errorf(c, "socket listener for %s failed: %v", socketPath, err)
			return err
		}
		defer func() {
			_ = socket.Remove(grpcListener)
		}()
	}
	dlog.Debugf(c, "Listener opened on %s", grpcListener.Addr())

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence User Daemon %s starting...", client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// Start services from within a group routine so that it gets proper cancellation
	// when the group is cancelled.
	siCh := make(chan *service)
	g.Go("serve-grpc", func(c context.Context) error {
		// svcCancel is what a `quit -s` call will cancel. The Group provides soft cancellation to it.
		// which will result in a graceful termination of the grpc server.
		c, svcCancel := context.WithCancel(c)

		var opts []grpc.ServerOption
		if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
			opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
		}
		svc := server.New(c, opts...)
		siCh <- newService(c, svcCancel, cfg, svc)
		close(siCh)
		return server.Serve(c, svc, grpcListener)
	})

	s, ok := <-siCh
	if !ok {
		// Return error from the "service" go routine
		return g.Wait()
	}

	s.rootSessionInProc = rootSessionInProc
	s.daemonAddress = daemonAddress
	if tp, err := flags.GetUint16(teleroutePortFlag); err == nil && tp > 0 {
		dlog.Debugf(c, "Using teleroute %d", tp)
		s.teleroutePort = tp
	}

	if err := logging.LoadTimedLevelFromCache(c, s.timedLogLevel, client.UserDaemonName); err != nil {
		return err
	}

	if cfg.Intercept().UseFtp && !s.fuseFtpMgr.LinkedFTP() {
		g.Go("fuseftp-server", func(c context.Context) error {
			if err := s.InitFTPServer(c); err != nil {
				return err
			}
			<-c.Done()
			return nil
		})
	}

	g.Go("config-reload", s.configReload)
	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}

func (s *service) LinkedFTP() bool {
	return s.fuseFtpMgr.LinkedFTP()
}

func (s *service) InitFTPServer(ctx context.Context) error {
	return s.fuseFtpMgr.DeferInit(ctx)
}
