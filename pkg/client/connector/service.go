package connector

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh/terminal"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cache"
	"github.com/datawire/telepresence2/rpc/common"
	rpc "github.com/datawire/telepresence2/rpc/connector"
	"github.com/datawire/telepresence2/rpc/daemon"
	"github.com/datawire/telepresence2/rpc/manager"
)

var help = `The Telepresence Connect is a background component that manages a connection. It
requires that a daemon is already running.

Launch the Telepresence Connector:
    telepresence connect

Examine the Connector's log output in
    ` +
	func() string {
		cachedir, err := cache.UserCacheDir()
		if err != nil {
			cachedir = "${user_cache_dir}"
		}
		return filepath.Join(cachedir, "telepresence", "connector.log")
	}() + `
to troubleshoot problems.
`

// service represents the state of the Telepresence Connector
type service struct {
	rpc.UnsafeConnectorServer
	env          client.Env
	daemon       daemon.DaemonClient
	cluster      *k8sCluster
	bridge       *bridge
	trafficMgr   *trafficManager
	managerProxy mgrProxy
	ctx          context.Context
	cancel       func()
}

// Command returns the CLI sub-command for "connector-foreground"
func Command() *cobra.Command {
	c := &cobra.Command{
		Use:    "connector-foreground",
		Short:  "Launch Telepresence Connector in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		Long:   help,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context())
		},
	}
	return c
}

type callCtx struct {
	context.Context
	caller context.Context
}

func (c callCtx) Deadline() (deadline time.Time, ok bool) {
	if dl, ok := c.Context.Deadline(); ok {
		return dl, true
	}
	return c.caller.Deadline()
}

func (c callCtx) Done() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		select {
		case <-c.Context.Done():
			close(ch)
		case <-c.caller.Done():
			close(ch)
		}
	}()
	return ch
}

func (c callCtx) Err() error {
	err := c.Context.Err()
	if err == nil {
		err = c.caller.Err()
	}
	return err
}

func (c callCtx) Value(key interface{}) interface{} {
	return c.Context.Value(key)
}

func callRecovery(c context.Context, r interface{}, err error) error {
	perr := dutil.PanicToError(r)
	if perr != nil {
		if err == nil {
			err = perr
		} else {
			dlog.Errorf(c, "%+v", perr)
		}
	}
	if err != nil {
		dlog.Errorf(c, "%+v", err)
	}
	return err
}

var ucn int64 = 0

func nextUcn() int {
	return int(atomic.AddInt64(&ucn, 1))
}

func callName(s string) string {
	return fmt.Sprintf("%s-%d", s, nextUcn())
}

func (s *service) callCtx(c context.Context, name string) context.Context {
	return dgroup.WithGoroutineName(&callCtx{Context: s.ctx, caller: c}, callName(name))
}

func (s *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *service) Connect(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	c = s.callCtx(c, "Connect")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.connect(c, cr), nil
}

func (s *service) CreateIntercept(c context.Context, ir *manager.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	ie, is := s.interceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	c = s.callCtx(c, "CreateIntercept")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.trafficMgr.addIntercept(c, s.ctx, ir)
}

