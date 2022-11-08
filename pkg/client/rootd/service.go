package rootd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type NewServiceFunc func(*scout.Reporter, *client.Config) *Service

type newServiceKey struct{}

func WithNewServiceFunc(ctx context.Context, f NewServiceFunc) context.Context {
	return context.WithValue(ctx, newServiceKey{}, f)
}

func GetNewServiceFunc(ctx context.Context) NewServiceFunc {
	if f, ok := ctx.Value(newServiceKey{}).(NewServiceFunc); ok {
		return f
	}
	panic("No User daemon Service creator has been registered")
}

const (
	ProcessName = "daemon"
	titleName   = "Daemon"
)

func help() string {
	return `The Telepresence ` + titleName + ` is a long-lived background component that manages
connections and network state.

Launch the Telepresence ` + titleName + `:
    sudo telepresence Service

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(func() string { dir, _ := filelocation.AppUserLogDir(context.Background()); return dir }(), ProcessName+".log") + `
to troubleshoot problems.
`
}

type sessionReply struct {
	status *rpc.DaemonStatus
	err    error
}

// Service represents the state of the Telepresence Daemon.
type Service struct {
	rpc.UnsafeDaemonServer
	quit            context.CancelFunc
	connectCh       chan *rpc.OutboundInfo
	connectReplyCh  chan sessionReply
	sessionLock     sync.RWMutex
	sessionCancel   context.CancelFunc
	sessionContext  context.Context
	sessionQuitting int32 // atomic boolean. True if non-zero.
	session         *Session
	timedLogLevel   log.TimedLevel

	scout *scout.Reporter
}

func NewService(sr *scout.Reporter, cfg *client.Config) *Service {
	return &Service{
		scout:          sr,
		timedLogLevel:  log.NewTimedLevel(cfg.LogLevels.RootDaemon.String(), log.SetLevel),
		connectCh:      make(chan *rpc.OutboundInfo),
		connectReplyCh: make(chan sessionReply),
	}
}

func (s *Service) As(ptr any) {
	if sp, ok := ptr.(**Service); ok {
		*sp = s
	} else {
		panic(fmt.Sprintf("%T does not implement %T", s, *sp))
	}
}

// Command returns the telepresence sub-command "daemon-foreground".
func Command() *cobra.Command {
	return &cobra.Command{
		Use:    ProcessName + "-foreground <logging dir> <config dir>",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		Long:   help(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), args[0], args[1])
		},
	}
}

func (s *Service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *Service) Status(_ context.Context, _ *empty.Empty) (*rpc.DaemonStatus, error) {
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	r := &rpc.DaemonStatus{
		Version: &common.VersionInfo{
			ApiVersion: client.APIVersion,
			Version:    client.Version(),
		},
	}
	if s.session != nil {
		r.OutboundConfig = s.session.getInfo()
	}
	return r, nil
}

func (s *Service) Quit(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(ctx, "Received gRPC Quit")
	s.quit()
	s.sessionLock.Lock()
	defer s.sessionLock.Unlock()
	s.session = nil
	return &empty.Empty{}, nil
}

func (s *Service) SetDnsSearchPath(ctx context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	err := s.WithSession(func(ctx context.Context, session *Session) error {
		session.SetSearchPath(ctx, paths.Paths, paths.Namespaces)
		return nil
	})
	return &empty.Empty{}, err
}

func (s *Service) Connect(ctx context.Context, info *rpc.OutboundInfo) (*rpc.DaemonStatus, error) {
	dlog.Debug(ctx, "Received gRPC Connect")
	select {
	case <-ctx.Done():
		return nil, status.Error(codes.Canceled, ctx.Err().Error())
	case s.connectCh <- info:
	}
	select {
	case <-ctx.Done():
		return nil, status.Error(codes.Canceled, ctx.Err().Error())
	case reply := <-s.connectReplyCh:
		return reply.status, reply.err
	}
}

func (s *Service) Disconnect(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(ctx, "Received gRPC Disconnect")
	s.cancelSession()
	return &empty.Empty{}, nil
}

func (s *Service) WaitForNetwork(ctx context.Context, e *empty.Empty) (*empty.Empty, error) {
	err := s.WithSession(func(ctx context.Context, session *Session) error {
		if err, ok := <-session.networkReady(ctx); ok {
			return status.Error(codes.Unavailable, err.Error())
		}
		return nil
	})
	return &empty.Empty{}, err
}

func (s *Service) cancelSession() {
	if !atomic.CompareAndSwapInt32(&s.sessionQuitting, 0, 1) {
		return
	}
	s.sessionLock.RLock()
	s.sessionCancel()
	s.sessionLock.RUnlock()

	s.sessionLock.Lock()
	s.session = nil
	s.sessionCancel()
	atomic.StoreInt32(&s.sessionQuitting, 0)
	s.sessionLock.Unlock()
}

func (s *Service) WithSession(f func(context.Context, *Session) error) error {
	if atomic.LoadInt32(&s.sessionQuitting) != 0 {
		return status.Error(codes.Canceled, "session cancelled")
	}
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	if s.session == nil {
		return status.Error(codes.Unavailable, "no active session")
	}
	return f(s.sessionContext, s.session)
}

func (s *Service) GetClusterSubnets(ctx context.Context, _ *empty.Empty) (*rpc.ClusterSubnets, error) {
	podSubnets := []*manager.IPNet{}
	svcSubnets := []*manager.IPNet{}
	err := s.WithSession(func(ctx context.Context, session *Session) error {
		// The manager can sometimes send the different subnets in different Sends,
		// but after 5 seconds of listening to it, we should expect to have everything
		tCtx, tCancel := context.WithTimeout(ctx, 5*time.Second)
		defer tCancel()
		infoStream, err := session.managerClient.WatchClusterInfo(tCtx, session.session)
		if err != nil {
			return err
		}
		for {
			mgrInfo, err := infoStream.Recv()
			if err != nil {
				if tCtx.Err() != nil || errors.Is(err, io.EOF) {
					err = nil
				}
				return err
			}
			if mgrInfo.ServiceSubnet != nil {
				svcSubnets = append(svcSubnets, mgrInfo.ServiceSubnet)
			}
			podSubnets = append(podSubnets, mgrInfo.PodSubnets...)
		}
	})
	if err != nil {
		return nil, err
	}
	return &rpc.ClusterSubnets{PodSubnets: podSubnets, SvcSubnets: svcSubnets}, nil
}

func (s *Service) SetLogLevel(ctx context.Context, request *manager.LogLevelRequest) (*empty.Empty, error) {
	duration := time.Duration(0)
	if request.Duration != nil {
		duration = request.Duration.AsDuration()
	}
	return &empty.Empty{}, logging.SetAndStoreTimedLevel(ctx, s.timedLogLevel, request.LogLevel, duration, ProcessName)
}

func (s *Service) configReload(c context.Context) error {
	return client.Watch(c, func(c context.Context) error {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		return s.session.applyConfig(c)
	})
}

// manageSessions is the counterpart to the Connect method. It reads the connectCh, creates
// a session and writes a reply to the connectErrCh. The session is then started if it was
// successfully created.
func (s *Service) manageSessions(c context.Context) error {
	// The d.quit is called when we receive a Quit. Since it
	// terminates this function, it terminates the whole process.
	wg := sync.WaitGroup{}
	c, s.quit = context.WithCancel(c)
nextSession:
	for {
		// Wait for a connection request
		var oi *rpc.OutboundInfo
		select {
		case <-c.Done():
			break nextSession
		case oi = <-s.connectCh:
		}

		var session *Session
		reply := sessionReply{}

		s.sessionLock.Lock() // Locked during creation
		if c.Err() == nil {  // If by the time we've got the session lock we're cancelled, then don't create the session and just leave by way of the select below
			// Respond by setting the session and returning the error (or nil
			// if everything is ok)
			reply.status = &rpc.DaemonStatus{
				Version: &common.VersionInfo{
					ApiVersion: client.APIVersion,
					Version:    client.Version(),
				},
			}
			if s.session != nil {
				reply.status.OutboundConfig = s.session.getInfo()
			} else {
				sCtx, sCancel := context.WithCancel(c)
				session, reply.err = GetNewSessionFunc(c)(sCtx, s.scout, oi)
				if reply.err == nil {
					s.session = session
					s.sessionContext = sCtx
					s.sessionCancel = sCancel
					if err := s.session.applyConfig(c); err != nil {
						dlog.Warnf(c, "failed to apply config from traffic-manager: %v", err)
					}

					reply.status.OutboundConfig = s.session.getInfo()
				} else {
					sCancel()
				}
			}
		}
		s.sessionLock.Unlock()

		select {
		case <-c.Done():
			break nextSession
		case s.connectReplyCh <- reply:
		default:
			// Nobody left to read the response? That's fine really. Just means that
			// whoever wanted to start the session terminated early.
			s.cancelSession()
			continue
		}
		if reply.err != nil {
			continue
		}

		// Run the session asynchronously. We must be able to respond to connect (with getInfo) while
		// the session is running. The d.session.cancel is called from Disconnect
		wg.Add(1)
		go func() {
			defer func() {
				s.sessionLock.Lock()
				s.session = nil
				if err := client.RestoreDefaults(c, true); err != nil {
					dlog.Warn(c, err)
				}
				s.sessionLock.Unlock()
				wg.Done()
			}()
			if err := s.session.run(s.sessionContext); err != nil {
				dlog.Error(c, err)
			}
		}()
	}
	wg.Wait()
	return nil
}

func (s *Service) serveGrpc(c context.Context, l net.Listener, tracer common.TracingServer) error {
	defer func() {
		// Error recovery.
		if perr := derror.PanicToError(recover()); perr != nil {
			dlog.Errorf(c, "%+v", perr)
		}
	}()

	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
		grpc.StreamInterceptor(otelgrpc.StreamServerInterceptor()),
	}
	cfg := client.GetConfig(c)
	if !cfg.Grpc.MaxReceiveSize.IsZero() {
		if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
			opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
		}
	}
	svc := grpc.NewServer(opts...)
	rpc.RegisterDaemonServer(svc, s)
	common.RegisterTracingServer(svc, tracer)

	sc := &dhttp.ServerConfig{
		Handler: svc,
	}
	dlog.Info(c, "gRPC server started")
	err := sc.Serve(c, l)
	if err != nil {
		dlog.Errorf(c, "gRPC server ended with: %v", err)
	} else {
		dlog.Debug(c, "gRPC server ended")
	}
	return err
}

// run is the main function when executing as the daemon.
func run(c context.Context, loggingDir, configDir string) error {
	if !proc.IsAdmin() {
		return fmt.Errorf("telepresence %s must run with elevated privileges", ProcessName)
	}

	// seed random generator (used when shuffling IPs)
	rand.Seed(time.Now().UnixNano())

	// Spoof the AppUserLogDir and AppUserConfigDir so that they return the original user's
	// directories rather than directories for the root user.
	c = filelocation.WithAppUserLogDir(c, loggingDir)
	c = filelocation.WithAppUserConfigDir(c, configDir)

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

	tracer, err := tracing.NewTraceServer(c, "root-daemon")
	if err != nil {
		return err
	}

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", ProcessName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	// Listen on domain unix domain socket or windows named pipe. The listener must be opened
	// before other tasks because the CLI client will only wait for a short period of time for
	// the socket/pipe to appear before it gives up.
	grpcListener, err := client.ListenSocket(c, ProcessName, client.DaemonSocketName)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.RemoveSocket(grpcListener)
	}()
	dlog.Debug(c, "Listener opened")

	d := GetNewServiceFunc(c)(scout.NewReporter(c, "daemon"), cfg)
	if err = logging.LoadTimedLevelFromCache(c, d.timedLogLevel, ProcessName); err != nil {
		return err
	}
	vif.InitLogger(c)

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// Add a reload function that triggers on create and write of the config.yml file.
	g.Go("config-reload", d.configReload)
	g.Go("session", d.manageSessions)
	g.Go("server-grpc", func(c context.Context) error { return d.serveGrpc(c, grpcListener, tracer) })
	g.Go("metriton", d.scout.Run)
	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
