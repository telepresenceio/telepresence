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

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
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
	AddIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.InterceptResult, error)
	CanIntercept(context.Context, *rpc.CreateInterceptRequest) (*serviceProps, *rpc.InterceptResult)
	AddInterceptor(string, int) error
	RemoveInterceptor(string) error
	GetInterceptSpec(string) *manager.InterceptSpec
	Status(context.Context) *rpc.ConnectInfo
	IngressInfos(c context.Context) ([]*manager.IngressInfo, error)
	ClearIntercepts(context.Context) error
	RemoveIntercept(context.Context, string) error
	Run(context.Context) error
	Uninstall(context.Context, *rpc.UninstallRequest) (*rpc.UninstallResult, error)
	UpdateStatus(context.Context, *rpc.ConnectRequest) *rpc.ConnectInfo
	WatchWorkloads(context.Context, *rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error
	WithK8sInterface(context.Context) context.Context
	WorkloadInfoSnapshot(context.Context, []string, rpc.ListRequest_Filter, bool) (*rpc.WorkloadInfoSnapshot, error)
	ManagerClient() manager.ManagerClient
	GetCurrentNamespaces(forClientAccess bool) []string
	ActualNamespace(string) string
	RemainWithToken(context.Context) error
	AddNamespaceListener(k8s.NamespaceListener)
	GatherLogs(context.Context, *connector.LogsRequest) (*connector.LogsResponse, error)
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
	*installer // installer is also a k8sCluster

	// local information
	installID   string // telepresence's install ID
	userAndHost string // "laptop-username@laptop-hostname"

	getCloudAPIKey func(context.Context, string, bool) (string, error)

	ingressInfo []*manager.IngressInfo

	// manager client
	managerClient manager.ManagerClient

	// manager client connection
	managerConn *grpc.ClientConn

	// version reported by the manager
	managerVersion semver.Version

	// search paths are propagated to the rootDaemon
	rootDaemon daemon.DaemonClient

	sessionInfo *manager.SessionInfo // sessionInfo returned by the traffic-manager

	// Map of desired mount points for intercepts
	mountPoints sync.Map

	// Map of mutexes, so that we don't create and delete
	// mount points concurrently
	mountMutexes sync.Map

	wlWatcher *workloadsAndServicesWatcher

	insLock sync.Mutex

	// Currently intercepted namespaces by remote intercepts
	interceptedNamespaces map[string]struct{}

	// Currently intercepted namespaces by local intercepts
	localInterceptedNamespaces map[string]struct{}

	localIntercepts map[string]string

	// currentIntercepts is the latest snapshot returned by the intercept watcher
	currentIntercepts     []*manager.InterceptInfo
	currentInterceptsLock sync.Mutex
	currentMatchers       map[string]*apiMatcher
	currentAPIServers     map[int]*apiServer

	// Pid of interceptor owned by an intercept. This entry will only be present when
	// the telepresence intercept command spawns a new command. The int value reflects
	// the pid of that new command.
	currentInterceptors map[string]int

	// currentAgents is the latest snapshot returned by the agent watcher
	currentAgents     []*manager.AgentInfo
	currentAgentsLock sync.Mutex

	// activeInterceptsWaiters contains chan interceptResult keyed by intercept name
	activeInterceptsWaiters sync.Map

	// agentWaiters contains chan *manager.AgentInfo keyed by agent <name>.<namespace>
	agentWaiters sync.Map

	// agentInitWaiters  is protected by the currentAgentsLock. The contained channels are closed
	// and the slice is cleared when an agent snapshot arrives.
	agentInitWaiters []chan<- struct{}

	sessionServices []SessionService
	sr              *scout.Reporter
}

// interceptResult is what gets written to the activeInterceptsWaiters channels
type interceptResult struct {
	intercept *manager.InterceptInfo
	err       error
}

// firstAgentConfigMapVersion first version of traffic-manager that uses the agent ConfigMap
// TODO: Change to released version
var firstAgentConfigMapVersion = semver.MustParse("2.6.0-alpha.64")

