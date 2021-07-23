package connector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/sharedstate"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
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

type parsedConnectRequest struct {
	*rpc.ConnectRequest
	*userd_k8s.Config
}

type ScoutReport = scout.ScoutReport

// service represents the state of the Telepresence Connector
type service struct {
	env         client.Env
	scoutClient *client.Scout // don't use this directly; use the 'scout' chan instead

	managerProxy userd_grpc.MgrProxy
	cancel       func()

	// Must hold connectMu to use the sharedState.MaybeSetXXX methods.
	connectMu   sync.Mutex
	sharedState *sharedstate.State

	// These are used to communicate between the various goroutines.
	scout           chan ScoutReport          // any-of-scoutUsers -> background-metriton
	connectRequest  chan parsedConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo     // connectWorker -> server-grpc.connect()
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

// connect the connector to a cluster
func (s *service) connect(c context.Context, cr *rpc.ConnectRequest, dryRun bool) *rpc.ConnectInfo {
	s.connectMu.Lock()
	defer s.connectMu.Unlock()

	Config, err := userd_k8s.NewConfig(cr.KubeFlags, s.env)
	if err != nil && !dryRun {
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText: err.Error(),
		}
	}
	if cluster := s.sharedState.GetClusterNonBlocking(); cluster != nil {
		if cluster.Config.Equals(Config) {
			if mns := cr.MappedNamespaces; len(mns) > 0 {
				if len(mns) == 1 && mns[0] == "all" {
					mns = nil
				}
				sort.Strings(mns)
				cluster.SetMappedNamespaces(c, mns)
			}
			ingressInfo, err := cluster.DetectIngressBehavior(c)
			if err != nil {
				return &rpc.ConnectInfo{
					Error:     rpc.ConnectInfo_CLUSTER_FAILED,
					ErrorText: err.Error(),
				}
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
				Config:         Config,
			}
			close(s.connectRequest)
			return <-s.connectResponse
		}
	}
}

