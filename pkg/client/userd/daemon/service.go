package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	authGrpc "github.com/telepresenceio/telepresence/v2/pkg/authenticator/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/pprof"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
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
	timedLogLevel log.TimedLevel
	fuseFTPError  error

	// The quit function that quits the server.
	quit func()

	clientConfig    clientcmd.ClientConfig
	session         userd.Session
	sessionQuitting int32 // atomic boolean. True if non-zero.
	sessionLock     sync.RWMutex

	// These are used to communicate between the various goroutines.
	connectRequest  chan userd.ConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo     // connectWorker -> server-grpc.connect()

	fuseFtpMgr remotefs.FuseFTPManager

	// Run root session in-process
	rootSessionInProc bool

	// The TCP address that the daemon listens to. Will be nil if the daemon listens to a unix socket.
	daemonAddress netip.AddrPort

	// Port where root daemon (or rather the embedded root daemon) starts the teleroute service.
	teleroutePort uint16
}

func (s *service) ClientConfig() (clientcmd.ClientConfig, error) {
	if s.clientConfig == nil {
		return nil, errors.New("user daemon has no client config")
	}
	return s.clientConfig, nil
}

func NewService(cancel context.CancelFunc, _ *dgroup.Group, cfg client.Config, srv *grpc.Server) (userd.Service, error) {
	s := &service{
		srv:             srv,
		connectRequest:  make(chan userd.ConnectRequest),
		connectResponse: make(chan *rpc.ConnectInfo),
		timedLogLevel:   log.NewTimedLevel(cfg.LogLevels().UserDaemon.String(), log.SetLevel),
		fuseFtpMgr:      remotefs.NewFuseFTPManager(),
		quit:            cancel,
	}
	if srv != nil {
		// The podd daemon never registers the gRPC servers
		rpc.RegisterConnectorServer(srv, s)
		authGrpc.RegisterAuthenticatorServer(srv, s)
	} else {
		s.rootSessionInProc = true
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

func (s *service) PostConnectRequest(ctx context.Context, cr userd.ConnectRequest) error {
	select {
	case <-ctx.Done():
		return status.Error(codes.Unavailable, ctx.Err().Error())
	case s.connectRequest <- cr:
		return nil
	}
}

func (s *service) ReadConnectResponse(ctx context.Context) (result *rpc.ConnectInfo, err error) {
	select {
	case <-ctx.Done():
		err = status.Error(codes.Unavailable, ctx.Err().Error())
	case result = <-s.connectResponse:
	}
	return result, err
}

const (
	nameFlag          = "name"
	addressFlag       = "address"
	embedNetworkFlag  = "embed-network"
	pprofFlag         = "pprof"
	teleroutePortFlag = "teleroute-port"
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
			return client.ReloadDaemonLogLevel(ctx, false)
		}
		return s.session.ApplyConfig()
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
				s.cancelSession(c)
			}
		}
	}
}

func (s *service) startSession(parentCtx context.Context, cr userd.ConnectRequest, wg *sync.WaitGroup) *rpc.ConnectInfo {
	s.sessionLock.Lock() // Locked during creation
	defer s.sessionLock.Unlock()

	if s.session != nil {
		// UpdateStatus sets rpc.ConnectInfo_ALREADY_CONNECTED if successful
		return s.session.UpdateStatus(cr)
	}

	// Obtain the kubeconfig from the request parameters so that we can determine
	// what kubernetes context that will be used.
	ctx, config, err := client.DaemonKubeconfig(parentCtx, cr.Request())
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
	s.clientConfig = config.ClientConfig

	ctx, cancel := context.WithCancel(ctx)
	ctx = userd.WithService(ctx, s)

	daemonID := daemon.NewIdentifier(cr.Request().Name, config.Context, config.Namespace, proc.RunningInContainer())
	go runAliveAndCancellation(ctx, cancel, daemonID, wg)

	session, rsp := trafficmgr.NewSession(ctx, cr, config, wg)
	if ctx.Err() != nil || rsp.Error != rpc.ConnectInfo_UNSPECIFIED {
		cancel()
		if s.rootSessionInProc {
			// Simplified session management. The daemon handles one session, then exits.
			s.quit()
		}
		return rsp
	}
	s.session = session

	// Run the session asynchronously. We must be able to respond to connect (with UpdateStatus) while
	// the session is running. The s.sessionCancel is called from Disconnect
	wg.Add(1)
	go func() {
		defer func() {
			s.sessionLock.Lock()
			s.clientConfig = nil
			s.session = nil
			s.sessionLock.Unlock()
			_ = client.ReloadDaemonLogLevel(parentCtx, false)
			wg.Done()
		}()
		if err := session.Run(); err != nil {
			dlog.Error(ctx, err)
		}
		if s.rootSessionInProc {
			// Simplified session management. The daemon handles one session, then exits.
			s.quit()
		}
	}()
	return rsp
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

func (s *service) cancelSession(ctx context.Context) {
	if atomic.CompareAndSwapInt32(&s.sessionQuitting, 0, 1) {
		if s.session != nil {
			// We use a TryRLock here because the session-lock will be held during
			// session initialization, and we might well receive a quit-call during
			// that time (the initialization may take a long time if there are
			// problems connecting to the cluster).
			if s.sessionLock.TryRLock() {
				if err := s.session.ClearIngestsAndIntercepts(); err != nil {
					dlog.Errorf(ctx, "failed to clear intercepts: %v", err)
				}
				s.sessionLock.RUnlock()
			}
			s.session.Cancel()
		}
		s.session = nil
		atomic.StoreInt32(&s.sessionQuitting, 0)
	}
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
	c, err = logging.InitContext(c, userd.ProcessName, logging.RotateDaily, true, false)
	if err != nil {
		return err
	}

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

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// Start services from within a group routine so that it gets proper cancellation
	// when the group is cancelled.
	siCh := make(chan userd.Service)
	g.Go("serve-grpc", func(c context.Context) error {
		// svcCancel is what a `quit -s` call will cancel. The Group provides soft cancellation to it.
		// which will result in a graceful termination of the grpc server.
		c, svcCancel := context.WithCancel(c)

		var opts []grpc.ServerOption
		if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
			opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
		}
		svc := server.New(c, opts...)
		si, err := NewService(svcCancel, g, cfg, svc)
		if err != nil {
			close(siCh)
			return err
		}
		siCh <- si
		close(siCh)
		return server.Serve(c, svc, grpcListener)
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
	if tp, err := flags.GetUint16(teleroutePortFlag); err == nil && tp > 0 {
		dlog.Debugf(c, "Using teleroute %d", tp)
		s.teleroutePort = tp
	}

	if err := logging.LoadTimedLevelFromCache(c, s.timedLogLevel, userd.ProcessName); err != nil {
		return err
	}

	if cfg.Intercept().UseFtp {
		g.Go("fuseftp-server", func(c context.Context) error {
			if err := s.InitFTPServer(c); err != nil {
				dlog.Error(c, err)
			}
			<-c.Done()
			return nil
		})
	}

	g.Go("config-reload", s.configReload)
	g.Go(sessionName, func(c context.Context) error {
		return s.ManageSessions(c)
	})

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}

func (s *service) InitFTPServer(ctx context.Context) error {
	return s.fuseFtpMgr.DeferInit(ctx)
}
