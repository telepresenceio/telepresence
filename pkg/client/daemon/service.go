package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// service represents the state of the Telepresence Daemon
type service struct {
	rpc.UnsafeDaemonServer
	cancel        context.CancelFunc
	sessionsCtx   context.Context
	sessionLock   sync.Mutex
	session       *session
	timedLogLevel log.TimedLevel

	scoutClient *scout.Scout           // don't use this directly; use the 'scout' chan instead
	scout       chan scout.ScoutReport // any-of-scoutUsers -> background-metriton
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
	d.sessionLock.Lock()
	defer d.sessionLock.Unlock()
	r := &rpc.DaemonStatus{}
	if d.session != nil {
		r.OutboundConfig = d.session.getInfo()
	}
	return r, nil
}

func (d *service) Quit(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(ctx, "Received gRPC Quit")
	d.disconnect(ctx)
	d.cancel()
	return &empty.Empty{}, nil
}

func (d *service) SetDnsSearchPath(ctx context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	session, err := d.currentSession()
	if err != nil {
		return nil, err
	}
	session.SetSearchPath(ctx, paths.Paths, paths.Namespaces)
	return &empty.Empty{}, nil
}

func (d *service) Connect(ctx context.Context, info *rpc.OutboundInfo) (*empty.Empty, error) {
	d.sessionLock.Lock()
	defer d.sessionLock.Unlock()
	if d.session != nil {
		return nil, status.Error(codes.AlreadyExists, "an active session exists")
	}
	s, err := newSession(d.sessionsCtx, d.scout, info)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	d.session = s
	return &empty.Empty{}, nil
}

func (d *service) Disconnect(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(ctx, "Received gRPC Disconnect")
	d.disconnect(ctx)
	return &empty.Empty{}, nil
}

func (d *service) disconnect(ctx context.Context) {
	d.sessionLock.Lock()
	defer d.sessionLock.Unlock()
	if d.session != nil {
		d.session.stop(ctx)
		d.session = nil
	}
}

func (d *service) currentSession() (*session, error) {
	d.sessionLock.Lock()
	defer d.sessionLock.Unlock()
	if d.session == nil {
		return nil, status.Error(codes.Unavailable, "no active session")
	}
	return d.session, nil
}

func (d *service) GetClusterSubnets(ctx context.Context, _ *empty.Empty) (*rpc.ClusterSubnets, error) {
	session, err := d.currentSession()
	if err != nil {
		return nil, err
	}

	// The manager can sometimes send the different subnets in different Sends, but after 5 seconds of listening to it
	// we should expect to have everything
	tCtx, tCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tCancel()
	infoStream, err := session.managerClient.WatchClusterInfo(tCtx, session.session)
	if err != nil {
		return nil, err
	}
	podSubnets := []*manager.IPNet{}
	svcSubnets := []*manager.IPNet{}
	for {
		mgrInfo, err := infoStream.Recv()
		if err != nil {
			if tCtx.Err() == nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
		if mgrInfo.ServiceSubnet != nil {
			svcSubnets = append(svcSubnets, mgrInfo.ServiceSubnet)
		}
		podSubnets = append(podSubnets, mgrInfo.PodSubnets...)
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

// run is the main function when executing as the daemon
func run(c context.Context, loggingDir, configDir string) error {
	if !proc.IsAdmin() {
		return fmt.Errorf("telepresence %s must run with elevated privileges", ProcessName)
	}

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
	c, err = logging.InitContext(c, ProcessName)
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
		scoutClient:   scout.NewScout(c, "daemon"),
		scout:         make(chan scout.ScoutReport, 25),
		timedLogLevel: log.NewTimedLevel(cfg.LogLevels.RootDaemon.String(), log.SetLevel),
	}
	defer close(d.scout)

	if err = logging.LoadTimedLevelFromCache(c, d.timedLogLevel, ProcessName); err != nil {
		return err
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	g.Go("session", func(c context.Context) (err error) {
		c, d.cancel = context.WithCancel(c)
		d.sessionsCtx = c
		<-c.Done()
		return nil
	})

	g.Go("server-grpc", func(c context.Context) (err error) {
		defer func() {
			// Error recovery.
			if perr := derror.PanicToError(recover()); perr != nil {
				dlog.Error(c, perr)
			}
		}()

		defer func() {
			if err != nil {
				dlog.Errorf(c, "gRPC server ended with: %v", err)
			} else {
				dlog.Debug(c, "gRPC server ended")
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
		return sc.Serve(c, grpcListener)
	})

	// metriton is the goroutine that handles all telemetry reports, so that calls to
	// metriton don't block the functional goroutines.
	g.Go("metriton", func(c context.Context) error {
		for {
			select {
			case <-c.Done():
				return nil
			case report := <-d.scout:
				for k, v := range report.PersistentMetadata {
					d.scoutClient.SetMetadatum(k, v)
				}
				var metadata []scout.ScoutMeta
				for k, v := range report.Metadata {
					metadata = append(metadata, scout.ScoutMeta{
						Key:   k,
						Value: v,
					})
				}
				d.scoutClient.Report(c, report.Action, metadata...)
			}
		}
	})
	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
