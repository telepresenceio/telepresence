package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	stacktrace "github.com/pkg/errors"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc2 "github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/install/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

// A SessionService represents a service that should be started together with each daemon session.
// Can be used when passing in custom commands to start up any resources needed for the commands.
type SessionService interface {
	Name() string
	// Run should run the Session service. Run will be launched in its own goroutine and it's expected that it blocks until the context is finished.
	Run(ctx context.Context, scout *scout.Reporter, session Session) error
}

type WatchWorkloadsStream interface {
	Send(*rpc.WorkloadInfoSnapshot) error
}

type Session interface {
	restapi.AgentState
	k8s.KubeConfig
	AddIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.InterceptResult, error)
	CanIntercept(context.Context, *rpc.CreateInterceptRequest) (*serviceProps, *rpc.InterceptResult)
	AddInterceptor(string, int) error
	RemoveInterceptor(string) error
	GetInterceptSpec(string) *manager.InterceptSpec
	InterceptsForWorkload(string, string) []*manager.InterceptSpec
	Status(context.Context) *rpc.ConnectInfo
	IngressInfos(c context.Context) ([]*manager.IngressInfo, error)
	ClearIntercepts(context.Context) error
	RemoveIntercept(context.Context, string) error
	Run(context.Context) error
	Uninstall(context.Context, *rpc.UninstallRequest) (*rpc.Result, error)
	UpdateStatus(context.Context, *rpc.ConnectRequest) *rpc.ConnectInfo
	WatchWorkloads(context.Context, *rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error
	WithK8sInterface(context.Context) context.Context
	WorkloadInfoSnapshot(context.Context, []string, rpc.ListRequest_Filter, bool) (*rpc.WorkloadInfoSnapshot, error)
	ManagerClient() manager.ManagerClient
	ManagerConn() *grpc.ClientConn
	GetCurrentNamespaces(forClientAccess bool) []string
	ActualNamespace(string) string
	RemainWithToken(context.Context) error
	AddNamespaceListener(k8s.NamespaceListener)
	GatherLogs(context.Context, *connector.LogsRequest) (*connector.LogsResponse, error)
	ForeachAgentPod(ctx context.Context, fn func(context.Context, typed.PodInterface, *core.Pod), filter func(*core.Pod) bool) error
}

type Service interface {
	RootDaemonClient(context.Context) (daemon.DaemonClient, error)
	SetManagerClient(manager.ManagerClient, ...grpc.CallOption)
	LoginExecutor() auth.LoginExecutor
}

type apiServer struct {
	restapi.Server
	cancel context.CancelFunc
}

type apiMatcher struct {
	requestMatcher matcher.Request
	metadata       map[string]string
}

type TrafficManager struct {
	*k8s.Cluster

	// local information
	installID   string // telepresence's install ID
	userAndHost string // "laptop-username@laptop-hostname"

	getCloudAPIKey func(context.Context, string, bool) (string, error)

	// manager client
	managerClient manager.ManagerClient

	// manager client connection
	managerConn *grpc.ClientConn

	// version reported by the manager
	managerVersion semver.Version

	// search paths are propagated to the rootDaemon
	rootDaemon daemon.DaemonClient

	sessionInfo *manager.SessionInfo // sessionInfo returned by the traffic-manager

	wlWatcher *workloadsAndServicesWatcher

	// currentInterceptsLock ensures that all accesses to currentIntercepts, currentMatchers,
	// currentAPIServers, interceptWaiters, localIntercepts, interceptedNamespace, and ingressInfo are synchronized
	//
	currentInterceptsLock sync.Mutex

	// currentIntercepts is the latest snapshot returned by the intercept watcher. It
	// is keyeed by the intercept ID
	currentIntercepts map[string]*intercept

	// currentMatches hold the matchers used when using the APIServer.
	currentMatchers map[string]*apiMatcher

	// currentAPIServers contains the APIServer in use. Typically zero or only one, but since the
	// port is determined by the intercept, there might theoretically be serveral.
	currentAPIServers map[int]*apiServer

	// Map of desired awaited intercepts. Keyed by intercept name, because it
	// is filled in prior to the intercept being created. Entries are short lived. They
	// are deleted as soon as the intercept arrives and gets stored in currentIntercepts
	interceptWaiters map[string]*awaitIntercept

	// Names of local intercepts
	localIntercepts map[string]struct{}

	// Name of currently intercepted namespace
	interceptedNamespace string

	ingressInfo []*manager.IngressInfo

	// currentAgents is the latest snapshot returned by the agent watcher
	currentAgents     []*manager.AgentInfo
	currentAgentsLock sync.Mutex

	// agentWaiters contains chan *manager.AgentInfo keyed by agent <name>.<namespace>
	agentWaiters sync.Map

	// agentInitWaiters  is protected by the currentAgentsLock. The contained channels are closed
	// and the slice is cleared when an agent snapshot arrives.
	agentInitWaiters []chan<- struct{}

	sessionServices []SessionService
	sr              *scout.Reporter

	isPodDaemon bool

	fuseFtp rpc2.FuseFTPClient
}