func (s *service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (result *rpc.InterceptResult, err error) {
	ie, is := s.interceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	c = s.callCtx(c, "RemoveIntercept")
	defer func() { err = callRecovery(c, recover(), err) }()
	err = s.trafficMgr.removeIntercept(c, rr.Name)
	return &rpc.InterceptResult{}, err
}

func (s *service) List(ctx context.Context, lr *rpc.ListRequest) (*rpc.DeploymentInfoSnapshot, error) {
	if s.trafficMgr.managerClient == nil {
		return &rpc.DeploymentInfoSnapshot{}, nil
	}
	return s.trafficMgr.deploymentInfoSnapshot(ctx, lr.Filter), nil
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *rpc.UninstallResult, err error) {
	c = s.callCtx(c, "Uninstall")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.trafficMgr.uninstall(c, ur)
}

func (s *service) Quit(_ context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.cancel()
	return &empty.Empty{}, nil
}

// connect the connector to a cluster
func (s *service) connect(c context.Context, cr *rpc.ConnectRequest) *rpc.ConnectInfo {
	r := &rpc.ConnectInfo{}
	setStatus := func() {
		r.ClusterOk = true
		r.ClusterContext = s.cluster.Context
		r.ClusterServer = s.cluster.server()
		if s.bridge != nil {
			r.BridgeOk = s.bridge.check(c)
		}
		if s.trafficMgr != nil {
			s.trafficMgr.setStatus(c, r)
		}
		r.IngressInfos = s.cluster.detectIngressBehavior()
	}

	// Sanity checks
	if s.cluster != nil {
		setStatus()
		r.Error = rpc.ConnectInfo_ALREADY_CONNECTED
		return r
	}

	if s.bridge != nil {
		r.Error = rpc.ConnectInfo_DISCONNECTING
		return r
	}

	dgroup.ParentGroup(s.ctx).Go(callName("metriton"), func(c context.Context) error {
		reporter := &metriton.Reporter{
			Application:  "telepresence2",
			Version:      client.Version(),
			GetInstallID: func(_ *metriton.Reporter) (string, error) { return cr.InstallId, nil },
			BaseMetadata: map[string]interface{}{"mode": "daemon"},
		}

		if _, err := reporter.Report(c, map[string]interface{}{"action": "connect"}); err != nil {
			dlog.Errorf(c, "report failed: %+v", err)
		}
		return nil // error is logged and is not fatal
	})

	dlog.Info(c, "Connecting to traffic manager...")
	cluster, err := trackKCluster(s.ctx, cr.Kubeflags, s.daemon)
	if err != nil {
		dlog.Errorf(c, "unable to track k8s cluster: %+v", err)
		r.Error = rpc.ConnectInfo_CLUSTER_FAILED
		r.ErrorText = err.Error()
		s.cancel()
		return r
	}
	s.cluster = cluster

	/*
		previewHost, err := cluster.getClusterPreviewHostname(p)
		if err != nil {
			p.Logf("get preview URL hostname: %+v", err)
			previewHost = ""
		}
	*/

	dlog.Infof(c, "Connected to context %s (%s)", s.cluster.Context, s.cluster.server())

	tmgr, err := newTrafficManager(s.ctx, s.env, s.cluster, cr.InstallId)
	if err != nil {
		dlog.Errorf(c, "Unable to connect to TrafficManager: %s", err)
		r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
		r.ErrorText = err.Error()
		// No point in continuing without a traffic manager
		s.cancel()
		return r
	}

	s.trafficMgr = tmgr
	// Wait for traffic manager to connect
	dlog.Info(c, "Waiting for TrafficManager to connect")
	if err := tmgr.waitUntilStarted(); err != nil {
		dlog.Errorf(c, "Failed to start traffic-manager: %v", err)
		r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
		r.ErrorText = err.Error()
		// No point in continuing without a traffic manager
		s.cancel()
		return r
	}
	s.managerProxy.SetClient(tmgr.managerClient)

	dlog.Infof(c, "Starting traffic-manager bridge in context %s, namespace %s", cluster.Context, cluster.Namespace)
	br := newBridge(cluster.Namespace, s.daemon, tmgr.sshPort)
	err = br.start(s.ctx)
	if err != nil {
		dlog.Errorf(c, "Failed to start traffic-manager bridge: %v", err)
		r.Error = rpc.ConnectInfo_BRIDGE_FAILED
		r.ErrorText = err.Error()
		// No point in continuing without a bridge
		s.cancel()
		return r
	}

	s.bridge = br
	setStatus()
	return r
}

func setupLogging(ctx context.Context) (context.Context, error) {
	// Whenever we start, log to "connector.log", but first rename any existing "connector.log"
	// to "connector.log.old"; so the last 2 logs are always available (a poor-man's logrotate).
	// This is what X11 does with "$XDG_DATA_HOME/xorg/Xorg.${display_number}.log".
	//
	// (Except we use XDG_CACHE_HOME not XDG_DATA_HOME, because it's always bothered me when
	// things put logs in XDG_DATA_HOME -- XDG_DATA_HOME is for "user-specific data", and
	// XDG_CACHE_HOME is for "user-specific non-essential (cached) data"[1]; logs are
	// non-essential!  A good rule of thumb is: If you track your configuration with Git, and
	// you wouldn't check a given file in to Git (possibly encrypting it before checking it in),
	// then that file either needs to go in XDG_RUNTIME_DIR or XDG_CACHE_DIR; and NOT
	// XDG_DATA_HOME or XDG_CONFIG_HOME.)
	//
	// [1]: https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html
	//
	// In the past there was a mechanism where the connector RPC'd its logs over to the daemon,
	// and the daemon put them in to a unified log file.  This turned out to make things hard to
	// debug--it was hard to tell where a log line was coming from, and some things were
	// missing.  Logs related to RPC problems nescessarily got dropped, and things that didn't
	// play nice/go through our logger (things we haven't audited as thoroughly;
	// *cough*client-go*cough*) ended up getting their logs dropped; and those are all cases
	// where we *especially* want the logs.
	cachedir, err := cache.CacheDir()
	if err != nil {
		return ctx, err
	}
	logfilename := filepath.Join(cachedir, "connector.log")
	// Rename the existing .log to .log.old even if we're logging to stdout (below); this way
	// you can't get confused and think that "connector.log" is the logs of the currently
	// running connector even when it's not.
	_ = os.Rename(logfilename, logfilename+".old")

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.Formatter = client.NewFormatter("15:04:05")

	if !terminal.IsTerminal(int(os.Stdout.Fd())) {
		logger.Formatter = client.NewFormatter("2006/01/02 15:04:05")

		logfile, err := os.OpenFile(logfilename, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			return ctx, err
		}
		defer logfile.Close()

		// https://github.com/golang/go/issues/325
		_ = syscall.Dup2(int(logfile.Fd()), int(os.Stdout.Fd()))
		_ = syscall.Dup2(int(logfile.Fd()), int(os.Stderr.Fd()))
	}

	return dlog.WithLogger(ctx, dlog.WrapLogrus(logger)), nil
}

// run is the main function when executing as the connector
func run(c context.Context) error {
	c, err := setupLogging(c)
	if err != nil {
		return err
	}

	env, err := client.LoadEnv(c)
	if err != nil {
		return err
	}

	if client.SocketExists(client.ConnectorSocketName) {
		return fmt.Errorf("socket %s exists so connector already started or terminated ungracefully",
			client.SocketURL(client.ConnectorSocketName))
	}
	defer func() {
		if perr := dutil.PanicToError(recover()); perr != nil {
			dlog.Error(c, perr)
		}
		_ = os.Remove(client.ConnectorSocketName)
	}()

	// establish a connection to the daemon gRPC service
	conn, err := client.DialSocket(c, client.DaemonSocketName)
	if err != nil {
		return err
	}
	defer conn.Close()
	s := &service{
		env:    env,
		daemon: daemon.NewDaemonClient(conn),
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	s.cancel = func() { g.Go("connector-quit", func(_ context.Context) error { return nil }) }

	g.Go("connector", func(c context.Context) (err error) {
		listener, err := net.Listen("unix", client.ConnectorSocketName)
		if err != nil {
			return err
		}

		defer func() {
			if perr := dutil.PanicToError(recover()); perr != nil {
				dlog.Error(c, perr)
			}
			_ = listener.Close()
			if err != nil {
				dlog.Errorf(c, "Server ended with: %v", err)
			} else {
				dlog.Debug(c, "Server ended")
			}
		}()

		// Listen on unix domain socket

		dlog.Info(c, "---")
		dlog.Infof(c, "Telepresence Connector %s starting...", client.DisplayVersion())
		dlog.Infof(c, "PID is %d", os.Getpid())
		dlog.Info(c, "")

		svc := grpc.NewServer()
		rpc.RegisterConnectorServer(svc, s)
		manager.RegisterManagerServer(svc, &s.managerProxy)

		// Need a subgroup here because several services started by incoming gRPC calls run using
		// dgroup.ParentGroup().Go()
		dgroup.NewGroup(c, dgroup.GroupConfig{}).Go("server", func(c context.Context) error {
			s.ctx = c
			<-c.Done()
			dlog.Info(c, "Shutting down")
			svc.GracefulStop()
			return nil
		})
		err = svc.Serve(listener)
		dlog.Info(c, "Done serving")
		return err
	})

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
