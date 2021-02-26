package connector

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/datawire/telepresence2/rpc/v2/common"
	rpc "github.com/datawire/telepresence2/rpc/v2/connector"
	"github.com/datawire/telepresence2/rpc/v2/daemon"
	"github.com/datawire/telepresence2/rpc/v2/manager"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/client/logging"
	"github.com/datawire/telepresence2/v2/pkg/filelocation"
)

const processName = "connector"
const titleName = "Connector"

var help = `The Telepresence ` + titleName + ` is a background component that manages a connection. It
requires that a daemon is already running.

Launch the Telepresence ` + titleName + `:
    telepresence connect

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(func() string { dir, _ := filelocation.AppUserLogDir(context.Background()); return dir }(), processName+".log") + `
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
		Use:    processName + "-foreground",
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

func (s *service) ConnectCluster(c context.Context, cr *rpc.ConnectRequest) (clusterInfo *rpc.ClusterInfo, err error) {
	c = s.callCtx(c, "ConnectCluster")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.connectCluster(c, cr), nil
}

func (s *service) Connect(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	c = s.callCtx(c, "Connect")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.connect(c, cr), nil
}

func (s *service) CreateIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	ie, is := s.interceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	c = s.callCtx(c, "CreateIntercept")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.trafficMgr.addIntercept(c, ir)
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
	return s.trafficMgr.deploymentInfoSnapshot(ctx, lr), nil
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

// Most of this used to be part of the connect function. But since we want to
// let the user know connecting to the traffic manager might be slow if they
// have many services, we separate this chunk out.
func (s *service) connectCluster(c context.Context, cr *rpc.ConnectRequest) *rpc.ClusterInfo {
	r := &rpc.ClusterInfo{}
	configAndFlags, err := newConfigAndFlags(cr.KubeFlags, cr.MappedNamespaces)
	if err != nil {
		r.Error = rpc.ClusterInfo_CLUSTER_FAILED
		r.ErrorText = err.Error()
		return r
	}

	// Sanity checks
	if s.cluster != nil {
		if s.cluster.equals(configAndFlags) {
			mns := configAndFlags.mappedNamespaces
			if len(mns) > 0 {
				if len(mns) == 1 && mns[0] == "all" {
					mns = nil
				}
				s.cluster.setMappedNamespaces(c, mns)
			}
			r.Error = rpc.ClusterInfo_ALREADY_CONNECTED
		} else {
			r.Error = rpc.ClusterInfo_MUST_RESTART
		}
		return r
	}

	// If the cluster is nil but the bridge isn't, then we
	// are disconnecting.
	if s.bridge != nil {
		r.Error = rpc.ClusterInfo_DISCONNECTING
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
	cluster, err := trackKCluster(s.ctx, configAndFlags, s.daemon)
	if err != nil {
		dlog.Errorf(c, "unable to track k8s cluster: %+v", err)
		r.Error = rpc.ClusterInfo_CLUSTER_FAILED
		r.ErrorText = err.Error()
		s.cancel()
		return r
	}
	s.cluster = cluster
	dlog.Infof(c, "Connected to context %s (%s)", s.cluster.Context, s.cluster.Server)

	k8sObjectMap := cluster.findNumK8sObjects()
	r.Services = int32(k8sObjectMap["services"])
	r.Pods = int32(k8sObjectMap["pods"])
	r.Namespaces = int32(k8sObjectMap["namespaces"])
	return r
}

// connect the connector to a cluster
func (s *service) connect(c context.Context, cr *rpc.ConnectRequest) *rpc.ConnectInfo {
	r := &rpc.ConnectInfo{}
	setStatus := func() {
		r.ClusterOk = true
		r.ClusterContext = s.cluster.Context
		r.ClusterServer = s.cluster.Server
		r.ClusterId = s.cluster.getClusterId(c)
		if s.bridge != nil {
			r.BridgeOk = s.bridge.check(c)
		}
		if s.trafficMgr != nil {
			s.trafficMgr.setStatus(c, r)
		}
		r.IngressInfos = s.cluster.detectIngressBehavior()
	}

	// If the bridge isn't nil, then we've already connected to the
	// traffic manager, so update the status and return early
	if s.bridge != nil {
		setStatus()
		r.Error = rpc.ConnectInfo_ALREADY_CONNECTED
		return r
	}
	// Phone home with the information about the size of the cluster
	k8sObjectMap := s.cluster.findNumK8sObjects()
	scout := client.NewScout("cli")
	scout.SetMetadatum("cluster_id", s.cluster.getClusterId(c))
	for objectType, num := range k8sObjectMap {
		scout.SetMetadatum(objectType, num)
	}
	scout.SetMetadatum("mapped_namespaces", len(cr.MappedNamespaces))
	_ = scout.Report(c, "connecting_traffic_manager")

	connectStart := time.Now()
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
	if err := tmgr.waitUntilStarted(c); err != nil {
		dlog.Errorf(c, "Failed to start traffic-manager: %v", err)
		r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
		r.ErrorText = err.Error()
		// No point in continuing without a traffic manager
		s.cancel()
		return r
	}
	s.managerProxy.SetClient(tmgr.managerClient)

	dlog.Infof(c, "Starting traffic-manager bridge in context %s", s.cluster.Context)
	br := newBridge(s.daemon, tmgr.sshPort)
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

	// Collect data on how long connection time took
	connectDuration := time.Since(connectStart)
	scout.SetMetadatum("connect_duration", connectDuration.Seconds())
	_ = scout.Report(c, "finished_connecting_traffic_manager")
	return r
}

// run is the main function when executing as the connector
func run(c context.Context) error {
	c, err := logging.InitContext(c, processName)
	if err != nil {
		return err
	}

	env, err := client.LoadEnv(c)
	if err != nil {
		return err
	}

	if client.SocketExists(client.ConnectorSocketName) {
		return fmt.Errorf("socket %s exists so %s already started or terminated ungracefully",
			client.SocketURL(client.ConnectorSocketName), processName)
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

	s.cancel = func() { g.Go(processName+"-quit", func(_ context.Context) error { return nil }) }

	g.Go(processName, func(c context.Context) (err error) {
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
		dlog.Infof(c, "Telepresence %s %s starting...", titleName, client.DisplayVersion())
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