// firstAgentConfigMapVersion first version of traffic-manager that uses the agent ConfigMap
// TODO: Change to released version
var firstAgentConfigMapVersion = semver.MustParse("2.6.0")

func NewSession(
	ctx context.Context,
	sr *scout.Reporter,
	cr *rpc.ConnectRequest,
	svc Service,
	extraServices []SessionService,
	fuseFtp rpc2.FuseFTPClient,
) (context.Context, Session, *connector.ConnectInfo) {
	dlog.Info(ctx, "-- Starting new session")
	sr.Report(ctx, "connect")

	var rootDaemon daemon.DaemonClient
	if !cr.IsPodDaemon {
		var err error
		rootDaemon, err = svc.RootDaemonClient(ctx)
		if err != nil {
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
	}

	dlog.Info(ctx, "Connecting to k8s cluster...")
	cluster, err := connectCluster(ctx, cr)
	if err != nil {
		dlog.Errorf(ctx, "unable to track k8s cluster: %+v", err)
		return ctx, nil, connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	dlog.Infof(ctx, "Connected to context %s (%s)", cluster.Context, cluster.Server)

	// Phone home with the information about the size of the cluster
	ctx = cluster.WithK8sInterface(ctx)
	sr.SetMetadatum(ctx, "cluster_id", cluster.GetClusterId(ctx))
	if !cr.IsPodDaemon {
		sr.Report(ctx, "connecting_traffic_manager", scout.Entry{
			Key:   "mapped_namespaces",
			Value: len(cr.MappedNamespaces),
		})
	}

	connectStart := time.Now()

	dlog.Info(ctx, "Connecting to traffic manager...")
	tmgr, err := connectMgr(ctx, sr, cluster, sr.InstallID(), svc, rootDaemon, cr.IsPodDaemon, extraServices, fuseFtp)
	if err != nil {
		dlog.Errorf(ctx, "Unable to connect to TrafficManager: %s", err)
		return ctx, nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}

	// Must call SetManagerClient before calling daemon.Connect which tells the
	// daemon to use the proxy.
	var opts []grpc.CallOption
	cfg := client.GetConfig(ctx)
	if !cfg.Grpc.MaxReceiveSize.IsZero() {
		if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
			opts = append(opts, grpc.MaxCallRecvMsgSize(int(mz)))
		}
	}
	svc.SetManagerClient(tmgr.managerClient, opts...)

	// Tell daemon what it needs to know in order to establish outbound traffic to the cluster
	if !cr.IsPodDaemon {
		oi := tmgr.getOutboundInfo(ctx)

		dlog.Debug(ctx, "Connecting to root daemon")
		var rootStatus *daemon.DaemonStatus
		for attempt := 1; ; attempt++ {
			if rootStatus, err = rootDaemon.Connect(ctx, oi); err != nil {
				dlog.Errorf(ctx, "failed to connect to root daemon: %v", err)
				return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
			}
			oc := rootStatus.OutboundConfig
			if oc == nil || oc.Session == nil {
				// This is an internal error. Something is wrong with the root daemon.
				return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, errors.New("root daemon's OutboundConfig has no Session"))
			}
			if oc.Session.SessionId == oi.Session.SessionId {
				break
			}

			// Root daemon was running an old session. This indicates that this daemon somehow
			// crashed without disconnecting. So let's do that now, and then reconnect...
			if attempt == 2 {
				// ...or not, since we've already done it.
				return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, errors.New("unable to reconnect"))
			}
			if _, err = rootDaemon.Disconnect(ctx, &empty.Empty{}); err != nil {
				return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, fmt.Errorf("failed to disconnect from the root daemon: %w", err))
			}
		}
		dlog.Debug(ctx, "Connected to root daemon")
		tmgr.AddNamespaceListener(tmgr.updateDaemonNamespaces)

		tmgr.updateDaemonNamespaces(ctx)
		if _, err = rootDaemon.WaitForNetwork(ctx, &empty.Empty{}); err != nil {
			if se, ok := status.FromError(err); ok {
				err = se.Err()
			}
			dlog.Errorf(ctx, "failed to connect to root daemon: %v", err)
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
	}

	// Collect data on how long connection time took
	dlog.Debug(ctx, "Finished connecting to traffic manager")
	sr.Report(ctx, "finished_connecting_traffic_manager", scout.Entry{
		Key: "connect_duration", Value: time.Since(connectStart).Seconds()})

	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_UNSPECIFIED,
		ClusterContext: cluster.Config.Context,
		ClusterServer:  cluster.Config.Server,
		ClusterId:      cluster.GetClusterId(ctx),
		SessionInfo:    tmgr.session(),
		Intercepts:     &manager.InterceptInfoSnapshot{Intercepts: tmgr.getCurrentInterceptInfos()},
	}
	ctx = WithSession(ctx, tmgr)
	return ctx, tmgr, ret
}

func (tm *TrafficManager) RemainWithToken(ctx context.Context) error {
	tok, err := tm.getCloudAPIKey(ctx, a8rcloud.KeyDescTrafficManager, false)
	if err != nil {
		return fmt.Errorf("failed to get api key: %w", err)
	}
	_, err = tm.managerClient.Remain(ctx, &manager.RemainRequest{
		Session: tm.session(),
		ApiKey:  tok,
	})
	if err != nil {
		return fmt.Errorf("error calling Remain: %w", err)
	}
	return nil
}

