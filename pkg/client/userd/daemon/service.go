package daemon

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
	fuseftp "github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

const titleName = "Connector"

func help() string {
	return `The Telepresence ` + titleName + ` is a background component that manages a connection. It
requires that a daemon is already running.

Launch the Telepresence ` + titleName + `:
    telepresence connect

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(func() string { dir, _ := filelocation.AppUserLogDir(context.Background()); return dir }(), userd.ProcessName+".log") + `
to troubleshoot problems.
`
}

// Service represents the long-running state of the Telepresence User Daemon.
type Service struct {
	rpc.UnsafeConnectorServer
	srv           *grpc.Server
	procName      string
	timedLogLevel log.TimedLevel
	ucn           int64
	fuseFTPError  error
	scout         *scout.Reporter

	quit func()

	session         userd.Session
	sessionCancel   context.CancelFunc
	sessionContext  context.Context
	sessionQuitting int32 // atomic boolean. True if non-zero.
	sessionLock     sync.RWMutex

	// These are used to communicate between the various goroutines.
	connectRequest  chan *rpc.ConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo    // connectWorker -> server-grpc.connect()

	fuseFtpCh   chan fuseftp.FuseFTPClient
	startFuseCh chan struct{}
}

func NewService(ctx context.Context, _ *dgroup.Group, sr *scout.Reporter, cfg *client.Config, srv *grpc.Server) (userd.Service, error) {
	s := &Service{
		srv:             srv,
		scout:           sr,
		connectRequest:  make(chan *rpc.ConnectRequest),
		connectResponse: make(chan *rpc.ConnectInfo),
		timedLogLevel:   log.NewTimedLevel(cfg.LogLevels.UserDaemon.String(), log.SetLevel),
		fuseFtpCh:       make(chan fuseftp.FuseFTPClient, 1),
		startFuseCh:     make(chan struct{}),
	}
	if srv != nil {
		// The podd daemon never registers the gRPC servers
		rpc.RegisterConnectorServer(srv, s)
		tracer, err := tracing.NewTraceServer(ctx, "user-daemon")
		if err != nil {
			return nil, err
		}
		common.RegisterTracingServer(srv, tracer)
	}
	return s, nil
}

func (s *Service) As(ptr any) {
	switch ptr := ptr.(type) {
	case **Service:
		*ptr = s
	default:
		panic(fmt.Sprintf("%T does not implement %T", s, ptr))
	}
}

func (s *Service) GetAPIKey(_ context.Context) (string, error) {
	return "", nil
}

func (s *Service) Reporter() *scout.Reporter {
	return s.scout
}

func (s *Service) Server() *grpc.Server {
	return s.srv
}

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
	return c
}

func (s *Service) configReload(c context.Context) error {
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
func ManageSessions(c context.Context, si userd.Service) error {
	wg := sync.WaitGroup{}
	defer wg.Wait()

	var s *Service
	si.As(&s)

	for {
		// Wait for a connection request
		select {
		case <-c.Done():
			return nil
		case cr := <-s.connectRequest:
			rsp := startSession(c, si, cr, &wg)
			select {
			case <-c.Done():
				return nil
			case s.connectResponse <- rsp:
			default:
				// Nobody left to read the response? That's fine really. Just means that
				// whoever wanted to start the session terminated early.
				s.cancelSession()
			}
		}
	}
}

func startSession(ctx context.Context, si userd.Service, cr *rpc.ConnectRequest, wg *sync.WaitGroup) *rpc.ConnectInfo {
	var s *Service
	si.As(&s)
	s.sessionLock.Lock() // Locked during creation
	defer s.sessionLock.Unlock()

	if s.session != nil {
		// UpdateStatus sets rpc.ConnectInfo_ALREADY_CONNECTED if successful
		return s.session.UpdateStatus(s.sessionContext, cr)
	}

	ctx, cancel := context.WithCancel(ctx)
	ctx = userd.WithService(ctx, si)
	ctx, session, rsp := userd.GetNewSessionFunc(ctx)(ctx, s.scout, cr)
	if ctx.Err() != nil || rsp.Error != rpc.ConnectInfo_UNSPECIFIED {
		cancel()
		return rsp
	}

	s.session = session
	s.sessionContext = session.WithK8sInterface(ctx)
	s.sessionCancel = func() {
		cancel()
		<-session.Done()
	}
	if err := s.session.ApplyConfig(ctx); err != nil {
		dlog.Warnf(ctx, "failed to apply config from traffic-manager: %v", err)
	}

	// Run the session asynchronously. We must be able to respond to connect (with UpdateStatus) while
	// the session is running. The s.sessionCancel is called from Disconnect
	wg.Add(1)
	go func(cr *rpc.ConnectRequest) {
		defer func() {
			s.sessionLock.Lock()
			s.session = nil
			s.sessionCancel = nil
			if err := client.RestoreDefaults(ctx, false); err != nil {
				dlog.Warn(ctx, err)
			}
			s.sessionLock.Unlock()
			wg.Done()
		}()
		if err := userd.RunSession(s.sessionContext, session); err != nil {
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
	}(cr)
	return rsp
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

// run is the main function when executing as the connector.
func run(cmd *cobra.Command, _ []string) error {
	c := cmd.Context()
	cfg, err := client.LoadConfig(c)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	c = client.WithConfig(c, cfg)
	c = dgroup.WithGoroutineName(c, "/"+userd.ProcessName)
	c, err = logging.InitContext(c, userd.ProcessName, logging.RotateDaily, true)
	if err != nil {
		return err
	}

	// Listen on domain unix domain socket or windows named pipe. The listener must be opened
	// before other tasks because the CLI client will only wait for a short period of time for
	// the socket/pipe to appear before it gives up.
	grpcListener, err := client.ListenSocket(c, userd.ProcessName, client.ConnectorSocketName)
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
			grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
			grpc.StreamInterceptor(otelgrpc.StreamServerInterceptor()),
		}
		if !cfg.Grpc.MaxReceiveSize.IsZero() {
			if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
				opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
			}
		}
		sr := scout.NewReporter(c, "connector")
		si, err := userd.GetNewServiceFunc(c)(c, g, sr, cfg, grpc.NewServer(opts...))
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

	var s *Service
	si.As(&s)

	if err := logging.LoadTimedLevelFromCache(c, s.timedLogLevel, s.procName); err != nil {
		return err
	}

	g.Go("fuseftp-server", s.deferFuseFtpInit)

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
	g.Go("session", func(c context.Context) error {
		c, s.quit = context.WithCancel(c)
		return ManageSessions(c, si)
	})

	// background-metriton is the goroutine that handles all telemetry reports, so that calls to
	// metriton don't block the functional goroutines.
	g.Go("background-metriton", s.scout.Run)

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
