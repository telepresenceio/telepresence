package userd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/internal/broadcastqueue"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
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

// service represents the long running state of the Telepresence User Daemon
type service struct {
	rpc.UnsafeConnectorServer

	svc               *grpc.Server
	managerProxy      trafficmgr.ManagerProxy
	procName          string
	timedLogLevel     log.TimedLevel
	daemonClient      daemon.DaemonClient
	loginExecutor     auth.LoginExecutor
	userNotifications func(context.Context) <-chan string
	ucn               int64

	scout *scout.Reporter

	quit func()

	session       trafficmgr.Session
	sessionCancel context.CancelFunc
	sessionLock   sync.Mutex

	// These are used to communicate between the various goroutines.
	connectRequest  chan *rpc.ConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo    // connectWorker -> server-grpc.connect()
}

func (s *service) SetManagerClient(managerClient manager.ManagerClient, callOptions ...grpc.CallOption) {
	s.managerProxy.SetClient(managerClient, callOptions...)
}

func (s *service) RootDaemonClient() daemon.DaemonClient {
	return s.daemonClient
}

func (s *service) LoginExecutor() auth.LoginExecutor {
	return s.loginExecutor
}

// Command returns the CLI sub-command for "connector-foreground"
func Command() *cobra.Command {
	c := &cobra.Command{
		Use:    ProcessName + "-foreground",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		Long:   help,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context())
		},
	}
	return c
}

func (s *service) configReload(c context.Context) error {
	return client.Watch(c, func(c context.Context) error {
		return logging.ReloadDaemonConfig(c, false)
	})
}

// manageSessions is the counterpart to the Connect method. It reads the connectCh, creates
// a session and writes a reply to the connectErrCh. The session is then started if it was
// successfully created.
func (s *service) manageSessions(c context.Context) error {
	// The d.quit is called when we receive a Quit. Since it
	// terminates this function, it terminates the whole process.
	c, s.quit = context.WithCancel(c)
	for {
		// Wait for a connection request
		var oi *rpc.ConnectRequest
		select {
		case <-c.Done():
			return nil
		case oi = <-s.connectRequest:
		}

		// Respond by setting the session and returning the error (or nil
		// if everything is ok)
		var rsp *rpc.ConnectInfo
		s.session, rsp = trafficmgr.NewSession(c, s.scout, oi, s)
		select {
		case <-c.Done():
			return nil
		case s.connectResponse <- rsp:
		}
		if rsp.Error != rpc.ConnectInfo_UNSPECIFIED {
			continue
		}

		// Run the session synchronously and ensure that it is cleaned
		// up properly when the context is cancelled
		func(c context.Context) {
			defer func() {
				s.sessionLock.Lock()
				s.session = nil
				s.sessionLock.Unlock()
			}()

			// The d.session.Cancel is called from Disconnect
			c, s.sessionCancel = context.WithCancel(c)
			if err := s.session.Run(c); err != nil {
				dlog.Error(c, err)
			}
		}(c)
	}
}

// run is the main function when executing as the connector
func run(c context.Context) error {
	cfg, err := client.LoadConfig(c)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	c = client.WithConfig(c, cfg)
	c = dgroup.WithGoroutineName(c, "/"+ProcessName)
	c, err = logging.InitContext(c, ProcessName, logging.NewRotateOnce())
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

	// establish a connection to the root daemon gRPC grpcService
	dlog.Info(c, "Connecting to root daemon...")
	conn, err := client.DialSocket(c, client.DaemonSocketName)
	if err != nil {
		dlog.Errorf(c, "unable to connect to root daemon: %+v", err)
		return err
	}

	// Don't bother calling 'conn.Close()', it should remain open until we shut down, and just
	// prefer to let the OS close it when we exit.

	sr := scout.NewReporter(c, "connector")
	cliio := &broadcastqueue.BroadcastQueue{}

	s := &service{
		scout:             sr,
		connectRequest:    make(chan *rpc.ConnectRequest),
		connectResponse:   make(chan *rpc.ConnectInfo),
		daemonClient:      daemon.NewDaemonClient(conn),
		managerProxy:      trafficmgr.NewManagerProxy(),
		loginExecutor:     auth.NewStandardLoginExecutor(cliio, sr),
		userNotifications: func(ctx context.Context) <-chan string { return cliio.Subscribe(ctx) },
		timedLogLevel:     log.NewTimedLevel(cfg.LogLevels.UserDaemon.String(), log.SetLevel),
	}
	if err := logging.LoadTimedLevelFromCache(c, s.timedLogLevel, s.procName); err != nil {
		return err
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	quitOnce := sync.Once{}
	s.quit = func() {
		quitOnce.Do(func() {
			g.Go("quit", func(_ context.Context) error {
				cliio.Close()
				return nil
			})
		})
	}

	g.Go("server-grpc", func(c context.Context) (err error) {
		opts := []grpc.ServerOption{}
		cfg := client.GetConfig(c)
		if !cfg.Grpc.MaxReceiveSize.IsZero() {
			if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
				opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
			}
		}
		s.svc = grpc.NewServer(opts...)
		rpc.RegisterConnectorServer(s.svc, s)
		manager.RegisterManagerServer(s.svc, s.managerProxy)

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
	g.Go("session", s.manageSessions)

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