func (tm *TrafficManager) ManagerClient() manager.ManagerClient {
	return tm.managerClient
}

func (tm *TrafficManager) ManagerConn() *grpc.ClientConn {
	return tm.managerConn
}

// connectCluster returns a configured cluster instance
func connectCluster(c context.Context, cr *rpc.ConnectRequest) (*k8s.Cluster, error) {
	var config *k8s.Config
	var err error
	if cr.IsPodDaemon {
		config, err = k8s.NewInClusterConfig(c, cr.KubeFlags)
	} else {
		config, err = k8s.NewConfig(c, cr.KubeFlags)
	}

	if err != nil {
		return nil, err
	}

	mappedNamespaces := cr.MappedNamespaces
	if len(mappedNamespaces) == 1 && mappedNamespaces[0] == "all" {
		mappedNamespaces = nil
	} else {
		sort.Strings(mappedNamespaces)
	}

	cluster, err := k8s.NewCluster(c, config, mappedNamespaces)
	if err != nil {
		return nil, err
	}
	return cluster, nil
}

func DeleteManager(ctx context.Context, req *rpc.HelmRequest) error {
	cr := req.GetConnectRequest()
	if cr == nil {
		dlog.Info(ctx, "Connect_request in Helm_request was nil, using defaults")
		cr = &rpc.ConnectRequest{}
	}

	cluster, err := connectCluster(ctx, cr)
	if err != nil {
		return err
	}

	return helm.DeleteTrafficManager(ctx, cluster.ConfigFlags, cluster.GetManagerNamespace(), false)
}

func EnsureManager(ctx context.Context, req *rpc.HelmRequest) error {
	// seg guard
	cr := req.GetConnectRequest()
	if cr == nil {
		dlog.Info(ctx, "Connect_request in Helm_request was nil, using defaults")
		cr = &rpc.ConnectRequest{}
	}

	cluster, err := connectCluster(ctx, cr)
	if err != nil {
		return err
	}

	dlog.Debug(ctx, "ensuring that traffic-manager exists")
	c := cluster.WithK8sInterface(ctx)
	return helm.EnsureTrafficManager(c, cluster.ConfigFlags, cluster.GetManagerNamespace(), req)
}

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(
	ctx context.Context,
	sr *scout.Reporter,
	cluster *k8s.Cluster,
	installID string,
	svc Service,
	rootDaemon daemon.DaemonClient,
	isPodDaemon bool,
	sessionServices []SessionService,
	fuseFtp rpc2.FuseFTPClient,
) (*TrafficManager, error) {
	clientConfig := client.GetConfig(ctx)
	tos := &clientConfig.Timeouts

	ctx, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	userinfo, err := user.Current()
	if err != nil {
		return nil, stacktrace.Wrap(err, "user.Current()")
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, stacktrace.Wrap(err, "os.Hostname()")
	}

	apiKey, err := svc.LoginExecutor().GetAPIKey(ctx, a8rcloud.KeyDescTrafficManager)
	if err != nil {
		dlog.Errorf(ctx, "unable to get APIKey: %v", err)
	}

	dlog.Debug(ctx, "checking that traffic-manager exists")
	existing, _, isManagerErr := helm.IsTrafficManager(ctx, cluster.ConfigFlags, cluster.GetManagerNamespace())
	if isManagerErr == nil && existing == nil {
		return nil, errcat.User.New("traffic manager not found, if it is not installed, please run 'telepresence helm install'")
	}

	if isManagerErr != nil {
		dlog.Infof(ctx, "unable to look for existing helm release: %v. Assuming it's there and continuing...", isManagerErr)
	}

	dlog.Debug(ctx, "creating port-forward")
	grpcDialer, err := dnet.NewK8sPortForwardDialer(ctx, cluster.Config.RestConfig, k8sapi.GetK8sInterface(ctx))
	if err != nil {
		return nil, err
	}
	grpcAddr := net.JoinHostPort(
		"svc/traffic-manager."+cluster.GetManagerNamespace(),
		fmt.Sprint(install.ManagerPortHTTP))

	// First check. Establish connection
	tc, tCancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer tCancel()

	opts := []grpc.DialOption{grpc.WithContextDialer(grpcDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	}

	var conn *grpc.ClientConn
	if conn, err = grpc.DialContext(tc, grpcAddr, opts...); err != nil {
		// if traffic manager was not found in previous step, it is probably not installed
		// return `helm install` err message
		if isManagerErr != nil {
			return nil, isManagerErr
		}
		return nil, client.CheckTimeout(tc, fmt.Errorf("dial manager: %w", err))
	}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()

	userAndHost := fmt.Sprintf("%s@%s", userinfo.Username, host)
	mClient := manager.NewManagerClient(conn)

	vi, err := mClient.Version(tc, &empty.Empty{})
	if err != nil {
		return nil, client.CheckTimeout(tc, fmt.Errorf("manager.Version: %w", err))
	}
	managerVersion, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return nil, client.CheckTimeout(tc, fmt.Errorf("unable to parse manager.Version: %w", err))
	}

	clusterHost := cluster.Config.RestConfig.Host
	si, err := LoadSessionFromUserCache(ctx, clusterHost)
	if err != nil {
		return nil, err
	}

	if si != nil {
		// Check if the session is still valid in the traffic-manager by calling Remain
		_, err = mClient.Remain(ctx, &manager.RemainRequest{
			Session: si,
			ApiKey: func() string {
				// Discard any errors; including an apikey with this request
				// is optional.  We might not even be logged in.
				tok, _ := auth.GetCloudAPIKey(ctx, svc.LoginExecutor(), a8rcloud.KeyDescTrafficManager, false)
				return tok
			}(),
		})
		if err == nil {
			dlog.Debugf(ctx, "traffic-manager port-forward established, client was already known to the traffic-manager as %q", userAndHost)
		} else {
			si = nil
		}
	}

	if si == nil {
		dlog.Debugf(ctx, "traffic-manager port-forward established, making client known to the traffic-manager as %q", userAndHost)
		si, err = mClient.ArriveAsClient(tc, &manager.ClientInfo{
			Name:      userAndHost,
			InstallId: installID,
			Product:   "telepresence",
			Version:   client.Version(),
			ApiKey:    apiKey,
		})
		if err != nil {
			return nil, client.CheckTimeout(tc, fmt.Errorf("manager.ArriveAsClient: %w", err))
		}
		if err = SaveSessionToUserCache(ctx, clusterHost, si); err != nil {
			return nil, err
		}
	}

	return &TrafficManager{
		Cluster:     cluster,
		installID:   installID,
		userAndHost: userAndHost,
		getCloudAPIKey: func(ctx context.Context, desc string, autoLogin bool) (string, error) {
			return auth.GetCloudAPIKey(ctx, svc.LoginExecutor(), desc, autoLogin)
		},
		managerClient:    mClient,
		managerConn:      conn,
		managerVersion:   managerVersion,
		sessionInfo:      si,
		rootDaemon:       rootDaemon,
		localIntercepts:  make(map[string]struct{}),
		interceptWaiters: make(map[string]*awaitIntercept),
		wlWatcher:        newWASWatcher(),
		isPodDaemon:      isPodDaemon,
		sessionServices:  sessionServices,
		fuseFtp:          fuseFtp,
		sr:               sr,
	}, nil
}