func NewSession(c context.Context, sr *scout.Reporter, cr *rpc.ConnectRequest, svc Service, extraServices []SessionService) (Session, *connector.ConnectInfo) {
	sr.Report(c, "connect")

	rootDaemon, err := svc.RootDaemonClient(c)
	if err != nil {
		return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
	}

	dlog.Info(c, "Connecting to k8s cluster...")
	cluster, err := connectCluster(c, cr)
	if err != nil {
		dlog.Errorf(c, "unable to track k8s cluster: %+v", err)
		return nil, connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	dlog.Infof(c, "Connected to context %s (%s)", cluster.Context, cluster.Server)

	// Phone home with the information about the size of the cluster
	c = cluster.WithK8sInterface(c)
	sr.SetMetadatum(c, "cluster_id", cluster.GetClusterId(c))
	sr.Report(c, "connecting_traffic_manager", scout.Entry{
		Key:   "mapped_namespaces",
		Value: len(cr.MappedNamespaces),
	})

	connectStart := time.Now()

	dlog.Info(c, "Connecting to traffic manager...")
	tmgr, err := connectMgr(c, cluster, sr.InstallID(), svc, rootDaemon)

	if err != nil {
		dlog.Errorf(c, "Unable to connect to TrafficManager: %s", err)
		return nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}

	tmgr.sessionServices = extraServices
	tmgr.sr = sr

	// Must call SetManagerClient before calling daemon.Connect which tells the
	// daemon to use the proxy.
	var opts []grpc.CallOption
	cfg := client.GetConfig(c)
	if !cfg.Grpc.MaxReceiveSize.IsZero() {
		if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
			opts = append(opts, grpc.MaxCallRecvMsgSize(int(mz)))
		}
	}
	svc.SetManagerClient(tmgr.managerClient, opts...)

	// Tell daemon what it needs to know in order to establish outbound traffic to the cluster
	oi := tmgr.getOutboundInfo(c)

	dlog.Debug(c, "Connecting to root daemon")
	var rootStatus *daemon.DaemonStatus
	for attempt := 1; ; attempt++ {
		if rootStatus, err = rootDaemon.Connect(c, oi); err != nil {
			dlog.Errorf(c, "failed to connect to root daemon: %v", err)
			return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
		oc := rootStatus.OutboundConfig
		if oc == nil || oc.Session == nil {
			// This is an internal error. Something is wrong with the root daemon.
			return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, errors.New("root daemon's OutboundConfig has no Session"))
		}
		if oc.Session.SessionId == oi.Session.SessionId {
			break
		}

		// Root daemon was running an old session. This indicates that this daemon somehow
		// crashed without disconnecting. So let's do that now, and then reconnect...
		if attempt == 2 {
			// ...or not, since we've already done it.
			return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, errors.New("unable to reconnect"))
		}
		if _, err = rootDaemon.Disconnect(c, &empty.Empty{}); err != nil {
			return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, fmt.Errorf("failed to disconnect from the root daemon: %w", err))
		}
	}
	dlog.Debug(c, "Connected to root daemon")
	tmgr.AddNamespaceListener(tmgr.updateDaemonNamespaces)

	// Collect data on how long connection time took
	dlog.Debug(c, "Finished connecting to traffic manager")
	sr.Report(c, "finished_connecting_traffic_manager", scout.Entry{
		Key: "connect_duration", Value: time.Since(connectStart).Seconds()})

	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_UNSPECIFIED,
		ClusterContext: cluster.Config.Context,
		ClusterServer:  cluster.Config.Server,
		ClusterId:      cluster.GetClusterId(c),
		SessionInfo:    tmgr.session(),
		Intercepts:     &manager.InterceptInfoSnapshot{Intercepts: tmgr.getCurrentIntercepts()},
	}
	return tmgr, ret
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

