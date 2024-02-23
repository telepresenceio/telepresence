package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/pprof"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

const titleName = "Connector"

func help() string {
	return `The Telepresence ` + titleName + ` is a background component that manages a connection.

Launch the Telepresence ` + titleName + `:
    telepresence connect

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(filelocation.AppUserLogDir(context.Background()), userd.ProcessName+".log") + `
to troubleshoot problems.
`
}

// service represents the long-running state of the Telepresence User Daemon.
type service struct {
	rpc.UnsafeConnectorServer
	srv           *grpc.Server
	managerProxy  *mgrProxy
	timedLogLevel log.TimedLevel
	ucn           int64
	fuseFTPError  error

	// The quit function that quits the server.
	quit func()

	// quitDisable will temporarily disable the quit function. This is used when there's a desire
	// to cancel the session without cancelling the process although the simplified session management
	// is in effect (rootSessionInProc == true).
	quitDisable bool

	session         userd.Session
	sessionCancel   context.CancelFunc
	sessionContext  context.Context
	sessionQuitting int32 // atomic boolean. True if non-zero.
	sessionLock     sync.RWMutex

	// These are used to communicate between the various goroutines.
	connectRequest  chan *rpc.ConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo    // connectWorker -> server-grpc.connect()

	fuseFtpMgr remotefs.FuseFTPManager

	// Run root session in-process
	rootSessionInProc bool

	// The TCP address that the daemon listens to. Will be nil if the daemon listens to a unix socket.
	daemonAddress *net.TCPAddr

	// Possibly extended version of the service. Use when calling interface methods.
	self userd.Service
}

func NewService(ctx context.Context, _ *dgroup.Group, cfg client.Config, srv *grpc.Server) (userd.Service, error) {
	s := &service{
		srv:             srv,
		connectRequest:  make(chan *rpc.ConnectRequest),
		connectResponse: make(chan *rpc.ConnectInfo),
		managerProxy:    &mgrProxy{},
		timedLogLevel:   log.NewTimedLevel(cfg.LogLevels().UserDaemon.String(), log.SetLevel),
		fuseFtpMgr:      remotefs.NewFuseFTPManager(),
	}
	s.self = s
	if srv != nil {
		// The podd daemon never registers the gRPC servers
		rpc.RegisterConnectorServer(srv, s)
		rpc.RegisterManagerProxyServer(srv, s.managerProxy)
		tracer, err := tracing.NewTraceServer(ctx, "user-daemon")
		if err != nil {
			return nil, err
		}
		common.RegisterTracingServer(srv, tracer)
	}
	return s, nil
}

func (s *service) As(ptr any) {
	switch ptr := ptr.(type) {
	case **service:
		*ptr = s
	case *rpc.ConnectorServer:
		*ptr = s
	default:
		panic(fmt.Sprintf("%T does not implement %T", s, ptr))
	}
}

func (s *service) ListenerAddress(ctx context.Context) string {
	if s.daemonAddress != nil {
		return s.daemonAddress.String()
	}
	return "unix:" + socket.UserDaemonPath(ctx)
}

func (s *service) SetSelf(self userd.Service) {
	s.self = self
}

func (s *service) FuseFTPMgr() remotefs.FuseFTPManager {
	return s.fuseFtpMgr
}

func (s *service) RootSessionInProcess() bool {
	return s.rootSessionInProc
}

func (s *service) Server() *grpc.Server {
	return s.srv
}

func (s *service) SetManagerClient(managerClient manager.ManagerClient, callOptions ...grpc.CallOption) {
	s.managerProxy.setClient(managerClient, callOptions...)
}

const (
	nameFlag         = "name"
	addressFlag      = "address"
	embedNetworkFlag = "embed-network"
	pprofFlag        = "pprof"
)

// Command returns the CLI sub-command for "connector-foreground".
func Command() *cobra.Command {
	c := &cobra.Command{
		Use:    userd.ProcessName + "-foreground",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		Long:   help(),
		RunE:   run,
	}
	flags := c.Flags()
	flags.String(nameFlag, userd.ProcessName, "Daemon name")
	flags.String(addressFlag, "", "Address to listen to. Defaults to "+socket.UserDaemonPath(context.Background()))
	flags.Bool(embedNetworkFlag, false, "Embed network functionality in the user daemon. Requires capability NET_ADMIN")
	flags.Uint16(pprofFlag, 0, "start pprof server on the given port")
	return c
}

func (s *service) configReload(c context.Context) error {
	// Ensure that the directory to watch exists.
	if err := os.MkdirAll(filepath.Dir(client.GetConfigFile(c)), 0o755); err != nil {
		return err
	}
	return client.Watch(c, func(ctx context.Context) error {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session == nil {
			return client.RestoreDefaults(c, false)
		}
		return s.session.ApplyConfig(c)
	})
}

// ManageSessions is the counterpart to the Connect method. It reads the connectCh, creates
// a session and writes a reply to the connectErrCh. The session is then started if it was
// successfully created.
func (s *service) ManageSessions(c context.Context) error {
	wg := sync.WaitGroup{}
	defer wg.Wait()

	for {
		// Wait for a connection request
		select {
		case <-c.Done():
			return nil
		case cr := <-s.connectRequest:
			rsp := s.startSession(c, cr, &wg)
			select {
			case s.connectResponse <- rsp:
			default:
				// Nobody left to read the response? That's fine really. Just means that
				// whoever wanted to start the session terminated early.
				s.cancelSession()
			}
		}
	}
}

func (s *service) startSession(ctx context.Context, cr *rpc.ConnectRequest, wg *sync.WaitGroup) *rpc.ConnectInfo {
	s.sessionLock.Lock() // Locked during creation
	defer s.sessionLock.Unlock()

	if s.session != nil {
		// UpdateStatus sets rpc.ConnectInfo_ALREADY_CONNECTED if successful
		return s.session.UpdateStatus(s.sessionContext, cr)
	}

	// Obtain the kubeconfig from the request parameters so that we can determine
	// what kubernetes context that will be used.
	config, err := client.DaemonKubeconfig(ctx, cr)
	if err != nil {
		if s.rootSessionInProc {
			s.quit()
		}
		dlog.Errorf(ctx, "Failed to obtain kubeconfig: %v", err)
		return &rpc.ConnectInfo{
			Error:         rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText:     err.Error(),
			ErrorCategory: int32(errcat.GetCategory(err)),
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	ctx = userd.WithService(ctx, s.self)

	daemonID, err := daemon.NewIdentifier(cr.Name, config.Context, config.Namespace, proc.RunningInContainer())
	if err != nil {
		cancel()
		return &rpc.ConnectInfo{
			Error:         rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText:     err.Error(),
			ErrorCategory: int32(errcat.GetCategory(err)),
		}
	}
	go runAliveAndCancellation(ctx, cancel, daemonID)

	ctx, session, rsp := userd.GetNewSessionFunc(ctx)(ctx, cr, config)
	if ctx.Err() != nil || rsp.Error != rpc.ConnectInfo_UNSPECIFIED {
		cancel()
		if s.rootSessionInProc {
			// Simplified session management. The daemon handles one session, then exits.
			s.quit()
		}
		return rsp
	}
	s.session = session
	s.sessionContext = userd.WithSession(ctx, session)
	s.sessionCancel = func() {
		cancel()
		<-session.Done()
	}

	// Run the session asynchronously. We must be able to respond to connect (with UpdateStatus) while
	// the session is running. The s.sessionCancel is called from Disconnect
	wg.Add(1)
	go func(cr *rpc.ConnectRequest) {
		defer func() {
			s.sessionLock.Lock()
			s.self.SetManagerClient(nil)
			s.session = nil
			s.sessionCancel = nil
			if err := client.RestoreDefaults(ctx, false); err != nil {
				dlog.Warn(ctx, err)
			}
			s.sessionLock.Unlock()
			wg.Done()
		}()
		if err := session.RunSession(s.sessionContext); err != nil {
			if errors.Is(err, trafficmgr.ErrSessionExpired) {
				// Session has expired. We need to cancel the owner session and reconnect
				dlog.Info(ctx, "refreshing session")
				s.cancelSession()
				select {
				case <-ctx.Done():
				case s.connectRequest <- cr:
				}
				return
			}

			dlog.Error(ctx, err)
		}
		if s.rootSessionInProc {
			// Simplified session management. The daemon handles one session, then exits.
			s.quit()
		}
	}(cr)
	return rsp
}

func runAliveAndCancellation(ctx context.Context, cancel context.CancelFunc, daemonID *daemon.Identifier) {
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

func (s *service) cancelSessionReadLocked() {
	if s.sessionCancel != nil {
		if err := s.session.ClearIntercepts(s.sessionContext); err != nil {
			dlog.Errorf(s.sessionContext, "failed to clear intercepts: %v", err)
		}
		s.sessionCancel()
	}
}

func (s *service) cancelSession() {
	if !atomic.CompareAndSwapInt32(&s.sessionQuitting, 0, 1) {
		return
	}
	s.sessionLock.RLock()
	s.cancelSessionReadLocked()
	s.sessionLock.RUnlock()

	// We have to cancel the session before we can acquire this write-lock, because we need any long-running RPCs
	// that may be holding the RLock to die.
	s.sessionLock.Lock()
	s.session = nil
	s.sessionCancel = nil
	atomic.StoreInt32(&s.sessionQuitting, 0)
	s.sessionLock.Unlock()
}

// run is the main function when executing as the connector.
func run(cmd *cobra.Command, _ []string) error {
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
	sessionName := "session"
	if di := strings.IndexByte(name, '-'); di > 0 {
		sessionName = name[di+1:]
		name = name[:di]
	}
	c = dgroup.WithGoroutineName(c, "/"+name)
	c, err = logging.InitContext(c, userd.ProcessName, logging.RotateDaily, true)
	if err != nil {
		return err
	}
	rootSessionInProc, _ := flags.GetBool(embedNetworkFlag)
	var daemonAddress *net.TCPAddr
	if addr, _ := flags.GetString(addressFlag); addr != "" {
		lc := net.ListenConfig{}
		if grpcListener, err = lc.Listen(c, "tcp", addr); err != nil {
			return err
		}
		daemonAddress = grpcListener.Addr().(*net.TCPAddr)
		defer func() {
			_ = grpcListener.Close()
		}()
	} else {
		socketPath := socket.UserDaemonPath(c)
		dlog.Infof(c, "Starting socket listener for %s", socketPath)
		if grpcListener, err = socket.Listen(c, userd.ProcessName, socketPath); err != nil {
			dlog.Errorf(c, "socket listener for %s failed: %v", socketPath, err)
			return err
		}
		defer func() {
			_ = socket.Remove(grpcListener)
		}()
	}
	dlog.Debugf(c, "Listener opened on %s", grpcListener.Addr())

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", titleName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	// Don't bother calling 'conn.Close()', it should remain open until we shut down, and just
	// prefer to let the OS close it when we exit.

	c = scout.NewReporter(c, "connector")
	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// Start services from within a group routine so that it gets proper cancellation
	// when the group is cancelled.
	siCh := make(chan userd.Service)
	g.Go("service", func(c context.Context) error {
		opts := []grpc.ServerOption{
			grpc.StatsHandler(otelgrpc.NewServerHandler()),
		}
		if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
			opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
		}
		si, err := userd.GetNewServiceFunc(c)(c, g, cfg, grpc.NewServer(opts...))
		if err != nil {
			close(siCh)
			return err
		}
		siCh <- si
		close(siCh)

		<-c.Done() // wait for context cancellation
		return nil
	})

	si, ok := <-siCh
	if !ok {
		// Return error from the "service" go routine
		return g.Wait()
	}

	var s *service
	si.As(&s)
	s.rootSessionInProc = rootSessionInProc
	s.daemonAddress = daemonAddress

	if err := logging.LoadTimedLevelFromCache(c, s.timedLogLevel, userd.ProcessName); err != nil {
		return err
	}

	if cfg.Intercept().UseFtp {
		g.Go("fuseftp-server", func(c context.Context) error {
			if err := s.fuseFtpMgr.DeferInit(c); err != nil {
				dlog.Error(c, err)
			}
			<-c.Done()
			return nil
		})
	}

	g.Go("server-grpc", func(c context.Context) (err error) {
		sc := &dhttp.ServerConfig{Handler: s.srv}
		dlog.Info(c, "gRPC server started")
		if err = sc.Serve(c, grpcListener); err != nil && c.Err() != nil {
			err = nil // Normal shutdown
		}
		if err != nil {
			dlog.Errorf(c, "gRPC server ended with: %v", err)
		} else {
			dlog.Debug(c, "gRPC server ended")
		}
		return err
	})

	g.Go("config-reload", s.configReload)
	g.Go(sessionName, func(c context.Context) error {
		c, cancel := context.WithCancel(c)
		s.quit = func() {
			if !s.quitDisable {
				cancel()
			}
		}
		return s.ManageSessions(c)
	})

	// background-metriton is the goroutine that handles all telemetry reports, so that calls to
	// metriton don't block the functional goroutines.
	g.Go("background-metriton", scout.Run)

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