func connectError(t rpc.ConnectInfo_ErrType, err error) *rpc.ConnectInfo {
	return &rpc.ConnectInfo{
		Error:         t,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

func (tm *TrafficManager) setInterceptedNamespace(c context.Context, ns string) {
	tm.currentInterceptsLock.Lock()
	diff := tm.interceptedNamespace != ns
	if diff {
		tm.interceptedNamespace = ns
	}
	tm.currentInterceptsLock.Unlock()
	if diff {
		tm.updateDaemonNamespaces(c)
	}
}

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (tm *TrafficManager) updateDaemonNamespaces(c context.Context) {
	tm.wlWatcher.setNamespacesToWatch(c, tm.GetCurrentNamespaces(true))

	var namespaces []string
	tm.currentInterceptsLock.Lock()
	if tm.interceptedNamespace != "" {
		namespaces = []string{tm.interceptedNamespace}
	}
	tm.currentInterceptsLock.Unlock()
	// Avoid being locked for the remainder of this function.

	// Pass current mapped namespaces as plain names (no ending dot). The DNS-resolver will
	// create special mapping for those, allowing names like myservice.mynamespace to be resolved
	paths := tm.GetCurrentNamespaces(false)
	dlog.Debugf(c, "posting search paths %v and namespaces %v", paths, namespaces)

	if _, err := tm.rootDaemon.SetDnsSearchPath(c, &daemon.Paths{Paths: paths, Namespaces: namespaces}); err != nil {
		dlog.Errorf(c, "error posting search paths %v and namespaces %v to root daemon: %v", paths, namespaces, err)
	}
	dlog.Debug(c, "search paths posted successfully")
}

// Run (1) starts up with ensuring that the manager is installed and running,
// but then for most of its life
//   - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//   - run the intercepts (manager.WatchIntercepts) and then
//   - (3) listen on the appropriate local ports and forward them to the intercepted
//     Services, and
//   - (4) mount the appropriate remote volumes.
func (tm *TrafficManager) Run(c context.Context) error {
	defer dlog.Info(c, "-- Session ended")

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("remain", tm.remain)
	g.Go("intercept-port-forward", tm.watchInterceptsHandler)
	g.Go("agent-watcher", tm.agentInfoWatcher)
	g.Go("dial-request-watcher", tm.dialRequestWatcher)
	for _, svc := range tm.sessionServices {
		func(svc SessionService) {
			dlog.Infof(c, "Starting additional session service %s", svc.Name())
			g.Go(svc.Name(), func(c context.Context) error {
				return svc.Run(c, tm.sr, tm)
			})
		}(svc)
	}
	return g.Wait()
}

func (tm *TrafficManager) session() *manager.SessionInfo {
	return tm.sessionInfo
}

// getInfosForWorkloads returns a list of workloads found in the given namespace that fulfils the given filter criteria.
func (tm *TrafficManager) getInfosForWorkloads(
	ctx context.Context,
	namespaces []string,
	iMap map[string][]*manager.InterceptInfo,
	aMap map[string]*manager.AgentInfo,
	filter rpc.ListRequest_Filter,
) ([]*rpc.WorkloadInfo, error) {
	wiMap := make(map[types.UID]*rpc.WorkloadInfo)
	var err error
	tm.wlWatcher.eachService(ctx, tm.GetManagerNamespace(), namespaces, func(svc *core.Service) {
		var wls []k8sapi.Workload
		if wls, err = tm.wlWatcher.findMatchingWorkloads(ctx, svc); err != nil {
			return
		}
		for _, workload := range wls {
			if _, ok := wiMap[workload.GetUID()]; ok {
				continue
			}
			name := workload.GetName()
			dlog.Debugf(ctx, "Getting info for %s %s.%s, matching service %s.%s", workload.GetKind(), name, workload.GetNamespace(), svc.Name, svc.Namespace)
			ports := []*rpc.WorkloadInfo_ServiceReference_Port{}
			for _, p := range svc.Spec.Ports {
				ports = append(ports, &rpc.WorkloadInfo_ServiceReference_Port{
					Name: p.Name,
					Port: p.Port,
				})
			}
			wlInfo := &rpc.WorkloadInfo{
				Name:                 name,
				Namespace:            workload.GetNamespace(),
				WorkloadResourceType: workload.GetKind(),
				Uid:                  string(workload.GetUID()),
				Service: &rpc.WorkloadInfo_ServiceReference{
					Name:      svc.Name,
					Namespace: svc.Namespace,
					Uid:       string(svc.UID),
					Ports:     ports,
				},
			}
			var ok bool
			if wlInfo.InterceptInfos, ok = iMap[name]; !ok && filter <= rpc.ListRequest_INTERCEPTS {
				continue
			}
			if wlInfo.AgentInfo, ok = aMap[name]; !ok && filter <= rpc.ListRequest_INSTALLED_AGENTS {
				continue
			}
			wiMap[workload.GetUID()] = wlInfo
		}
	})
	wiz := make([]*rpc.WorkloadInfo, len(wiMap))
	i := 0
	for _, wi := range wiMap {
		wiz[i] = wi
		i++
	}
	sort.Slice(wiz, func(i, j int) bool { return wiz[i].Name < wiz[j].Name })
	return wiz, nil
}

func (tm *TrafficManager) waitForSync(ctx context.Context) {
	tm.WaitForNSSync(ctx)
	tm.wlWatcher.setNamespacesToWatch(ctx, tm.GetCurrentNamespaces(true))
	tm.wlWatcher.waitForSync(ctx)
}

func (tm *TrafficManager) getActiveNamespaces(ctx context.Context) []string {
	tm.waitForSync(ctx)
	return tm.wlWatcher.getActiveNamespaces()
}

func (tm *TrafficManager) addActiveNamespaceListener(l func()) {
	tm.wlWatcher.addActiveNamespaceListener(l)
}

func (tm *TrafficManager) WatchWorkloads(c context.Context, wr *rpc.WatchWorkloadsRequest, stream WatchWorkloadsStream) error {
	tm.waitForSync(c)
	tm.ensureWatchers(c, wr.Namespaces)
	sCtx, sCancel := context.WithCancel(c)
	// We need to make sure the subscription ends when we leave this method, since this is the one consuming the snapshotAvailable channel.
	// Otherwise, the goroutine that writes to the channel will leak.
	defer sCancel()
	snapshotAvailable := tm.wlWatcher.subscribe(sCtx)
	for {
		select {
		case <-c.Done():
			return nil
		case <-snapshotAvailable:
			snapshot, err := tm.workloadInfoSnapshot(c, wr.GetNamespaces(), rpc.ListRequest_INTERCEPTABLE, false)
			if err != nil {
				return status.Errorf(codes.Unavailable, "failed to create WorkloadInfoSnapshot: %v", err)
			}
			if err := stream.Send(snapshot); err != nil {
				dlog.Errorf(c, "WatchWorkloads.Send() failed: %v", err)
				return err
			}
		}
	}
}

func (tm *TrafficManager) WorkloadInfoSnapshot(
	ctx context.Context,
	namespaces []string,
	filter rpc.ListRequest_Filter,
	includeLocalIntercepts bool,
) (*rpc.WorkloadInfoSnapshot, error) {
	tm.waitForSync(ctx)
	return tm.workloadInfoSnapshot(ctx, namespaces, filter, includeLocalIntercepts)
}

func (tm *TrafficManager) ensureWatchers(ctx context.Context,
	namespaces []string) {
	// If a watcher is started, we better wait for the next snapshot from WatchAgentsNS
	waitCh := make(chan struct{}, 1)
	tm.currentAgentsLock.Lock()
	tm.agentInitWaiters = append(tm.agentInitWaiters, waitCh)
	tm.currentAgentsLock.Unlock()
	needWait := false

	wg := sync.WaitGroup{}
	wg.Add(len(namespaces))
	for _, ns := range namespaces {
		if ns == "" {
			// Don't use tm.ActualNamespace here because the accessibility of the namespace
			// is actually determined once the watcher starts
			ns = tm.Namespace
		}
		tm.wlWatcher.ensureStarted(ctx, ns, func(started bool) {
			if started {
				needWait = true
			}
			wg.Done()
		})
	}
	wg.Wait()
	wc, cancel := client.GetConfig(ctx).Timeouts.TimeoutContext(ctx, client.TimeoutRoundtripLatency)
	defer cancel()
	if needWait {
		select {
		case <-wc.Done():
		case <-waitCh:
		}
	}
}

func (tm *TrafficManager) workloadInfoSnapshot(
	ctx context.Context,
	namespaces []string,
	filter rpc.ListRequest_Filter,
	includeLocalIntercepts bool,
) (*rpc.WorkloadInfoSnapshot, error) {
	is := tm.getCurrentIntercepts()
	tm.ensureWatchers(ctx, namespaces)

	var nss []string
	if filter == rpc.ListRequest_INTERCEPTS {
		// Special case, we don't care about namespaces in general. Instead, we use the intercepted namespaces
		tm.currentInterceptsLock.Lock()
		if tm.interceptedNamespace != "" {
			nss = []string{tm.interceptedNamespace}
		}
		tm.currentInterceptsLock.Unlock()
		if len(nss) == 0 {
			// No active intercepts
			return &rpc.WorkloadInfoSnapshot{}, nil
		}
	} else {
		nss = make([]string, 0, len(namespaces))
		for _, ns := range namespaces {
			ns = tm.ActualNamespace(ns)
			if ns != "" {
				nss = append(nss, ns)
			}
		}
	}
	if len(nss) == 0 {
		// none of the namespaces are currently mapped
		return &rpc.WorkloadInfoSnapshot{}, nil
	}

	iMap := make(map[string][]*manager.InterceptInfo, len(is))
nextIs:
	for _, i := range is {
		for _, ns := range nss {
			if i.Spec.Namespace == ns {
				iMap[i.Spec.Agent] = append(iMap[i.Spec.Agent], i.InterceptInfo)
				continue nextIs
			}
		}
	}
	aMap := make(map[string]*manager.AgentInfo)
	for _, ns := range nss {
		for k, v := range tm.getCurrentAgentsInNamespace(ns) {
			aMap[k] = v
		}
	}
	workloadInfos, err := tm.getInfosForWorkloads(ctx, nss, iMap, aMap, filter)
	if err != nil {
		return nil, err
	}

	if includeLocalIntercepts {
		tm.currentInterceptsLock.Lock()
		for localIntercept := range tm.localIntercepts {
			workloadInfos = append(workloadInfos, &rpc.WorkloadInfo{InterceptInfos: []*manager.InterceptInfo{{
				Spec:              &manager.InterceptSpec{Name: localIntercept, Namespace: tm.interceptedNamespace},
				Disposition:       manager.InterceptDispositionType_ACTIVE,
				MechanismArgsDesc: "as local-only",
			}}})
		}
		tm.currentInterceptsLock.Unlock()
	}
	return &rpc.WorkloadInfoSnapshot{Workloads: workloadInfos}, nil
}

var SessionExpiredErr = errors.New("session expired")

func (tm *TrafficManager) remain(c context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer func() {
		ticker.Stop()
		c = dcontext.WithoutCancel(c)
		c, cancel := context.WithTimeout(c, 3*time.Second)
		defer cancel()
		if _, err := tm.managerClient.Depart(c, tm.session()); err != nil {
			dlog.Errorf(c, "failed to depart from manager: %v", err)
		} else {
			// Depart succeeded so the traffic-manager has dropped the session. We should too
			if err = DeleteSessionFromUserCache(c); err != nil {
				dlog.Errorf(c, "failed to delete session from user cache: %v", err)
			}
		}
		tm.managerConn.Close()
	}()

	for {
		select {
		case <-c.Done():
			return nil
		case <-ticker.C:
			_, err := tm.managerClient.Remain(c, &manager.RemainRequest{
				Session: tm.session(),
				ApiKey: func() string {
					// Discard any errors; including an apikey with this request
					// is optional.  We might not even be logged in.
					tok, _ := tm.getCloudAPIKey(c, a8rcloud.KeyDescTrafficManager, false)
					return tok
				}(),
			})
			if err != nil && c.Err() == nil {
				dlog.Error(c, err)
				if gErr, ok := status.FromError(err); ok && gErr.Code() == codes.NotFound {
					// Session has expired. We need to cancel the owner session and reconnect
					return SessionExpiredErr
				}
			}
		}
	}
}

func (tm *TrafficManager) UpdateStatus(c context.Context, cr *rpc.ConnectRequest) *rpc.ConnectInfo {
	var config *k8s.Config
	var err error
	if cr.IsPodDaemon {
		config, err = k8s.NewInClusterConfig(c, cr.KubeFlags)
	} else {
		config, err = k8s.NewConfig(c, cr.KubeFlags)
	}
	if err != nil {
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}

	if !cr.IsPodDaemon && !tm.Config.ContextServiceAndFlagsEqual(config) {
		return &rpc.ConnectInfo{
			Error:          rpc.ConnectInfo_MUST_RESTART,
			ClusterContext: tm.Config.Context,
			ClusterServer:  tm.Config.Server,
			ClusterId:      tm.GetClusterId(c),
		}
	}

	if tm.SetMappedNamespaces(c, cr.MappedNamespaces) {
		tm.currentInterceptsLock.Lock()
		tm.ingressInfo = nil
		tm.currentInterceptsLock.Unlock()
	}
	return tm.Status(c)
}

func (tm *TrafficManager) Status(c context.Context) *rpc.ConnectInfo {
	cfg := tm.Config
	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_ALREADY_CONNECTED,
		ClusterContext: cfg.Context,
		ClusterServer:  cfg.Server,
		ClusterId:      tm.GetClusterId(c),
		SessionInfo:    tm.session(),
		Intercepts:     &manager.InterceptInfoSnapshot{Intercepts: tm.getCurrentInterceptInfos()},
	}
	return ret
}

// Given a slice of AgentInfo, this returns another slice of agents with one
// agent per namespace, name pair.
// Deprecated: not used with traffic-manager versions >= 2.6.0
func getRepresentativeAgents(_ context.Context, agents []*manager.AgentInfo) []*manager.AgentInfo {
	type workload struct {
		name, namespace string
	}
	workloads := map[workload]bool{}
	var representativeAgents []*manager.AgentInfo
	for _, agent := range agents {
		wk := workload{name: agent.Name, namespace: agent.Namespace}
		if !workloads[wk] {
			workloads[wk] = true
			representativeAgents = append(representativeAgents, agent)
		}
	}
	return representativeAgents
}

// Deprecated: not used with traffic-manager versions >= 2.6.0
func (tm *TrafficManager) legacyUninstall(c context.Context, ur *rpc.UninstallRequest) (*rpc.Result, error) {
	result := &rpc.Result{}
	agents := tm.getCurrentAgents()

	// Since workloads can have more than one replica, we get a slice of agents
	// where the agent to workload mapping is 1-to-1.  This is important
	// because in the ALL_AGENTS or default case, we could edit the same
	// workload n times for n replicas, which could cause race conditions
	agents = getRepresentativeAgents(c, agents)

	_ = tm.ClearIntercepts(c)
	switch ur.UninstallType {
	case rpc.UninstallRequest_UNSPECIFIED:
		return nil, status.Error(codes.InvalidArgument, "invalid uninstall request")
	case rpc.UninstallRequest_NAMED_AGENTS:
		var selectedAgents []*manager.AgentInfo
		for _, di := range ur.Agents {
			found := false
			namespace := tm.ActualNamespace(ur.Namespace)
			if namespace != "" {
				for _, ai := range agents {
					if namespace == ai.Namespace && di == ai.Name {
						found = true
						selectedAgents = append(selectedAgents, ai)
						break
					}
				}
			}
			if !found {
				result = errcat.ToResult(errcat.User.Newf("unable to find a workload named %s.%s with an agent installed", di, namespace))
			}
		}
		agents = selectedAgents
		fallthrough
	default:
		if len(agents) > 0 {
			if err := legacyRemoveAgents(c, agents); err != nil {
				result = errcat.ToResult(err)
			}
		}
	}
	return result, nil
}

// Uninstall parts or all of Telepresence from the cluster if the client has sufficient credentials to do so.
//
// Uninstalling everything requires that the client owns the helm chart installation and has permissions to run
// a `helm uninstall traffic-manager`.
//
// Uninstalling all or specific agents require that the client can get and update the agents ConfigMap.
func (tm *TrafficManager) Uninstall(ctx context.Context, ur *rpc.UninstallRequest) (*rpc.Result, error) {
	if tm.managerVersion.LT(firstAgentConfigMapVersion) {
		// fall back traffic-manager behaviour prior to 2.6
		return tm.legacyUninstall(ctx, ur)
	}

	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	loadAgentConfigMap := func(ns string) (*core.ConfigMap, error) {
		cm, err := api.ConfigMaps(ns).Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// there are no agents to remove
				return nil, nil
			}
			// TODO: find out if this is due to lack of access credentials and if so, report using errcat.User with more meaningful message
			return nil, err
		}
		return cm, nil
	}

	updateAgentConfigMap := func(ns string, cm *core.ConfigMap) error {
		_, err := api.ConfigMaps(ns).Update(ctx, cm, meta.UpdateOptions{})
		return err
	}

	// Removal of agents requested. We need the agents ConfigMap in order to do that.
	// This removal is deliberately done in the client instead of the traffic-manager so that RBAC can be configured
	// to prevent the clients from doing it.
	if ur.UninstallType == rpc.UninstallRequest_NAMED_AGENTS {
		// must have a valid namespace in order to uninstall named agents
		tm.waitForSync(ctx)
		if ur.Namespace == "" {
			ur.Namespace = tm.Namespace
		}
		tm.wlWatcher.ensureStarted(ctx, ur.Namespace, nil)
		namespace := tm.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return errcat.ToResult(errcat.User.Newf("namespace %s is not mapped", ur.Namespace)), nil
		}
		cm, err := loadAgentConfigMap(namespace)
		if err != nil || cm == nil {
			return errcat.ToResult(err), nil
		}
		changed := false
		ics := tm.getCurrentIntercepts()
		for _, an := range ur.Agents {
			for _, ic := range ics {
				if ic.Spec.Namespace == namespace && ic.Spec.Agent == an {
					_ = tm.removeIntercept(ctx, ic)
					break
				}
			}
			if _, ok := cm.Data[an]; ok {
				delete(cm.Data, an)
				changed = true
			}
		}
		if changed {
			return errcat.ToResult(updateAgentConfigMap(namespace, cm)), nil
		}
		return errcat.ToResult(nil), nil
	}
	if ur.UninstallType != rpc.UninstallRequest_ALL_AGENTS {
		return nil, status.Error(codes.InvalidArgument, "invalid uninstall request")
	}

	_ = tm.ClearIntercepts(ctx)
	clearAgentsConfigMap := func(ns string) error {
		cm, err := loadAgentConfigMap(ns)
		if err != nil {
			return err
		}
		if cm == nil {
			return nil
		}
		if len(cm.Data) > 0 {
			cm.Data = nil
			return updateAgentConfigMap(ns, cm)
		}
		return nil
	}

	if ur.Namespace != "" {
		tm.waitForSync(ctx)
		if ur.Namespace == "" {
			ur.Namespace = tm.Namespace
		}
		tm.wlWatcher.ensureStarted(ctx, ur.Namespace, nil)
		namespace := tm.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return errcat.ToResult(errcat.User.Newf("namespace %s is not mapped", ur.Namespace)), nil
		}
		return errcat.ToResult(clearAgentsConfigMap(namespace)), nil
	} else {
		// Load all effected configmaps
		for _, ns := range tm.GetCurrentNamespaces(true) {
			err := clearAgentsConfigMap(ns)
			if err != nil {
				return errcat.ToResult(err), nil
			}
		}
	}
	return errcat.ToResult(nil), nil
}