// connectCluster returns a configured cluster instance
func connectCluster(c context.Context, cr *rpc.ConnectRequest) (*k8s.Cluster, error) {
	config, err := k8s.NewConfig(c, cr.KubeFlags)
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

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(c context.Context, cluster *k8s.Cluster, installID string, svc Service, rootDaemon daemon.DaemonClient) (*TrafficManager, error) {
	clientConfig := client.GetConfig(c)
	tos := &clientConfig.Timeouts

	c, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerConnect)
	defer cancel()

	userinfo, err := user.Current()
	if err != nil {
		return nil, stacktrace.Wrap(err, "user.Current()")
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, stacktrace.Wrap(err, "os.Hostname()")
	}

	apiKey, err := svc.LoginExecutor().GetAPIKey(c, a8rcloud.KeyDescTrafficManager)
	if err != nil {
		dlog.Errorf(c, "unable to get APIKey: %v", err)
	}

	// Ensure that we have a traffic-manager to talk to.
	ti, err := NewTrafficManagerInstaller(cluster)
	if err != nil {
		return nil, stacktrace.Wrap(err, "new installer")
	}

	dlog.Debug(c, "ensure that traffic-manager exists")
	if err = ti.EnsureManager(c); err != nil {
		dlog.Errorf(c, "failed to ensure traffic-manager, %v", err)
		return nil, fmt.Errorf("failed to ensure traffic manager: %w", err)
	}

	dlog.Debug(c, "traffic-manager started, creating port-forward")
	restConfig, err := cluster.ConfigFlags.ToRESTConfig()
	if err != nil {
		return nil, stacktrace.Wrap(err, "ToRESTConfig")
	}
	grpcDialer, err := dnet.NewK8sPortForwardDialer(c, restConfig, k8sapi.GetK8sInterface(c))
	if err != nil {
		return nil, err
	}
	grpcAddr := net.JoinHostPort(
		"svc/traffic-manager."+cluster.GetManagerNamespace(),
		fmt.Sprint(install.ManagerPortHTTP))

	// First check. Establish connection
	tc, tCancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer tCancel()

	opts := []grpc.DialOption{grpc.WithContextDialer(grpcDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError()}

	var conn *grpc.ClientConn
	if conn, err = grpc.DialContext(tc, grpcAddr, opts...); err != nil {
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

	dlog.Debugf(c, "traffic-manager port-forward established, making client known to the traffic-manager as %q", userAndHost)
	si, err := mClient.ArriveAsClient(tc, &manager.ClientInfo{
		Name:      userAndHost,
		InstallId: installID,
		Product:   "telepresence",
		Version:   client.Version(),
		ApiKey:    apiKey,
	})
	if err != nil {
		return nil, client.CheckTimeout(tc, fmt.Errorf("manager.ArriveAsClient: %w", err))
	}

	return &TrafficManager{
		installer:           ti.(*installer),
		installID:           installID,
		userAndHost:         userAndHost,
		getCloudAPIKey:      svc.LoginExecutor().GetCloudAPIKey,
		managerClient:       mClient,
		managerConn:         conn,
		managerVersion:      managerVersion,
		sessionInfo:         si,
		rootDaemon:          rootDaemon,
		localIntercepts:     map[string]string{},
		currentInterceptors: map[string]int{},
		wlWatcher:           newWASWatcher(),
	}, nil
}

func connectError(t rpc.ConnectInfo_ErrType, err error) *rpc.ConnectInfo {
	return &rpc.ConnectInfo{
		Error:         t,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

func (tm *TrafficManager) setInterceptedNamespaces(c context.Context, interceptedNamespaces map[string]struct{}) {
	tm.insLock.Lock()
	tm.interceptedNamespaces = interceptedNamespaces
	tm.insLock.Unlock()
	tm.updateDaemonNamespaces(c)
}

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (tm *TrafficManager) updateDaemonNamespaces(c context.Context) {
	tm.wlWatcher.setNamespacesToWatch(c, tm.GetCurrentNamespaces(true))

	tm.insLock.Lock()
	namespaces := make([]string, 0, len(tm.interceptedNamespaces)+len(tm.localIntercepts))
	for ns := range tm.interceptedNamespaces {
		namespaces = append(namespaces, ns)
	}
	for ns := range tm.localInterceptedNamespaces {
		if _, found := tm.interceptedNamespaces[ns]; !found {
			namespaces = append(namespaces, ns)
		}
	}
	// Avoid being locked for the remainder of this function.
	tm.insLock.Unlock()
	sort.Strings(namespaces)

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
//  - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//  - run the intercepts (manager.WatchIntercepts) and then
//    + (3) listen on the appropriate local ports and forward them to the intercepted
//      Services, and
//    + (4) mount the appropriate remote volumes.
func (tm *TrafficManager) Run(c context.Context) error {
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("remain", tm.remain)
	g.Go("intercept-port-forward", tm.workerPortForwardIntercepts)
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
	tm.wlWatcher.eachService(ctx, namespaces, func(svc *core.Service) {
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

func (tm *TrafficManager) workloadInfoSnapshot(
	ctx context.Context,
	namespaces []string,
	filter rpc.ListRequest_Filter,
	includeLocalIntercepts bool,
) (*rpc.WorkloadInfoSnapshot, error) {
	is := tm.getCurrentIntercepts()

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

	var nss []string
	if filter == rpc.ListRequest_INTERCEPTS {
		// Special case, we don't care about namespaces. Instead, we use the namespaces of all
		// intercepts.
		nsMap := make(map[string]struct{})
		for _, i := range is {
			nsMap[i.Spec.Namespace] = struct{}{}
		}
		for _, ns := range tm.localIntercepts {
			nsMap[ns] = struct{}{}
		}
		nss = make([]string, len(nsMap))
		i := 0
		for ns := range nsMap {
			nss[i] = ns
			i++
		}
		sort.Strings(nss) // sort them so that the result is predictable
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
				iMap[i.Spec.Agent] = append(iMap[i.Spec.Agent], i)
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
	nextLocalNs:
		for localIntercept, localNs := range tm.localIntercepts {
			for _, ns := range nss {
				if localNs == ns {
					workloadInfos = append(workloadInfos, &rpc.WorkloadInfo{InterceptInfos: []*manager.InterceptInfo{{
						Spec:              &manager.InterceptSpec{Name: localIntercept, Namespace: localNs},
						Disposition:       manager.InterceptDispositionType_ACTIVE,
						MechanismArgsDesc: "as local-only",
					}}})
					continue nextLocalNs
				}
			}
		}
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
	config, err := k8s.NewConfig(c, cr.KubeFlags)
	if err != nil {
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	if !tm.Config.ContextServiceAndFlagsEqual(config) {
		return &rpc.ConnectInfo{
			Error:          rpc.ConnectInfo_MUST_RESTART,
			ClusterContext: tm.Config.Context,
			ClusterServer:  tm.Config.Server,
			ClusterId:      tm.GetClusterId(c),
		}
	}

	if tm.SetMappedNamespaces(c, cr.MappedNamespaces) {
		tm.insLock.Lock()
		tm.ingressInfo = nil
		tm.insLock.Unlock()
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
		Intercepts:     &manager.InterceptInfoSnapshot{Intercepts: tm.getCurrentIntercepts()},
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
func (tm *TrafficManager) legacyUninstall(c context.Context, ur *rpc.UninstallRequest) (*rpc.UninstallResult, error) {
	result := &rpc.UninstallResult{}
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
				result.ErrorText = fmt.Sprintf("unable to find a workload named %s.%s with an agent installed", di, namespace)
				result.ErrorCategory = int32(errcat.User)
			}
		}
		agents = selectedAgents
		fallthrough
	case rpc.UninstallRequest_ALL_AGENTS:
		if len(agents) > 0 {
			if err := tm.RemoveManagerAndAgents(c, true, agents); err != nil {
				result.ErrorText = err.Error()
				result.ErrorCategory = int32(errcat.GetCategory(err))
			}
		}
	default:
		// Cancel all communication with the manager
		if err := tm.RemoveManagerAndAgents(c, false, agents); err != nil {
			result.ErrorText = err.Error()
			result.ErrorCategory = int32(errcat.GetCategory(err))
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
func (tm *TrafficManager) Uninstall(ctx context.Context, ur *rpc.UninstallRequest) (*rpc.UninstallResult, error) {
	if tm.managerVersion.LT(firstAgentConfigMapVersion) {
		// fall back traffic-manager behaviour prior to 2.6
		return tm.legacyUninstall(ctx, ur)
	}

	result := func(err error) (*rpc.UninstallResult, error) {
		r := &rpc.UninstallResult{}
		if err != nil {
			r.ErrorText = err.Error()
			r.ErrorCategory = int32(errcat.GetCategory(err))
		}
		return r, nil
	}

	if ur.UninstallType == rpc.UninstallRequest_EVERYTHING {
		_ = tm.ClearIntercepts(ctx)
		// Uninstalling using helm chart will roll out all affected pods and remove their respective traffic-agent. This
		// of course, given that the client has permissions to do that, and the chart is owned by the client.
		if err := helm.DeleteTrafficManager(ctx, tm.ConfigFlags, tm.GetManagerNamespace(), true); err != nil {
			return result(errcat.User.New(err))
		}
		return result(nil)
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
		namespace := tm.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return result(errcat.User.Newf("namespace %s is not mapped", ur.Namespace))
		}
		cm, err := loadAgentConfigMap(namespace)
		if err != nil || cm == nil {
			return result(err)
		}
		changed := false
		ics := tm.getCurrentIntercepts()
		for _, an := range ur.Agents {
			for _, ic := range ics {
				if ic.Spec.Namespace == namespace && ic.Spec.Agent == an {
					_ = tm.RemoveIntercept(ctx, ic.Id)
					break
				}
			}
			if _, ok := cm.Data[an]; ok {
				delete(cm.Data, an)
				changed = true
			}
		}
		if changed {
			return result(updateAgentConfigMap(namespace, cm))
		}
		return result(nil)
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
		namespace := tm.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return result(errcat.User.Newf("namespace %s is not mapped", ur.Namespace))
		}
		return result(clearAgentsConfigMap(namespace))
	} else {
		// Load all effected configmaps
		for _, ns := range tm.GetCurrentNamespaces(true) {
			err := clearAgentsConfigMap(ns)
			if err != nil {
				return result(err)
			}
		}
	}
	return result(nil)
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
