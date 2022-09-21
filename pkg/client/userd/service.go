package userd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc2 "github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/internal/broadcastqueue"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

const ProcessName = "connector"
const titleName = "Connector"

var help = `The Telepresence ` + titleName + ` is a background component that manages a connection. It
requires that a daemon is already running.

Launch the Telepresence ` + titleName + `:
    telepresence connect

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(func() string { dir, _ := filelocation.AppUserLogDir(context.Background()); return dir }(), ProcessName+".log") + `
to troubleshoot problems.
`

type WithSession func(c context.Context, callName string, f func(context.Context, trafficmgr.Session) error) (err error)

// A DaemonService is one that runs during the entire lifecycle of the daemon.
// This should be used to augment the daemon with GRPC services.
type DaemonService interface {
	Name() string
	// Start should start the daemon service. It's expected that it returns and does not block. Any long-running tasks should be
	// managed as goroutines started by Start.
	Start(ctx context.Context, scout *scout.Reporter, grpcServer *grpc.Server, withSession WithSession) error
}

type CommandFactory func(context.Context) cliutil.CommandGroups

// Service represents the long-running state of the Telepresence User Daemon
type Service struct {
	rpc.UnsafeConnectorServer

	svc               *grpc.Server
	ManagerProxy      trafficmgr.ManagerProxy
	procName          string
	timedLogLevel     log.TimedLevel
	daemonClient      daemon.DaemonClient
	loginExecutor     auth.LoginExecutor
	userNotifications func(context.Context) <-chan string
	ucn               int64

	scout *scout.Reporter

	quit func()

	session         trafficmgr.Session
	sessionCancel   context.CancelFunc
	sessionContext  context.Context
	sessionQuitting int32 // atomic boolean. True if non-zero.
	sessionLock     sync.RWMutex

	// These are used to communicate between the various goroutines.
	connectRequest  chan *rpc.ConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo    // connectWorker -> server-grpc.connect()

	// This is used for the service to know which CLI commands it supports
	getCommands CommandFactory
}

func (s *Service) SetManagerClient(managerClient manager.ManagerClient, callOptions ...grpc.CallOption) {
	s.ManagerProxy.SetClient(managerClient, callOptions...)
}

func (s *Service) RootDaemonClient(c context.Context) (daemon.DaemonClient, error) {
	if s.daemonClient != nil {
		return s.daemonClient, nil
	}
	// establish a connection to the root daemon gRPC grpcService
	dlog.Info(c, "Connecting to root daemon...")
	conn, err := client.DialSocket(c, client.DaemonSocketName,
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		dlog.Errorf(c, "unable to connect to root daemon: %+v", err)
		return nil, err
	}
	s.daemonClient = daemon.NewDaemonClient(conn)
	return s.daemonClient, nil
}

func (s *Service) LoginExecutor() auth.LoginExecutor {
	return s.loginExecutor
}

// Command returns the CLI sub-command for "connector-foreground"
func Command(getCommands CommandFactory, daemonServices []DaemonService, sessionServices []trafficmgr.SessionService) *cobra.Command {
	c := &cobra.Command{
		Use:    ProcessName + "-foreground",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		Long:   help,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), getCommands, daemonServices, sessionServices)
		},
	}
	return c
}

func (s *Service) configReload(c context.Context) error {
	return client.Watch(c, func(c context.Context) error {
		return logging.ReloadDaemonConfig(c, false)
	})
}

// ManageSessions is the counterpart to the Connect method. It reads the connectCh, creates
// a session and writes a reply to the connectErrCh. The session is then started if it was
// successfully created.
func (s *Service) ManageSessions(c context.Context, sessionServices []trafficmgr.SessionService, fuseFtp rpc2.FuseFTPClient) error {
	// The d.quit is called when we receive a Quit. Since it
	// terminates this function, it terminates the whole process.
	wg := sync.WaitGroup{}
	c, s.quit = context.WithCancel(c)
nextSession:
	for {
		// Wait for a connection request
		var cr *rpc.ConnectRequest
		select {
		case <-c.Done():
			break nextSession
		case cr = <-s.connectRequest:
		}

		var session trafficmgr.Session
		var rsp *rpc.ConnectInfo

		s.sessionLock.Lock() // Locked during creation
		if c.Err() == nil {  // If by the time we've got the session lock we're cancelled, then don't create the session and just leave by way of the select below
			if s.session != nil {
				// UpdateStatus sets rpc.ConnectInfo_ALREADY_CONNECTED if successful
				rsp = s.session.UpdateStatus(s.sessionContext, cr)
			} else {
				sCtx, sCancel := context.WithCancel(c)
				sCtx, session, rsp = trafficmgr.NewSession(sCtx, s.scout, cr, s, sessionServices, fuseFtp)
				sCtx = a8rcloud.WithSystemAPool[*SessionClient](sCtx, a8rcloud.UserdConnName, &SessionClientProvider{session})
				if sCtx.Err() == nil && rsp.Error == rpc.ConnectInfo_UNSPECIFIED {
					s.sessionContext = session.WithK8sInterface(sCtx)
					s.sessionCancel = sCancel
					s.session = session
				} else {
					sCancel()
				}
			}
		}
		s.sessionLock.Unlock()

		select {
		case <-c.Done():
			break nextSession
		case s.connectResponse <- rsp:
		default:
			// Nobody there to read the response? That's fine. The user may have got
			// impatient.
			s.cancelSession()
			continue
		}
		if rsp.Error != rpc.ConnectInfo_UNSPECIFIED {
			continue
		}

		// Run the session asynchronously. We must be able to respond to connect (with UpdateStatus) while
		// the session is running. The s.sessionCancel is called from Disconnect
		wg.Add(1)
		go func(cr *rpc.ConnectRequest) {
			defer wg.Done()
			if err := s.session.Run(s.sessionContext); err != nil {
				if errors.Is(err, trafficmgr.SessionExpiredErr) {
					// Session has expired. We need to cancel the owner session and reconnect
					dlog.Info(c, "refreshing session")
					s.cancelSession()
					select {
					case <-c.Done():
					case s.connectRequest <- cr:
					}
					return
				}

				dlog.Error(c, err)
			}
		}(cr)
	}
	wg.Wait()
	return nil
}

func (s *Service) cancelSessionReadLocked() {
	if s.sessionCancel != nil {
		if err := s.session.ClearIntercepts(s.sessionContext); err != nil {
			dlog.Errorf(s.sessionContext, "failed to clear intercepts: %v", err)
		}
		s.sessionCancel()
	}
}

func (s *Service) cancelSession() {
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

func GetPoddService(sc *scout.Reporter, cfg client.Config, login auth.LoginExecutor) Service {
	return Service{
		scout:           sc,
		connectRequest:  make(chan *rpc.ConnectRequest),
		connectResponse: make(chan *rpc.ConnectInfo),
		ManagerProxy:    trafficmgr.NewManagerProxy(),
		loginExecutor:   login,
		timedLogLevel:   log.NewTimedLevel(cfg.LogLevels.UserDaemon.String(), log.SetLevel),
	}
}

// run is the main function when executing as the connector
func run(c context.Context, getCommands CommandFactory, daemonServices []DaemonService, sessionServices []trafficmgr.SessionService) error {
	cfg, err := client.LoadConfig(c)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	c = client.WithConfig(c, cfg)
	c = dgroup.WithGoroutineName(c, "/"+ProcessName)
	c, err = logging.InitContext(c, ProcessName, logging.RotateDaily, true)
	if err != nil {
		return err
	}

	// Listen on domain unix domain socket or windows named pipe. The listener must be opened
	// before other tasks because the CLI client will only wait for a short period of time for
	// the socket/pipe to appear before it gives up.
	grpcListener, err := client.ListenSocket(c, ProcessName, client.ConnectorSocketName)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.RemoveSocket(grpcListener)
	}()
	dlog.Debug(c, "Listener opened")

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", titleName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	// Don't bother calling 'conn.Close()', it should remain open until we shut down, and just
	// prefer to let the OS close it when we exit.

	sr := scout.NewReporter(c, "connector")
	cliio := &broadcastqueue.BroadcastQueue{}

	tracer, err := tracing.NewTraceServer(c, "user-daemon")
	if err != nil {
		return err
	}

	s := &Service{
		scout:             sr,
		connectRequest:    make(chan *rpc.ConnectRequest),
		connectResponse:   make(chan *rpc.ConnectInfo),
		ManagerProxy:      trafficmgr.NewManagerProxy(),
		loginExecutor:     auth.NewStandardLoginExecutor(cliio, sr),
		userNotifications: cliio.Subscribe,
		timedLogLevel:     log.NewTimedLevel(cfg.LogLevels.UserDaemon.String(), log.SetLevel),
		getCommands:       getCommands,
	}
	if err := logging.LoadTimedLevelFromCache(c, s.timedLogLevel, s.procName); err != nil {
		return err
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	fuseFtpCh := make(chan rpc2.FuseFTPClient)
	if cfg.Intercept.UseFtp {
		g.Go("fuseftp-server", func(c context.Context) error {
			return runFuseFTPServer(c, fuseFtpCh)
		})
	} else {
		close(fuseFtpCh)
	}

	g.Go("server-grpc", func(c context.Context) (err error) {
		opts := []grpc.ServerOption{
			grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
			grpc.StreamInterceptor(otelgrpc.StreamServerInterceptor()),
		}
		if !cfg.Grpc.MaxReceiveSize.IsZero() {
			if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
				opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
			}
		}
		s.svc = grpc.NewServer(opts...)
		rpc.RegisterConnectorServer(s.svc, s)
		manager.RegisterManagerServer(s.svc, s.ManagerProxy)
		common.RegisterTracingServer(s.svc, tracer)
		for _, ds := range daemonServices {
			dlog.Infof(c, "Starting additional daemon service %s", ds.Name())
			if err := ds.Start(c, sr, s.svc, s.withSession); err != nil {
				return err
			}
		}

		sc := &dhttp.ServerConfig{Handler: s.svc}
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
	g.Go("session", func(c context.Context) error {
		err := s.ManageSessions(c, sessionServices, <-fuseFtpCh)
		cliio.Close()
		return err
	})

	// background-systema runs a localhost HTTP server for handling callbacks from the
	// Ambassador Cloud login flow.
	g.Go("background-systema", s.loginExecutor.Worker)

	// background-metriton is the goroutine that handles all telemetry reports, so that calls to
	// metriton don't block the functional goroutines.
	g.Go("background-metriton", s.scout.Run)

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
