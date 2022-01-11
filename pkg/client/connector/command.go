package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/broadcastqueue"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
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

type parsedConnectRequest struct {
	*rpc.ConnectRequest
	*userd_k8s.Config
}

// service represents the state of the Telepresence Connector
type service struct {
	scout *scout.Reporter

	cancel func()

	// Must hold connectMu to use the sharedState.MaybeSetXXX methods.
	connectMu   sync.Mutex
	sharedState *userd_trafficmgr.State

	// These are used to communicate between the various goroutines.
	connectRequest  chan parsedConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo     // connectWorker -> server-grpc.connect()
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

func connectError(t rpc.ConnectInfo_ErrType, err error) *rpc.ConnectInfo {
	return &rpc.ConnectInfo{
		Error:         t,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

// connect the connector to a cluster
func (s *service) connect(c context.Context, cr *rpc.ConnectRequest, dryRun bool) *rpc.ConnectInfo {
	s.connectMu.Lock()
	defer s.connectMu.Unlock()

	config, err := userd_k8s.NewConfig(c, cr.KubeFlags)
	if err != nil && !dryRun {
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	if cluster := s.sharedState.GetClusterNonBlocking(); cluster != nil {
		if cluster.Config.ContextServiceAndFlagsEqual(config) {
			cluster.Config = config // namespace might have changed
			if mns := cr.MappedNamespaces; len(mns) > 0 {
				if len(mns) == 1 && mns[0] == "all" {
					mns = nil
				}
				sort.Strings(mns)
				cluster.SetMappedNamespaces(c, mns)
			}
			ingressInfo, err := cluster.DetectIngressBehavior(c)
			if err != nil {
				return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
			}
			ret := &rpc.ConnectInfo{
				Error:          rpc.ConnectInfo_ALREADY_CONNECTED,
				ClusterContext: cluster.Config.Context,
				ClusterServer:  cluster.Config.Server,
				ClusterId:      cluster.GetClusterId(c),
				IngressInfos:   ingressInfo,
			}
			s.sharedState.GetTrafficManagerNonBlocking().SetStatus(c, ret)
			return ret
		} else {
			ret := &rpc.ConnectInfo{
				Error:          rpc.ConnectInfo_MUST_RESTART,
				ClusterContext: cluster.Config.Context,
				ClusterServer:  cluster.Config.Server,
				ClusterId:      cluster.GetClusterId(c),
			}
			s.sharedState.GetTrafficManagerNonBlocking().SetStatus(c, ret)
			return ret
		}
	} else {
		// This is the first call to Connect; we have to tell the background connect
		// goroutine to actually do the work.
		if dryRun {
			return &rpc.ConnectInfo{
				Error: rpc.ConnectInfo_DISCONNECTED,
			}
		} else {
			s.connectRequest <- parsedConnectRequest{
				ConnectRequest: cr,
				Config:         config,
			}
			close(s.connectRequest)
			return <-s.connectResponse
		}
	}
}

func (s *service) connectWorker(c context.Context, cr *rpc.ConnectRequest, k8sConfig *userd_k8s.Config, svc *grpc.Server, le userd_auth.LoginExecutor) *rpc.ConnectInfo {
	mappedNamespaces := cr.MappedNamespaces
	if len(mappedNamespaces) == 1 && mappedNamespaces[0] == "all" {
		mappedNamespaces = nil
	}
	sort.Strings(mappedNamespaces)

	s.scout.Report(c, "connect")

	// establish a connection to the daemon gRPC service
	dlog.Info(c, "Connecting to daemon...")
	conn, err := client.DialSocket(c, client.DaemonSocketName)
	if err != nil {
		dlog.Errorf(c, "unable to connect to daemon: %+v", err)
		s.sharedState.MaybeSetCluster(nil)
		s.sharedState.MaybeSetTrafficManager(nil)
		s.cancel()
		return connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
	}
	// Don't bother calling 'conn.Close()', it should remain open until we shut down, and just
	// prefer to let the OS close it when we exit.
	daemonClient := daemon.NewDaemonClient(conn)

	dlog.Info(c, "Connecting to k8s cluster...")
	cluster, err := func() (*userd_k8s.Cluster, error) {
		c, cancel := client.GetConfig(c).Timeouts.TimeoutContext(c, client.TimeoutClusterConnect)
		defer cancel()
		cluster, err := userd_k8s.NewCluster(c,
			k8sConfig,
			mappedNamespaces,
			userd_k8s.Callbacks{
				SetDNSSearchPath: daemonClient.SetDnsSearchPath,
			},
		)
		if err != nil {
			return nil, err
		}
		return cluster, nil
	}()
	if err != nil {
		dlog.Errorf(c, "unable to track k8s cluster: %+v", err)
		s.sharedState.MaybeSetCluster(nil)
		s.sharedState.MaybeSetTrafficManager(nil)
		s.cancel()
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	s.sharedState.MaybeSetCluster(cluster)
	dlog.Infof(c, "Connected to context %s (%s)", cluster.Context, cluster.Server)

	// Phone home with the information about the size of the cluster
	s.scout.SetMetadatum(c, "cluster_id", cluster.GetClusterId(c))
	s.scout.Report(c, "connecting_traffic_manager", scout.Entry{
		Key:   "mapped_namespaces",
		Value: len(cr.MappedNamespaces),
	})

	connectStart := time.Now()

	dlog.Info(c, "Connecting to traffic manager...")
	tmgr, err := userd_trafficmgr.New(c,
		cluster,
		s.scout.InstallID(),
		userd_trafficmgr.Callbacks{
			GetCloudAPIKey: le.GetCloudAPIKey,
			RegisterManagerServer: func(mgrSrv manager.ManagerServer) {
				manager.RegisterManagerServer(svc, mgrSrv)
			},
			Connect: daemonClient.Connect,
		})
	if err != nil {
		dlog.Errorf(c, "Unable to connect to TrafficManager: %s", err)
		// No point in continuing without a traffic manager
		s.sharedState.MaybeSetTrafficManager(nil)
		s.cancel()
		return connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}
	s.sharedState.MaybeSetTrafficManager(tmgr)

	// Wait for traffic manager to connect
	dlog.Info(c, "Waiting for TrafficManager to connect")
	tc, cancel := client.GetConfig(c).Timeouts.TimeoutContext(c, client.TimeoutTrafficManagerConnect)
	defer cancel()
	if _, err := tmgr.GetClientBlocking(tc); err != nil {
		dlog.Errorf(c, "Failed to initialize session with traffic-manager: %v", err)
		// No point in continuing without a traffic manager
		s.cancel()
		return connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}

	// Wait until all of the k8s watches (in the "background-k8swatch" goroutine) are running.
	if err = cluster.WaitUntilReady(c); err != nil {
		s.cancel()
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}

	// Collect data on how long connection time took
	s.scout.Report(c, "finished_connecting_traffic_manager", scout.Entry{
		Key: "connect_duration", Value: time.Since(connectStart).Seconds()})

	ingressInfo, err := cluster.DetectIngressBehavior(c)
	if err != nil {
		s.cancel()
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}

	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_UNSPECIFIED,
		ClusterContext: cluster.Config.Context,
		ClusterServer:  cluster.Config.Server,
		ClusterId:      cluster.GetClusterId(c),
		IngressInfos:   ingressInfo,
	}
	tmgr.SetStatus(c, ret)
	return ret
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

	s := &service{
		scout:           scout.NewReporter(c, "connector"),
		connectRequest:  make(chan parsedConnectRequest),
		connectResponse: make(chan *rpc.ConnectInfo),
	}
	s.sharedState = userd_trafficmgr.NewState()

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	cliio := &broadcastqueue.BroadcastQueue{}
	quitOnce := sync.Once{}
	s.cancel = func() {
		quitOnce.Do(func() {
			g.Go("quit", func(_ context.Context) error {
				cliio.Close()
				return nil
			})
		})
	}
	loginExecutor := userd_auth.NewStandardLoginExecutor(cliio, s.scout)

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", titleName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	svcCh := make(chan *grpc.Server, 1)

	grpcQuitCh := make(chan func()) // Channel uses to propagate the grpcQuit cancel function. It must originate inside "server-grpc".
	g.Go("server-grpc", func(c context.Context) (err error) {
		// Prevent that the gRPC server is stopped before the "background-manager" completes. Termination goes like this:
		//
		// 1. s.cancel() is called. That starts the "quit" goroutine which exits and causes all other goroutines in the group to soft-cancel.
		// 2. The "background-manager" will then call grpcQuit() to cancel the grpcSoft context (which stems from the hard context of c, and
		//    hence isn't cancelled just yet).
		// 3. The canceling of grpcSoft shuts down the gRPC server that is started at the end of this function gracefully.
		// 4. If the server doesn't shut down, the hard context of c will eventually kill it after the SoftShutdownTimeout declared in
		//    the group.
		grpcSoft, grpcQuit := context.WithCancel(dcontext.WithSoftness(dcontext.HardContext(c)))
		grpcQuitCh <- grpcQuit
		close(grpcQuitCh)

		defer func() {
			close(svcCh)
			if perr := derror.PanicToError(recover()); perr != nil {
				dlog.Error(c, perr)
			}

			// Close s.connectRequest if it hasn't already been closed.
			select {
			case <-s.connectRequest:
			default:
				close(s.connectRequest)
			}
		}()

		opts := []grpc.ServerOption{}
		cfg := client.GetConfig(c)
		if !cfg.Grpc.MaxReceiveSize.IsZero() {
			if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
				opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
			}
		}
		svc := grpc.NewServer(opts...)
		var grpcSvc rpc.ConnectorServer
		grpcSvc, err = userd_grpc.NewGRPCService(
			c,
			ProcessName,
			userd_grpc.Callbacks{
				LoginExecutor:     loginExecutor,
				Cancel:            s.cancel,
				Connect:           s.connect,
				UserNotifications: func(ctx context.Context) <-chan string { return cliio.Subscribe(ctx) },
			},
			s.sharedState,
		)
		if err != nil {
			return err
		}
		rpc.RegisterConnectorServer(svc, grpcSvc)
		svcCh <- svc

		sc := &dhttp.ServerConfig{
			Handler: svc,
		}
		dlog.Info(c, "gRPC server started")
		if err = sc.Serve(grpcSoft, grpcListener); err != nil && c.Err() != nil {
			err = nil // Normal shutdown
		}
		if err != nil {
			dlog.Errorf(c, "gRPC server ended with: %v", err)
		} else {
			dlog.Debug(c, "gRPC server ended")
		}
		return err
	})

	// background-init handles the work done by the initial connector.Connect RPC call.  This
	// happens in a separate goroutine from the gRPC server's connection handler so that the
	// request getting cancelled doesn't cancel the work.
	g.Go("background-init", func(c context.Context) error {
		defer func() {
			close(s.connectResponse) // -> server-grpc.connect()
			s.sharedState.MaybeSetCluster(nil)
			s.sharedState.MaybeSetTrafficManager(nil)
			<-c.Done() // Don't trip ShutdownOnNonError in the parent group.
		}()

		pcr, ok := <-s.connectRequest
		if !ok {
			return nil
		}
		svc, ok := <-svcCh
		if !ok {
			return nil
		}
		s.connectResponse <- s.connectWorker(c, pcr.ConnectRequest, pcr.Config, svc, loginExecutor)

		return nil
	})

	// background-k8swatch watches all the necessary Kubernetes resources.
	g.Go("background-k8swatch", func(c context.Context) error {
		cluster, _ := s.sharedState.GetClusterBlocking(c)
		if cluster == nil {
			return nil
		}
		return cluster.RunWatchers(c)
	})

	// background-manager (1) starts up with ensuring that the manager is installed and running,
	// but then for most of its life
	//  - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
	//  - watch the intercepts (manager.WatchIntercepts) and then
	//    + (3) listen on the appropriate local ports and forward them to the intercepted
	//      Services, and
	//    + (4) mount the appropriate remote volumes.
	g.Go("background-manager", func(c context.Context) error {
		grpcQuit := <-grpcQuitCh
		defer grpcQuit()
		mgr, _ := s.sharedState.GetTrafficManagerBlocking(c)
		if mgr == nil {
			return nil
		}
		return mgr.Run(c)
	})

	// background-systema runs a localhost HTTP server for handling callbacks from the
	// Ambassador Cloud login flow.
	g.Go("background-systema", loginExecutor.Worker)

	// background-metriton is the goroutine that handles all telemetry reports, so that calls to
	// metriton don't block the functional goroutines.
	g.Go("background-metriton", s.scout.Run)

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