// getClusterCIDRs finds the service CIDR and the pod CIDRs of all nodes in the cluster
func (tm *TrafficManager) getOutboundInfo(ctx context.Context) *daemon.OutboundInfo {
	// We'll figure out the IP address of the API server(s) so that we can tell the daemon never to proxy them.
	// This is because in some setups the API server will be in the same CIDR range as the pods, and the
	// daemon will attempt to proxy traffic to it. This usually results in a loss of all traffic to/from
	// the cluster, since an open tunnel to the traffic-manager (via the API server) is itself required
	// to communicate with the cluster.
	neverProxy := []*manager.IPNet{}
	url, err := url.Parse(tm.Server)
	if err != nil {
		// This really shouldn't happen as we are connected to the server
		dlog.Errorf(ctx, "Unable to parse url for k8s server %s: %v", tm.Server, err)
	} else {
		hostname := url.Hostname()
		rawIP := iputil.Parse(hostname)
		ips := []net.IP{rawIP}
		if rawIP == nil {
			var err error
			ips, err = net.LookupIP(hostname)
			if err != nil {
				dlog.Errorf(ctx, "Unable to do DNS lookup for k8s server %s: %v", hostname, err)
				ips = []net.IP{}
			}
		}
		for _, ip := range ips {
			mask := net.CIDRMask(128, 128)
			if ipv4 := ip.To4(); ipv4 != nil {
				mask = net.CIDRMask(32, 32)
				ip = ipv4
			}
			ipnet := &net.IPNet{IP: ip, Mask: mask}
			neverProxy = append(neverProxy, iputil.IPNetToRPC(ipnet))
		}
	}
	for _, np := range tm.NeverProxy {
		neverProxy = append(neverProxy, iputil.IPNetToRPC((*net.IPNet)(np)))
	}
	info := &daemon.OutboundInfo{
		Session:           tm.sessionInfo,
		NeverProxySubnets: neverProxy,
	}

	if tm.DNS != nil {
		info.Dns = &daemon.DNSConfig{
			ExcludeSuffixes: tm.DNS.ExcludeSuffixes,
			IncludeSuffixes: tm.DNS.IncludeSuffixes,
			LookupTimeout:   durationpb.New(tm.DNS.LookupTimeout.Duration),
		}
		if len(tm.DNS.LocalIP) > 0 {
			info.Dns.LocalIp = tm.DNS.LocalIP.IP()
		}
		if len(tm.DNS.RemoteIP) > 0 {
			info.Dns.RemoteIp = tm.DNS.RemoteIP.IP()
		}
	}

	if len(tm.AlsoProxy) > 0 {
		info.AlsoProxySubnets = make([]*manager.IPNet, len(tm.AlsoProxy))
		for i, ap := range tm.AlsoProxy {
			info.AlsoProxySubnets[i] = iputil.IPNetToRPC((*net.IPNet)(ap))
		}
	}
	return info
}
