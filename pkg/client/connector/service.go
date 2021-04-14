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
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/broadcastqueue"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/scout"
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
	*k8sConfig
}

type ScoutReport = scout.ScoutReport

// service represents the state of the Telepresence Connector
type service struct {
	rpc.UnsafeConnectorServer

	env         client.Env
	daemon      daemon.DaemonClient
	scoutClient *client.Scout // don't use this directly; use the 'scout' chan instead

	managerProxy mgrProxy
	cancel       func()

	userNotifications broadcastqueue.BroadcastQueue

	loginExecutor auth.LoginExecutor

	connectMu sync.Mutex
	// These get set by .connect() and are protected by connectMu.
	cluster    *k8sCluster
	trafficMgr *trafficManager

	// These are used to communicate between the various goroutines.
	scout           chan ScoutReport          // any-of-scoutUsers -> background-metriton
	connectRequest  chan parsedConnectRequest // server-grpc.connect() -> connectWorker
	connectResponse chan *rpc.ConnectInfo     // connectWorker -> server-grpc.connect()
	clusterRequest  chan *k8sCluster          // connectWorker -> background-k8swatch
	managerRequest  chan *trafficManager      // connectWorker -> background-manager

	// ctx is part of the "callCtx hack", to hack around an issue in the gRPC server, since it
	// doesn't let us pass it a Context.  It should go away when we migrate to dhttp and
	// Contexts can get passed around properly for HTTP/2 requests.
	ctx context.Context
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
	perr := derror.PanicToError(r)
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
	return s.connect(c, cr, false), nil
}