func (s *service) connectWorker(c context.Context, cr *rpc.ConnectRequest, k8sConfig *userd_k8s.Config) *rpc.ConnectInfo {
	mappedNamespaces := cr.MappedNamespaces
	if len(mappedNamespaces) == 1 && mappedNamespaces[0] == "all" {
		mappedNamespaces = nil
	}
	sort.Strings(mappedNamespaces)

	s.scout <- ScoutReport{
		Action: "connect",
	}

	// establish a connection to the daemon gRPC service
	dlog.Info(c, "Connecting to daemon...")
	conn, err := client.DialSocket(c, client.DaemonSocketName)
	if err != nil {
		dlog.Errorf(c, "unable to connect to daemon: %+v", err)
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_DAEMON_FAILED,
			ErrorText: err.Error(),
		}
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
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText: err.Error(),
		}
	}
	s.sharedState.MaybeSetCluster(cluster)
	dlog.Infof(c, "Connected to context %s (%s)", cluster.Context, cluster.Server)

	// Phone home with the information about the size of the cluster
	s.scout <- func() ScoutReport {
		report := ScoutReport{
			Action: "connecting_traffic_manager",
			PersistentMetadata: map[string]interface{}{
				"cluster_id":        cluster.GetClusterId(c),
				"mapped_namespaces": len(cr.MappedNamespaces),
			},
		}
		return report
	}()

	connectStart := time.Now()

	dlog.Info(c, "Connecting to traffic manager...")
	tmgr, err := userd_trafficmgr.New(c,
		s.env,
		cluster,
		s.scoutClient.Reporter.InstallID(),
		userd_trafficmgr.Callbacks{
			GetCloudAPIKey:  s.sharedState.GetCloudAPIKey,
			SetClient:       s.managerProxy.SetClient,
			SetOutboundInfo: daemonClient.SetOutboundInfo,
		})
	if err != nil {
		dlog.Errorf(c, "Unable to connect to TrafficManager: %s", err)
		// No point in continuing without a traffic manager
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED,
			ErrorText: err.Error(),
		}
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
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED,
			ErrorText: err.Error(),
		}
	}

	// Wait until all of the k8s watches (in the "background-k8swatch" goroutine) are running.
	if err = cluster.WaitUntilReady(c); err != nil {
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText: err.Error(),
		}
	}

	// Collect data on how long connection time took
	s.scout <- ScoutReport{
		Action: "finished_connecting_traffic_manager",
		Metadata: map[string]interface{}{
			"connect_duration": time.Since(connectStart).Seconds(),
		},
	}

	ingressInfo, err := cluster.DetectIngressBehavior(c)
	if err != nil {
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText: err.Error(),
		}
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
	c, err := logging.InitContext(c, processName)
	if err != nil {
		return err
	}
	c = dgroup.WithGoroutineName(c, "/"+processName)

	env, err := client.LoadEnv(c)
	if err != nil {
		return err
	}

	s := &service{
		env:         env,
		scoutClient: client.NewScout(c, "connector"),

		sharedState: sharedstate.NewState(),

		scout:           make(chan ScoutReport, 10),
		connectRequest:  make(chan parsedConnectRequest),
		connectResponse: make(chan *rpc.ConnectInfo),
	}
	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})
	s.cancel = func() { g.Go("quit", func(_ context.Context) error { return nil }) }
	s.sharedState.LoginExecutor = userd_auth.NewStandardLoginExecutor(env, &s.sharedState.UserNotifications, s.scout)
	var scoutUsers sync.WaitGroup
	scoutUsers.Add(1) // how many of the goroutines might write to s.scout
	go func() {
		scoutUsers.Wait()
		close(s.scout)
	}()

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", titleName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	var grpcListener net.Listener
	defer func() {
		if grpcListener != nil {
			_ = os.Remove(grpcListener.Addr().String())
		}
	}()

	g.Go("server-grpc", func(c context.Context) (err error) {
		defer func() {
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

		// Listen on unix domain socket
		grpcListener, err = net.Listen("unix", client.ConnectorSocketName)
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				return fmt.Errorf("socket %q exists so the %s is either already running or terminated ungracefully",
					client.SocketURL(client.ConnectorSocketName), processName)
			}
			return err
		}
		// Don't have dhttp.ServerConfig.Serve unlink the socket; defer unlinking the socket
		// until the process exits.
		grpcListener.(*net.UnixListener).SetUnlinkOnClose(false)

		dlog.Info(c, "gRPC server started")
		defer func() {
			if err != nil {
				dlog.Errorf(c, "gRPC server ended with: %v", err)
			} else {
				dlog.Debug(c, "gRPC server ended")
			}
		}()

		svc := grpc.NewServer()
		rpc.RegisterConnectorServer(svc, userd_grpc.NewGRPCService(
			userd_grpc.Callbacks{
				InterceptStatus: s.interceptStatus,
				Cancel:          s.cancel,
				Connect:         s.connect,
			},
			s.sharedState,
		))
		manager.RegisterManagerServer(svc, &s.managerProxy)

		sc := &dhttp.ServerConfig{
			Handler: svc,
		}
		return sc.Serve(c, grpcListener)
	})

	// background-init handles the work done by the initial connector.Connect RPC call.  This
	// happens in a separage goroutine from the gRPC server's connection handler so that the
	// request getting cancelled doesn't cancel the work.
	g.Go("background-init", func(c context.Context) error {
		defer func() {
			close(s.connectResponse) // -> server-grpc.connect()
			s.sharedState.MaybeSetCluster(nil)
			s.sharedState.MaybeSetTrafficManager(nil)
			<-c.Done() // Don't trip ShutdownOnNonError in the parent group.
			scoutUsers.Done()
		}()

		pcr, ok := <-s.connectRequest
		if !ok {
			return nil
		}
		s.connectResponse <- s.connectWorker(c, pcr.ConnectRequest, pcr.Config)

		return nil
	})

	// background-k8swatch watches all of the nescessary Kubernetes resources.
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
	//    + (4) mount the appropriate remote valumes.
	g.Go("background-manager", func(c context.Context) error {
		mgr, _ := s.sharedState.GetTrafficManagerBlocking(c)
		if mgr == nil {
			return nil
		}
		return mgr.Run(c)
	})

	// background-systema runs a localhost HTTP server for handling callbacks from the
	// Ambassador Cloud login flow.
	g.Go("background-systema", s.sharedState.LoginExecutor.Worker)

	// background-metriton is the goroutine that handles all telemetry reports, so that calls to
	// metriton don't block the functional goroutines.
	g.Go("background-metriton", func(c context.Context) error {
		for report := range s.scout {
			for k, v := range report.PersistentMetadata {
				s.scoutClient.SetMetadatum(k, v)
			}

			var metadata []client.ScoutMeta
			for k, v := range report.Metadata {
				metadata = append(metadata, client.ScoutMeta{
					Key:   k,
					Value: v,
				})
			}
			if err := s.scoutClient.Report(c, report.Action, metadata...); err != nil {
				// error is logged and is not fatal
				dlog.Errorf(c, "report failed: %+v", err)
			}
		}
		return nil
	})

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
