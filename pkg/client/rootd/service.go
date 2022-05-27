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
	"time"

	"github.com/spf13/cobra"
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
)

const ProcessName = "daemon"
const titleName = "Daemon"

var help = `The Telepresence ` + titleName + ` is a long-lived background component that manages
connections and network state.

Launch the Telepresence ` + titleName + `:
    sudo telepresence service

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(func() string { dir, _ := filelocation.AppUserLogDir(context.Background()); return dir }(), ProcessName+".log") + `
to troubleshoot problems.
`

type sessionReply struct {
	status *rpc.DaemonStatus
	err    error
}

// service represents the state of the Telepresence Daemon
type service struct {
	rpc.UnsafeDaemonServer
	quit           context.CancelFunc
	connectCh      chan *rpc.OutboundInfo
	connectReplyCh chan sessionReply
	sessionLock    sync.RWMutex
	sessionContext context.Context
	session        *session
	cancelCh       chan struct{}
	timedLogLevel  log.TimedLevel

	scout *scout.Reporter
}

// Command returns the telepresence sub-command "daemon-foreground"
func Command() *cobra.Command {
	return &cobra.Command{
		Use:    ProcessName + "-foreground <logging dir> <config dir>",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		Long:   help,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), args[0], args[1])
		},
	}
}

func (d *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (d *service) Status(_ context.Context, _ *empty.Empty) (*rpc.DaemonStatus, error) {
	d.sessionLock.RLock()
	defer d.sessionLock.RUnlock()
	r := &rpc.DaemonStatus{}
	if d.session != nil {
		r.OutboundConfig = d.session.getInfo()
	}
	return r, nil
}

func (d *service) Quit(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(ctx, "Received gRPC Quit")
	d.sessionLock.Lock()
	defer d.sessionLock.Unlock()
	d.session = nil
	d.quit()
	return &empty.Empty{}, nil
}

func (d *service) SetDnsSearchPath(ctx context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	err := d.withSession(ctx, func(ctx context.Context, session *session) error {
		session.SetSearchPath(ctx, paths.Paths, paths.Namespaces)
		return nil
	})
	return &empty.Empty{}, err
}

func (d *service) Connect(ctx context.Context, info *rpc.OutboundInfo) (*rpc.DaemonStatus, error) {
	dlog.Debug(ctx, "Received gRPC Connect")
	select {
	case <-ctx.Done():
		return nil, status.Error(codes.Canceled, ctx.Err().Error())
	case d.connectCh <- info:
	}
	select {
	case <-ctx.Done():
		return nil, status.Error(codes.Canceled, ctx.Err().Error())
	case reply := <-d.connectReplyCh:
		return reply.status, reply.err
	}
}

func (d *service) Disconnect(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(ctx, "Received gRPC Disconnect")
	d.cancelSession()
	return &empty.Empty{}, nil
}

func (d *service) cancelSession() {
	d.sessionLock.Lock()
	defer d.sessionLock.Unlock()
	if d.cancelCh != nil || d.session == nil {
		// avoid repeated cancellations
		return
	}
	defer func() {
		d.session = nil
	}()
	d.cancelCh = make(chan struct{})
	d.session.cancel()
}

func (d *service) withSession(c context.Context, f func(context.Context, *session) error) error {
	d.sessionLock.RLock()
	defer d.sessionLock.RUnlock()
	if d.session == nil {
		return status.Error(codes.Unavailable, "no active session")
	}
	return f(d.sessionContext, d.session)
}

func (d *service) GetClusterSubnets(ctx context.Context, _ *empty.Empty) (*rpc.ClusterSubnets, error) {
	podSubnets := []*manager.IPNet{}
	svcSubnets := []*manager.IPNet{}
	err := d.withSession(ctx, func(ctx context.Context, session *session) error {
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

func (d *service) SetLogLevel(ctx context.Context, request *manager.LogLevelRequest) (*empty.Empty, error) {
	duration := time.Duration(0)
	if request.Duration != nil {
		duration = request.Duration.AsDuration()
	}
	return &empty.Empty{}, logging.SetAndStoreTimedLevel(ctx, d.timedLogLevel, request.LogLevel, duration, ProcessName)
}

func (d *service) configReload(c context.Context) error {
	return client.Watch(c, func(c context.Context) error {
		return logging.ReloadDaemonConfig(c, true)
	})
}

// manageSessions is the counterpart to the Connect method. It reads the connectCh, creates
// a session and writes a reply to the connectErrCh. The session is then started if it was
// successfully created.
func (d *service) manageSessions(c context.Context) error {
	// The d.quit is called when we receive a Quit. Since it
	// terminates this function, it terminates the whole process.
	wg := sync.WaitGroup{}
	c, d.quit = context.WithCancel(c)
nextSession:
	for {
		// Wait for a connection request
		var oi *rpc.OutboundInfo
		select {
		case <-c.Done():
			break nextSession
		case oi = <-d.connectCh:
		}

		var session *session
		reply := sessionReply{}

		d.sessionLock.Lock()
		// If a cancelCh exists, we must wait for it to close and
		// then check for it again. This ensures that the session
		// cannot be accessed during shutdown and that a new connect
		// must wait for an old to complete.
		for {
			if cancelCh := d.cancelCh; cancelCh != nil {
				d.sessionLock.Unlock()
				select {
				case <-c.Done():
					break nextSession
				case <-cancelCh:
					d.sessionLock.Lock()
				}
			} else {
				break
			}
		}

		// Respond by setting the session and returning the error (or nil
		// if everything is ok)
		if d.session != nil {
			reply.status = &rpc.DaemonStatus{OutboundConfig: d.session.getInfo()}
		} else {
			session, reply.err = newSession(c, d.scout, oi)
			if reply.err == nil {
				d.session = session
				d.sessionContext, session.cancel = context.WithCancel(c)
				reply.status = &rpc.DaemonStatus{OutboundConfig: d.session.getInfo()}
			}
		}
		d.sessionLock.Unlock()

		select {
		case <-c.Done():
			break nextSession
		case d.connectReplyCh <- reply:
		default:
			// Nobody left to read the response? That's fine really. Just means that
			// whoever wanted to start the session terminated early.
			d.cancelSession()
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
				d.sessionLock.Lock()
				if d.cancelCh != nil {
					close(d.cancelCh)
					d.cancelCh = nil
				}
				d.sessionLock.Unlock()
				wg.Done()
			}()
			if err := d.session.run(d.sessionContext); err != nil {
				dlog.Error(c, err)
			}
		}()
	}
	wg.Wait()
	return nil
}

func (d *service) serveGrpc(c context.Context, l net.Listener) error {
	defer func() {
		// Error recovery.
		if perr := derror.PanicToError(recover()); perr != nil {
			dlog.Errorf(c, "%+v", perr)
		}
	}()

	var opts []grpc.ServerOption
	cfg := client.GetConfig(c)
	if !cfg.Grpc.MaxReceiveSize.IsZero() {
		if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
			opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
		}
	}
	svc := grpc.NewServer(opts...)
	rpc.RegisterDaemonServer(svc, d)

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

// run is the main function when executing as the daemon
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

	d := &service{
		scout:          scout.NewReporter(c, "daemon"),
		timedLogLevel:  log.NewTimedLevel(cfg.LogLevels.RootDaemon.String(), log.SetLevel),
		connectCh:      make(chan *rpc.OutboundInfo),
		connectReplyCh: make(chan sessionReply),
	}
	if err = logging.LoadTimedLevelFromCache(c, d.timedLogLevel, ProcessName); err != nil {
		return err
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// Add a reload function that triggers on create and write of the config.yml file.
	g.Go("config-reload", d.configReload)
	g.Go("session", d.manageSessions)
	g.Go("server-grpc", func(c context.Context) error { return d.serveGrpc(c, grpcListener) })
	g.Go("metriton", d.scout.Run)
	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