func (s *service) Status(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	c = s.callCtx(c, "Status")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.connect(c, cr, true), nil
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

func (s *service) List(ctx context.Context, lr *rpc.ListRequest) (*rpc.WorkloadInfoSnapshot, error) {
	if s.trafficMgr.managerClient == nil {
		return &rpc.WorkloadInfoSnapshot{}, nil
	}
	return s.trafficMgr.workloadInfoSnapshot(ctx, lr), nil
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *rpc.UninstallResult, err error) {
	c = s.callCtx(c, "Uninstall")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.trafficMgr.uninstall(c, ur)
}

func (s *service) UserNotifications(_ *empty.Empty, stream rpc.Connector_UserNotificationsServer) error {
	ctx := s.callCtx(stream.Context(), "UserNotifications")

	for msg := range s.userNotifications.Subscribe(ctx) {
		if err := stream.Send(&rpc.Notification{Message: msg}); err != nil {
			return err
		}
	}

	return nil
}

func (s *service) Login(ctx context.Context, _ *empty.Empty) (*rpc.LoginResult, error) {
	ctx = s.callCtx(ctx, "Login")
	if _, err := s.loginExecutor.GetToken(ctx); err == nil {
		return &rpc.LoginResult{Code: rpc.LoginResult_OLD_LOGIN_REUSED}, nil
	}
	if err := s.loginExecutor.Login(ctx); err != nil {
		return nil, err
	}
	return &rpc.LoginResult{Code: rpc.LoginResult_NEW_LOGIN_SUCCEEDED}, nil
}

func (s *service) Logout(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	ctx = s.callCtx(ctx, "Logout")
	if err := s.loginExecutor.Logout(ctx); err != nil {
		if errors.Is(err, auth.ErrNotLoggedIn) {
			err = grpcStatus.Error(grpcCodes.NotFound, err.Error())
		}
		return nil, err
	}
	return &empty.Empty{}, nil
}

func (s *service) getCloudAccessToken(ctx context.Context, autoLogin bool) (string, error) {
	token, err := s.loginExecutor.GetToken(ctx)
	if autoLogin && err != nil {
		if _err := s.loginExecutor.Login(ctx); _err == nil {
			token, err = s.loginExecutor.GetToken(ctx)
		}
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *service) GetCloudAccessToken(ctx context.Context, req *rpc.TokenReq) (*rpc.TokenData, error) {
	ctx = s.callCtx(ctx, "GetCloudAccessToken")
	token, err := s.getCloudAccessToken(ctx, req.GetAutoLogin())
	if err != nil {
		return nil, err
	}
	return &rpc.TokenData{AccessToken: token}, nil
}

func (s *service) getCloudAPIKey(ctx context.Context, desc string, autoLogin bool) (string, error) {
	key, err := s.loginExecutor.GetAPIKey(ctx, desc)
	if autoLogin && err != nil {
		if _err := s.loginExecutor.Login(ctx); _err == nil {
			key, err = s.loginExecutor.GetAPIKey(ctx, desc)
		}
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func (s *service) GetCloudAPIKey(ctx context.Context, req *rpc.KeyRequest) (*rpc.KeyData, error) {
	ctx = s.callCtx(ctx, "GetCloudAPIKey")
	key, err := s.getCloudAPIKey(ctx, req.GetDescription(), req.GetAutoLogin())
	if err != nil {
		return nil, err
	}
	return &rpc.KeyData{ApiKey: key}, nil
}

func (s *service) GetCloudLicense(ctx context.Context, req *rpc.LicenseRequest) (*rpc.LicenseData, error) {
	ctx = s.callCtx(ctx, "GetCloudLicense")

	license, hostDomain, err := s.loginExecutor.GetLicense(ctx, req.GetId())
	// login is required to get the license from system a so
	// we try to login before retrying the request
	if err != nil {
		if _err := s.loginExecutor.Login(ctx); _err == nil {
			license, hostDomain, err = s.loginExecutor.GetLicense(ctx, req.GetId())
		}
	}
	if err != nil {
		return nil, err
	}
	return &rpc.LicenseData{License: license, HostDomain: hostDomain}, nil
}

func (s *service) Quit(_ context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.cancel()
	return &empty.Empty{}, nil
}

// connect the connector to a cluster
func (s *service) connect(c context.Context, cr *rpc.ConnectRequest, dryRun bool) *rpc.ConnectInfo {
	s.connectMu.Lock()
	defer s.connectMu.Unlock()

	k8sConfig, err := newK8sConfig(cr.KubeFlags)
	if err != nil && !dryRun {
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText: err.Error(),
		}
	}
	switch {
	case s.cluster != nil && s.cluster.k8sConfig.equals(k8sConfig):
		if mns := cr.MappedNamespaces; len(mns) > 0 {
			if len(mns) == 1 && mns[0] == "all" {
				mns = nil
			}
			sort.Strings(mns)
			s.cluster.setMappedNamespaces(c, mns)
		}
		ingressInfo, err := s.cluster.detectIngressBehavior(c)
		if err != nil {
			return &rpc.ConnectInfo{
				Error:     rpc.ConnectInfo_CLUSTER_FAILED,
				ErrorText: err.Error(),
			}
		}
		ret := &rpc.ConnectInfo{
			Error:          rpc.ConnectInfo_ALREADY_CONNECTED,
			ClusterContext: s.cluster.k8sConfig.Context,
			ClusterServer:  s.cluster.k8sConfig.Server,
			ClusterId:      s.cluster.getClusterId(c),
			IngressInfos:   ingressInfo,
		}
		s.trafficMgr.setStatus(c, ret)
		return ret
	case s.cluster != nil /* && !s.cluster.k8sConfig.equals(k8sConfig) */ :
		ret := &rpc.ConnectInfo{
			Error:          rpc.ConnectInfo_MUST_RESTART,
			ClusterContext: s.cluster.k8sConfig.Context,
			ClusterServer:  s.cluster.k8sConfig.Server,
			ClusterId:      s.cluster.getClusterId(c),
		}
		s.trafficMgr.setStatus(c, ret)
		return ret
	default:
		// This is the first call to Connect; we have to tell the background connect
		// goroutine to actually do the work.
		if dryRun {
			return &rpc.ConnectInfo{
				Error: rpc.ConnectInfo_DISCONNECTED,
			}
		} else {
			s.connectRequest <- parsedConnectRequest{
				ConnectRequest: cr,
				k8sConfig:      k8sConfig,
			}
			close(s.connectRequest)
			return <-s.connectResponse
		}
	}
}

func (s *service) connectWorker(c context.Context, cr *rpc.ConnectRequest, k8sConfig *k8sConfig) *rpc.ConnectInfo {
	mappedNamespaces := cr.MappedNamespaces
	if len(mappedNamespaces) == 1 && mappedNamespaces[0] == "all" {
		mappedNamespaces = nil
	}
	sort.Strings(mappedNamespaces)

	s.scout <- ScoutReport{
		Action: "connect",
	}

	dlog.Info(c, "Connecting to k8s cluster...")
	cluster, err := func() (*k8sCluster, error) {
		c, cancel := context.WithTimeout(c, client.GetConfig(c).Timeouts.ClusterConnect)
		defer cancel()
		cluster, err := newKCluster(c, k8sConfig, mappedNamespaces, s.daemon)
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
	s.cluster = cluster
	dlog.Infof(c, "Connected to context %s (%s)", s.cluster.Context, s.cluster.Server)

	// Phone home with the information about the size of the cluster
	s.scout <- func() ScoutReport {
		report := ScoutReport{
			Action: "connecting_traffic_manager",
			PersistentMetadata: map[string]interface{}{
				"cluster_id":        s.cluster.getClusterId(c),
				"mapped_namespaces": len(cr.MappedNamespaces),
			},
		}
		return report
	}()

	connectStart := time.Now()

	// Check the `kubectl` version; no point continuing if it's too old.
	if err := checkKubectl(c); err != nil {
		dlog.Errorln(c, err)
		// No point in continuing without a bridge
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText: err.Error(),
		}
	}

	dlog.Info(c, "Connecting to traffic manager...")
	tmgr, err := newTrafficManager(c, s.env, s.cluster, s.scoutClient.Reporter.InstallID(), s.getCloudAPIKey)
	if err != nil {
		dlog.Errorf(c, "Unable to connect to TrafficManager: %s", err)
		// No point in continuing without a traffic manager
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED,
			ErrorText: err.Error(),
		}
	}
	s.managerRequest <- tmgr
	s.trafficMgr = tmgr

	// Wait for traffic manager to connect
	dlog.Info(c, "Waiting for TrafficManager to connect")
	tc, cancel := context.WithTimeout(c, client.GetConfig(c).Timeouts.TrafficManagerConnect)
	defer cancel()
	if err := tmgr.waitUntilStarted(tc); err != nil {
		dlog.Errorf(c, "Failed to start traffic-manager: %v", err)
		// No point in continuing without a traffic manager
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED,
			ErrorText: err.Error(),
		}
	}
	s.managerProxy.SetClient(tmgr.managerClient)

	// Start k8s-watches
	s.clusterRequest <- cluster
	if err = cluster.waitUntilReady(c); err != nil {
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

	ingressInfo, err := s.cluster.detectIngressBehavior(c)
	if err != nil {
		s.cancel()
		return &rpc.ConnectInfo{
			Error:     rpc.ConnectInfo_CLUSTER_FAILED,
			ErrorText: err.Error(),
		}
	}

	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_UNSPECIFIED,
		ClusterContext: s.cluster.k8sConfig.Context,
		ClusterServer:  s.cluster.k8sConfig.Server,
		ClusterId:      s.cluster.getClusterId(c),
		IngressInfos:   ingressInfo,
	}
	s.trafficMgr.setStatus(c, ret)
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

	// establish a connection to the daemon gRPC service
	conn, err := client.DialSocket(c, client.DaemonSocketName)
	if err != nil {
		return err
	}
	defer conn.Close()

	s := &service{
		env:         env,
		daemon:      daemon.NewDaemonClient(conn),
		scoutClient: client.NewScout(c, "connector"),

		scout:           make(chan ScoutReport, 10),
		connectRequest:  make(chan parsedConnectRequest),
		connectResponse: make(chan *rpc.ConnectInfo),
		clusterRequest:  make(chan *k8sCluster),
		managerRequest:  make(chan *trafficManager),
	}
	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})
	s.cancel = func() { g.Go("quit", func(_ context.Context) error { return nil }) }
	s.loginExecutor = auth.NewStandardLoginExecutor(env, &s.userNotifications, s.scout)
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

	g.Go("server-grpc", func(c context.Context) error {
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
		if client.SocketExists(client.ConnectorSocketName) {
			return fmt.Errorf("socket %s exists so %s already started or terminated ungracefully",
				client.SocketURL(client.ConnectorSocketName), processName)
		}
		defer func() {
			_ = os.Remove(client.ConnectorSocketName)
		}()
		listener, err := net.Listen("unix", client.ConnectorSocketName)
		if err != nil {
			return err
		}

		defer func() {
			if perr := derror.PanicToError(recover()); perr != nil {
				dlog.Error(c, perr)
			}
			_ = listener.Close()
			if err != nil {
				dlog.Errorf(c, "Server ended with: %v", err)
			} else {
				dlog.Debug(c, "Server ended")
			}
		}()

		svc := grpc.NewServer()
		rpc.RegisterConnectorServer(svc, s)
		manager.RegisterManagerServer(svc, &s.managerProxy)

		subg := dgroup.NewGroup(c, dgroup.GroupConfig{})

		subg.Go("GracefulStop", func(c context.Context) error {
			<-c.Done()
			dlog.Info(c, "Shutting down")
			svc.GracefulStop()
			return nil
		})
		subg.Go("Serve", func(c context.Context) error {
			s.ctx = c
			err = svc.Serve(listener)
			dlog.Info(c, "Done serving")
			return err
		})

		return subg.Wait()
	})

	g.Go("background-init", func(c context.Context) error {
		defer func() {
			close(s.connectResponse) // -> server-grpc.connect()
			close(s.clusterRequest)  // -> background-k8swatch
			close(s.managerRequest)  // -> background-manager
			<-c.Done()               // Don't trip ShutdownOnNonError in the parent group.
			scoutUsers.Done()
		}()

		pcr, ok := <-s.connectRequest
		if !ok {
			return nil
		}
		s.connectResponse <- s.connectWorker(c, pcr.ConnectRequest, pcr.k8sConfig)

		return nil
	})

	g.Go("background-k8swatch", func(c context.Context) error {
		cluster, ok := <-s.clusterRequest
		if !ok {
			return nil
		}
		return cluster.runWatchers(c)
	})

	g.Go("background-manager", func(c context.Context) error {
		tm, ok := <-s.managerRequest
		if !ok {
			return nil
		}
		return tm.run(c)
	})

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

	g.Go("background-systema", s.loginExecutor.Worker)

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
