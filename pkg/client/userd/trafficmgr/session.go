package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/homedir"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/tm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type apiServer struct {
	restapi.Server
	cancel context.CancelFunc
}

type apiMatcher struct {
	requestMatcher matcher.Request
	metadata       map[string]string
}

type session struct {
	*k8s.Cluster
	rootDaemon rootdRpc.DaemonClient

	// local information
	installID   string // telepresence's install ID
	userAndHost string // "laptop-username@laptop-hostname"

	// Kubernetes Port Forward Dialer
	pfDialer dnet.PortForwardDialer

	// manager client
	managerClient manager.ManagerClient

	// manager client connection
	managerConn *grpc.ClientConn

	// name reported by the manager
	managerName string

	// version reported by the manager
	managerVersion semver.Version

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

	sr *scout.Reporter

	isPodDaemon bool

	sessionConfig client.Config

	// done is closed when the session ends
	done chan struct{}
}

// firstAgentConfigMapVersion first version of traffic-manager that uses the agent ConfigMap.
var firstAgentConfigMapVersion = semver.MustParse("2.6.0") //nolint:gochecknoglobals // constant

func NewSession(
	ctx context.Context,
	sr *scout.Reporter,
	cr *rpc.ConnectRequest,
	config *client.Kubeconfig,
) (context.Context, userd.Session, *connector.ConnectInfo) {
	dlog.Info(ctx, "-- Starting new session")
	sr.Report(ctx, "connect")

	dlog.Info(ctx, "Connecting to k8s cluster...")
	cluster, err := k8s.ConnectCluster(ctx, cr, config)
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
	tmgr, err := connectMgr(ctx, sr, cluster, sr.InstallID(), cr)
	if err != nil {
		dlog.Errorf(ctx, "Unable to connect to session: %s", err)
		return ctx, nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}

	tmgr.sessionConfig = client.GetDefaultConfig()
	cliCfg, err := tmgr.managerClient.GetClientConfig(ctx, &empty.Empty{})
	if err != nil {
		if status.Code(err) != codes.Unimplemented {
			dlog.Warnf(ctx, "Failed to get remote config from traffic manager: %v", err)
		}
	} else {
		if err := yaml.Unmarshal(cliCfg.ConfigYaml, &tmgr.sessionConfig); err != nil {
			dlog.Warnf(ctx, "Failed to deserialize remote config: %v", err)
		}
		if err := tmgr.ApplyConfig(ctx); err != nil {
			dlog.Warnf(ctx, "failed to apply config from traffic-manager: %v", err)
		}
		if err := cluster.AddRemoteKubeConfigExtension(ctx, cliCfg.ConfigYaml); err != nil {
			dlog.Warnf(ctx, "Failed to set remote kubeconfig values: %v", err)
		}
	}

	rdRunning := userd.GetService(ctx).RootSessionInProcess()
	if !rdRunning {
		// Connect to the root daemon if it is running. It's the CLI that starts it initially
		rdRunning, err = socket.IsRunning(ctx, socket.RootDaemonPath(ctx))
		if err != nil {
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
	}

	var daemonStatus *rootdRpc.DaemonStatus
	if rdRunning {
		tmgr.rootDaemon, err = tmgr.connectRootDaemon(ctx, tmgr.getOutboundInfo(ctx))
		if err != nil {
			tmgr.managerConn.Close()
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
		daemonStatus, err = tmgr.rootDaemon.Status(ctx, &empty.Empty{})
		if err != nil {
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
	} else {
		dlog.Info(ctx, "Root daemon is not running")
	}

	// Collect data on how long connection time took
	dlog.Debug(ctx, "Finished connecting to traffic manager")
	sr.Report(ctx, "finished_connecting_traffic_manager", scout.Entry{
		Key: "connect_duration", Value: time.Since(connectStart).Seconds(),
	})

	tmgr.AddNamespaceListener(ctx, tmgr.updateDaemonNamespaces)
	ret := &rpc.ConnectInfo{
		Error:            rpc.ConnectInfo_UNSPECIFIED,
		ClusterContext:   cluster.Kubeconfig.Context,
		ClusterServer:    cluster.Kubeconfig.Server,
		ClusterId:        cluster.GetClusterId(ctx),
		SessionInfo:      tmgr.SessionInfo(),
		Intercepts:       &manager.InterceptInfoSnapshot{Intercepts: tmgr.getCurrentInterceptInfos()},
		ManagerNamespace: cluster.Kubeconfig.GetManagerNamespace(),
		DaemonStatus:     daemonStatus,
	}
	return ctx, tmgr, ret
}

func (s *session) As(ptr any) {
	switch ptr := ptr.(type) {
	case **session:
		*ptr = s
	case *manager.ManagerClient:
		*ptr = s.managerClient
	default:
		panic(fmt.Sprintf("%T does not implement %T", s, ptr))
	}
}

func (s *session) ManagerClient() manager.ManagerClient {
	return s.managerClient
}

func (s *session) ManagerConn() *grpc.ClientConn {
	return s.managerConn
}

func (s *session) ManagerName() string {
	return s.managerName
}

func (s *session) ManagerVersion() semver.Version {
	return s.managerVersion
}

func (s *session) GetSessionConfig() *client.Config {
	return &s.sessionConfig
}

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(
	ctx context.Context,
	sr *scout.Reporter,
	cluster *k8s.Cluster,
	installID string,
	cr *rpc.ConnectRequest,
) (*session, error) {
	clientConfig := client.GetConfig(ctx)
	tos := &clientConfig.Timeouts

	ctx, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	userinfo, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("unable to obtain current user: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("unable to obtain hostname: %w", err)
	}

	err = CheckTrafficManagerService(ctx, cluster.GetManagerNamespace())
	if err != nil {
		return nil, err
	}

	dlog.Debug(ctx, "creating port-forward")
	pfDialer, err := dnet.NewK8sPortForwardDialer(ctx, cluster.Kubeconfig.RestConfig, k8sapi.GetK8sInterface(ctx))
	if err != nil {
		return nil, err
	}
	conn, mClient, vi, err := tm.ConnectToManager(ctx, cluster.GetManagerNamespace(), pfDialer.Dial)
	if err != nil {
		return nil, err
	}
	managerVersion, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return nil, fmt.Errorf("unable to parse manager.Version: %w", err)
	}

	userAndHost := fmt.Sprintf("%s@%s", userinfo.Username, host)

	clusterHost := cluster.Kubeconfig.RestConfig.Host
	si, err := LoadSessionInfoFromUserCache(ctx, clusterHost)
	if err != nil {
		return nil, err
	}

	svc := userd.GetService(ctx)
	if si != nil {
		// Check if the session is still valid in the traffic-manager by calling Remain
		apiKey, err := svc.GetAPIKey(ctx)
		if err != nil {
			dlog.Errorf(ctx, "failed to retrieve API key: %v", err)
		}
		_, err = mClient.Remain(ctx, &manager.RemainRequest{
			Session: si,
			ApiKey:  apiKey,
		})
		if err == nil {
			if ctx.Err() != nil {
				// Call timed out, so the traffic-manager isn't responding at all
				return nil, ctx.Err()
			}
			dlog.Debugf(ctx, "traffic-manager port-forward established, client was already known to the traffic-manager as %q", userAndHost)
		} else {
			si = nil
		}
	}

	if si == nil {
		dlog.Debugf(ctx, "traffic-manager port-forward established, making client known to the traffic-manager as %q", userAndHost)
		si, err = mClient.ArriveAsClient(ctx, &manager.ClientInfo{
			Name:      userAndHost,
			InstallId: installID,
			Product:   "telepresence",
			Version:   client.Version(),
		})
		if err != nil {
			return nil, client.CheckTimeout(ctx, fmt.Errorf("manager.ArriveAsClient: %w", err))
		}
		if err = SaveSessionInfoToUserCache(ctx, clusterHost, si); err != nil {
			return nil, err
		}
	}

	var opts []grpc.CallOption
	cfg := client.GetConfig(ctx)
	if !cfg.Grpc.MaxReceiveSize.IsZero() {
		if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
			opts = append(opts, grpc.MaxCallRecvMsgSize(int(mz)))
		}
	}
	svc.SetManagerClient(mClient, opts...)

	managerName := vi.Name
	if managerName == "" {
		// Older traffic-managers doesn't distinguish between OSS and pro versions
		managerName = "Traffic Manager"
	}

	extraAlsoProxy, err := parseCIDR(cr.GetAlsoProxy())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra also proxy: %w", err)
	}

	extraNeverProxy, err := parseCIDR(cr.GetNeverProxy())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra never proxy: %w", err)
	}

	cluster.AlsoProxy = append(cluster.AlsoProxy, extraAlsoProxy...)
	cluster.NeverProxy = append(cluster.NeverProxy, extraNeverProxy...)

	return &session{
		Cluster:          cluster,
		installID:        installID,
		userAndHost:      userAndHost,
		managerClient:    mClient,
		managerConn:      conn,
		pfDialer:         pfDialer,
		managerName:      managerName,
		managerVersion:   managerVersion,
		sessionInfo:      si,
		localIntercepts:  make(map[string]struct{}),
		interceptWaiters: make(map[string]*awaitIntercept),
		wlWatcher:        newWASWatcher(),
		isPodDaemon:      cr.IsPodDaemon,
		sr:               sr,
		done:             make(chan struct{}),
	}, nil
}

func parseCIDR(cidr []string) ([]*iputil.Subnet, error) {
	result := make([]*iputil.Subnet, 0)

	if cidr == nil {
		return result, nil
	}

	for i := range cidr {
		_, ipNet, err := net.ParseCIDR(cidr[i])
		if err != nil {
			return nil, fmt.Errorf("failed to parse CIDR %s: %w", cidr[i], err)
		}
		result = append(result, (*iputil.Subnet)(ipNet))
	}

	return result, nil
}

func CheckTrafficManagerService(ctx context.Context, namespace string) error {
	dlog.Debug(ctx, "checking that traffic-manager exists")
	coreV1 := k8sapi.GetK8sInterface(ctx).CoreV1()
	if _, err := coreV1.Services(namespace).Get(ctx, "traffic-manager", meta.GetOptions{}); err != nil {
		msg := fmt.Sprintf("unable to get service traffic-manager in %s: %v", namespace, err)
		se := &k8serrors.StatusError{}
		if errors.As(err, &se) {
			if se.Status().Code == http.StatusNotFound {
				msg = ("traffic manager not found, if it is not installed, please run 'telepresence helm install'. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in.")
			}
		}
		return errcat.User.New(msg)
	}
	return nil
}

func connectError(t rpc.ConnectInfo_ErrType, err error) *rpc.ConnectInfo {
	return &rpc.ConnectInfo{
		Error:         t,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

func (s *session) setInterceptedNamespace(c context.Context, ns string) {
	s.currentInterceptsLock.Lock()
	diff := s.interceptedNamespace != ns
	if diff {
		s.interceptedNamespace = ns
	}
	s.currentInterceptsLock.Unlock()
	if diff {
		s.updateDaemonNamespaces(c)
	}
}

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (s *session) updateDaemonNamespaces(c context.Context) {
	s.wlWatcher.setNamespacesToWatch(c, s.GetCurrentNamespaces(true))
	if s.rootDaemon == nil {
		return
	}
	var namespaces []string
	s.currentInterceptsLock.Lock()
	if s.interceptedNamespace != "" {
		namespaces = []string{s.interceptedNamespace}
	}
	s.currentInterceptsLock.Unlock()
	// Avoid being locked for the remainder of this function.

	// Pass current mapped namespaces as plain names (no ending dot). The DNS-resolver will
	// create special mapping for those, allowing names like myservice.mynamespace to be resolved
	paths := s.GetCurrentNamespaces(false)
	dlog.Debugf(c, "posting search paths %v and namespaces %v", paths, namespaces)

	if _, err := s.rootDaemon.SetDnsSearchPath(c, &rootdRpc.Paths{Paths: paths, Namespaces: namespaces}); err != nil {
		dlog.Errorf(c, "error posting search paths %v and namespaces %v to root daemon: %v", paths, namespaces, err)
	}
	dlog.Debug(c, "search paths posted successfully")
}

func (s *session) Epilog(ctx context.Context) {
	if s.rootDaemon != nil {
		_, _ = s.rootDaemon.Disconnect(ctx, &empty.Empty{})
	}
	_ = s.pfDialer.Close()
	dlog.Info(ctx, "-- Session ended")
	close(s.done)
}

func (s *session) StartServices(g *dgroup.Group) {
	g.Go("remain", s.remain)
	g.Go("intercept-port-forward", s.watchInterceptsHandler)
	g.Go("agent-watcher", s.agentInfoWatcher)
	g.Go("dial-request-watcher", s.dialRequestWatcher)
}

func runWithRetry(ctx context.Context, f func(context.Context) error) error {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		if err := f(ctx); err != nil {
			dlog.Error(ctx, err)
			dtime.SleepWithContext(ctx, backoff)
			backoff *= 2
			if backoff > 3*time.Second {
				backoff = 3 * time.Second
			}
		}
	}
	return nil
}

func (s *session) Done() <-chan struct{} {
	return s.done
}

func (s *session) Reporter() *scout.Reporter {
	return s.sr
}

func (s *session) SessionInfo() *manager.SessionInfo {
	return s.sessionInfo
}

func (s *session) ApplyConfig(ctx context.Context) error {
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		return err
	}
	err = client.MergeAndReplace(ctx, &s.sessionConfig, cfg, false)
	if err != nil {
		return err
	}
	if len(s.MappedNamespaces) == 0 {
		mns := client.GetConfig(ctx).Cluster.MappedNamespaces
		if len(mns) > 0 {
			s.SetMappedNamespaces(ctx, mns)
		}
	}
	return err
}

// getInfosForWorkloads returns a list of workloads found in the given namespace that fulfils the given filter criteria.
func (s *session) getInfosForWorkloads(
	ctx context.Context,
	namespaces []string,
	iMap map[string][]*manager.InterceptInfo,
	aMap map[string]*manager.AgentInfo,
	filter rpc.ListRequest_Filter,
) []*rpc.WorkloadInfo {
	wiMap := make(map[types.UID]*rpc.WorkloadInfo)
	s.wlWatcher.eachService(ctx, s.GetManagerNamespace(), namespaces, func(svc *core.Service) {
		wls, err := s.wlWatcher.findMatchingWorkloads(ctx, svc)
		if err != nil {
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
	return wiz
}

func (s *session) waitForSync(ctx context.Context) {
	s.wlWatcher.setNamespacesToWatch(ctx, s.GetCurrentNamespaces(true))
	s.wlWatcher.waitForSync(ctx)
}

func (s *session) getActiveNamespaces(ctx context.Context) []string {
	s.waitForSync(ctx)
	return s.wlWatcher.getActiveNamespaces()
}

func (s *session) addActiveNamespaceListener(l func()) {
	s.wlWatcher.addActiveNamespaceListener(l)
}

func (s *session) WatchWorkloads(c context.Context, wr *rpc.WatchWorkloadsRequest, stream userd.WatchWorkloadsStream) error {
	s.waitForSync(c)
	s.ensureWatchers(c, wr.Namespaces)
	sCtx, sCancel := context.WithCancel(c)
	// We need to make sure the subscription ends when we leave this method, since this is the one consuming the snapshotAvailable channel.
	// Otherwise, the goroutine that writes to the channel will leak.
	defer sCancel()
	snapshotAvailable := s.wlWatcher.subscribe(sCtx)
	for {
		select {
		case <-c.Done():
			return nil
		case <-snapshotAvailable:
			snapshot, err := s.workloadInfoSnapshot(c, wr.GetNamespaces(), rpc.ListRequest_INTERCEPTABLE, false)
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

func (s *session) WorkloadInfoSnapshot(
	ctx context.Context,
	namespaces []string,
	filter rpc.ListRequest_Filter,
	includeLocalIntercepts bool,
) (*rpc.WorkloadInfoSnapshot, error) {
	s.waitForSync(ctx)
	return s.workloadInfoSnapshot(ctx, namespaces, filter, includeLocalIntercepts)
}

func (s *session) ensureWatchers(ctx context.Context,
	namespaces []string,
) {
	dlog.Debugf(ctx, "Ensure watchers %v", namespaces)
	// If a watcher is started, we better wait for the next snapshot from WatchAgentsNS
	waitCh := make(chan struct{}, 1)
	s.currentAgentsLock.Lock()
	s.agentInitWaiters = append(s.agentInitWaiters, waitCh)
	s.currentAgentsLock.Unlock()
	needWait := false

	wg := sync.WaitGroup{}
	wg.Add(len(namespaces))
	for _, ns := range namespaces {
		if ns == "" {
			// Don't use tm.ActualNamespace here because the accessibility of the namespace
			// is actually determined once the watcher starts
			ns = s.Namespace
		}
		wgp := &wg
		s.wlWatcher.ensureStarted(ctx, ns, func(started bool) {
			if started {
				dlog.Debugf(ctx, "watchers for %s started", ns)
				needWait = true
			}
			if wgp != nil {
				wgp.Done()
				wgp = nil
			}
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

func (s *session) workloadInfoSnapshot(
	ctx context.Context,
	namespaces []string,
	filter rpc.ListRequest_Filter,
	includeLocalIntercepts bool,
) (*rpc.WorkloadInfoSnapshot, error) {
	is := s.getCurrentIntercepts()
	s.ensureWatchers(ctx, namespaces)

	var nss []string
	if filter == rpc.ListRequest_INTERCEPTS {
		// Special case, we don't care about namespaces in general. Instead, we use the intercepted namespaces
		s.currentInterceptsLock.Lock()
		if s.interceptedNamespace != "" {
			nss = []string{s.interceptedNamespace}
		}
		s.currentInterceptsLock.Unlock()
		if len(nss) == 0 {
			// No active intercepts
			return &rpc.WorkloadInfoSnapshot{}, nil
		}
	} else {
		nss = make([]string, 0, len(namespaces))
		for _, ns := range namespaces {
			ns = s.ActualNamespace(ns)
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
		for k, v := range s.getCurrentAgentsInNamespace(ns) {
			aMap[k] = v
		}
	}
	workloadInfos := s.getInfosForWorkloads(ctx, nss, iMap, aMap, filter)

	if includeLocalIntercepts {
		s.currentInterceptsLock.Lock()
		for localIntercept := range s.localIntercepts {
			workloadInfos = append(workloadInfos, &rpc.WorkloadInfo{InterceptInfos: []*manager.InterceptInfo{{
				Spec:              &manager.InterceptSpec{Name: localIntercept, Namespace: s.interceptedNamespace},
				Disposition:       manager.InterceptDispositionType_ACTIVE,
				MechanismArgsDesc: "as local-only",
			}}})
		}
		s.currentInterceptsLock.Unlock()
	}
	return &rpc.WorkloadInfoSnapshot{Workloads: workloadInfos}, nil
}

var ErrSessionExpired = errors.New("session expired")

func (s *session) remain(c context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer func() {
		ticker.Stop()
		c = dcontext.WithoutCancel(c)
		c, cancel := context.WithTimeout(c, 3*time.Second)
		defer cancel()
		if _, err := s.managerClient.Depart(c, s.SessionInfo()); err != nil {
			dlog.Errorf(c, "failed to depart from manager: %v", err)
		} else {
			// Depart succeeded so the traffic-manager has dropped the session. We should too
			if err = DeleteSessionInfoFromUserCache(c); err != nil {
				dlog.Errorf(c, "failed to delete session from user cache: %v", err)
			}
		}
		s.managerConn.Close()
	}()

	for {
		select {
		case <-c.Done():
			return nil
		case <-ticker.C:
			apiKey, err := userd.GetService(c).GetAPIKey(c)
			if err != nil {
				dlog.Errorf(c, "failed to retrieve API key: %v", err)
			}
			_, err = s.managerClient.Remain(c, &manager.RemainRequest{
				Session: s.SessionInfo(),
				ApiKey:  apiKey,
			})
			if err != nil && c.Err() == nil {
				dlog.Error(c, err)
				if gErr, ok := status.FromError(err); ok && gErr.Code() == codes.NotFound {
					// Session has expired. We need to cancel the owner session and reconnect
					return ErrSessionExpired
				}
			}
		}
	}
}

func (s *session) UpdateStatus(c context.Context, cr *rpc.ConnectRequest) *rpc.ConnectInfo {
	config, err := client.DaemonKubeconfig(c, cr)
	if err != nil {
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}

	if !cr.IsPodDaemon && !s.Kubeconfig.ContextServiceAndFlagsEqual(config) {
		return &rpc.ConnectInfo{
			Error:          rpc.ConnectInfo_MUST_RESTART,
			ClusterContext: s.Kubeconfig.Context,
			ClusterServer:  s.Kubeconfig.Server,
			ClusterId:      s.GetClusterId(c),
		}
	}

	namespaces := cr.MappedNamespaces
	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	}
	if len(namespaces) == 0 {
		namespaces = client.GetConfig(c).Cluster.MappedNamespaces
	}

	if s.SetMappedNamespaces(c, namespaces) {
		if len(namespaces) == 0 && s.CanWatchNamespaces(c) {
			s.StartNamespaceWatcher(c)
		}
		s.currentInterceptsLock.Lock()
		s.ingressInfo = nil
		s.currentInterceptsLock.Unlock()
	}
	return s.Status(c)
}

func (s *session) Status(c context.Context) *rpc.ConnectInfo {
	cfg := s.Kubeconfig
	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_ALREADY_CONNECTED,
		ClusterContext: cfg.Context,
		ClusterServer:  cfg.Server,
		ClusterId:      s.GetClusterId(c),
		SessionInfo:    s.SessionInfo(),
		Intercepts:     &manager.InterceptInfoSnapshot{Intercepts: s.getCurrentInterceptInfos()},
		Version: &common.VersionInfo{
			ApiVersion: client.APIVersion,
			Version:    client.Version(),
			Executable: client.GetExe(),
			Name:       client.DisplayName,
		},
		ManagerNamespace: cfg.GetManagerNamespace(),
	}
	if len(s.MappedNamespaces) > 0 || len(s.sessionConfig.Cluster.MappedNamespaces) > 0 {
		ret.MappedNamespaces = s.GetCurrentNamespaces(true)
	}
	if s.rootDaemon != nil {
		var err error
		ret.DaemonStatus, err = s.rootDaemon.Status(c, &empty.Empty{})
		if err != nil {
			return connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
	}
	return ret
}

// Given a slice of AgentInfo, this returns another slice of agents with one
// agent per namespace, name pair.
// Deprecated: not used with traffic-manager versions >= 2.6.0.
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

// Deprecated: not used with traffic-manager versions >= 2.6.0.
func (s *session) legacyUninstall(c context.Context, ur *rpc.UninstallRequest) (*common.Result, error) {
	result := &common.Result{}
	agents := s.getCurrentAgents()

	// Since workloads can have more than one replica, we get a slice of agents
	// where the agent to workload mapping is 1-to-1.  This is important
	// because in the ALL_AGENTS or default case, we could edit the same
	// workload n times for n replicas, which could cause race conditions
	agents = getRepresentativeAgents(c, agents)

	_ = s.ClearIntercepts(c)
	switch ur.UninstallType {
	case rpc.UninstallRequest_UNSPECIFIED:
		return nil, status.Error(codes.InvalidArgument, "invalid uninstall request")
	case rpc.UninstallRequest_NAMED_AGENTS:
		var selectedAgents []*manager.AgentInfo
		for _, di := range ur.Agents {
			found := false
			namespace := s.ActualNamespace(ur.Namespace)
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
func (s *session) Uninstall(ctx context.Context, ur *rpc.UninstallRequest) (*common.Result, error) {
	if s.managerVersion.LT(firstAgentConfigMapVersion) {
		// fall back traffic-manager behaviour prior to 2.6
		return s.legacyUninstall(ctx, ur)
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
		s.waitForSync(ctx)
		if ur.Namespace == "" {
			ur.Namespace = s.Namespace
		}
		s.wlWatcher.ensureStarted(ctx, ur.Namespace, nil)
		namespace := s.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return errcat.ToResult(errcat.User.Newf("namespace %s is not mapped", ur.Namespace)), nil
		}
		cm, err := loadAgentConfigMap(namespace)
		if err != nil || cm == nil {
			return errcat.ToResult(err), nil
		}
		changed := false
		ics := s.getCurrentIntercepts()
		for _, an := range ur.Agents {
			for _, ic := range ics {
				if ic.Spec.Namespace == namespace && ic.Spec.Agent == an {
					_ = s.removeIntercept(ctx, ic)
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

	_ = s.ClearIntercepts(ctx)
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
		s.waitForSync(ctx)
		if ur.Namespace == "" {
			ur.Namespace = s.Namespace
		}
		s.wlWatcher.ensureStarted(ctx, ur.Namespace, nil)
		namespace := s.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return errcat.ToResult(errcat.User.Newf("namespace %s is not mapped", ur.Namespace)), nil
		}
		return errcat.ToResult(clearAgentsConfigMap(namespace)), nil
	} else {
		// Load all effected configmaps
		for _, ns := range s.GetCurrentNamespaces(true) {
			err := clearAgentsConfigMap(ns)
			if err != nil {
				return errcat.ToResult(err), nil
			}
		}
	}
	return errcat.ToResult(nil), nil
}

// getClusterCIDRs finds the service CIDR and the pod CIDRs of all nodes in the cluster.
func (s *session) getOutboundInfo(ctx context.Context) *rootdRpc.OutboundInfo {
	// We'll figure out the IP address of the API server(s) so that we can tell the daemon never to proxy them.
	// This is because in some setups the API server will be in the same CIDR range as the pods, and the
	// daemon will attempt to proxy traffic to it. This usually results in a loss of all traffic to/from
	// the cluster, since an open tunnel to the traffic-manager (via the API server) is itself required
	// to communicate with the cluster.
	neverProxy := []*manager.IPNet{}
	serverURL, err := url.Parse(s.Server)
	if err != nil {
		// This really shouldn't happen as we are connected to the server
		dlog.Errorf(ctx, "Unable to parse url for k8s server %s: %v", s.Server, err)
	} else {
		hostname := serverURL.Hostname()
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
	for _, np := range s.NeverProxy {
		neverProxy = append(neverProxy, iputil.IPNetToRPC((*net.IPNet)(np)))
	}
	kubeFlags := s.FlagMap
	if kc, ok := os.LookupEnv("KUBECONFIG"); ok {
		kubeFlags = maps.Copy(s.FlagMap)
		kubeFlags["KUBECONFIG"] = kc
	}
	info := &rootdRpc.OutboundInfo{
		Session:           s.sessionInfo,
		NeverProxySubnets: neverProxy,
		HomeDir:           homedir.HomeDir(),
		ManagerNamespace:  s.GetManagerNamespace(),
		KubeFlags:         kubeFlags,
	}

	if s.DNS != nil {
		info.Dns = &rootdRpc.DNSConfig{
			ExcludeSuffixes: s.DNS.ExcludeSuffixes,
			IncludeSuffixes: s.DNS.IncludeSuffixes,
			LookupTimeout:   durationpb.New(s.DNS.LookupTimeout.Duration),
		}
		if len(s.DNS.LocalIP) > 0 {
			info.Dns.LocalIp = s.DNS.LocalIP.IP()
		}
		if len(s.DNS.RemoteIP) > 0 {
			info.Dns.RemoteIp = s.DNS.RemoteIP.IP()
		}
	}

	if len(s.AlsoProxy) > 0 {
		info.AlsoProxySubnets = make([]*manager.IPNet, len(s.AlsoProxy))
		for i, ap := range s.AlsoProxy {
			info.AlsoProxySubnets[i] = iputil.IPNetToRPC((*net.IPNet)(ap))
		}
	}
	return info
}

func (s *session) connectRootDaemon(ctx context.Context, oi *rootdRpc.OutboundInfo) (rd rootdRpc.DaemonClient, err error) {
	// establish a connection to the root daemon gRPC grpcService
	dlog.Info(ctx, "Connecting to root daemon...")
	svc := userd.GetService(ctx)
	if svc.RootSessionInProcess() {
		// Just run the root session in-process.
		rootSession := rootd.NewInProcSession(ctx, svc.Reporter(), oi, s.managerClient, s.managerVersion)
		if err = rootSession.Start(ctx, dgroup.NewGroup(ctx, dgroup.GroupConfig{})); err != nil {
			return nil, err
		}
		rd = rootSession
	} else {
		var conn *grpc.ClientConn
		conn, err = socket.Dial(ctx, socket.RootDaemonPath(ctx),
			grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
			grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
		)
		if err != nil {
			return nil, fmt.Errorf("unable open root daemon socket: %w", err)
		}
		defer func() {
			if err != nil {
				conn.Close()
			}
		}()
		rd = rootdRpc.NewDaemonClient(conn)

		for attempt := 1; ; attempt++ {
			var rootStatus *rootdRpc.DaemonStatus
			if rootStatus, err = rd.Connect(ctx, oi); err != nil {
				return nil, fmt.Errorf("failed to connect to root daemon: %w", err)
			}
			oc := rootStatus.OutboundConfig
			if oc == nil || oc.Session == nil {
				// This is an internal error. Something is wrong with the root daemon.
				return nil, errors.New("root daemon's OutboundConfig has no Session")
			}
			if oc.Session.SessionId == oi.Session.SessionId {
				break
			}

			// Root daemon was running an old session. This indicates that this daemon somehow
			// crashed without disconnecting. So let's do that now, and then reconnect...
			if attempt == 2 {
				// ...or not, since we've already done it.
				return nil, errors.New("unable to reconnect to root daemon")
			}
			if _, err = rd.Disconnect(ctx, &empty.Empty{}); err != nil {
				return nil, fmt.Errorf("failed to disconnect from the root daemon: %w", err)
			}
		}
	}

	// The root daemon needs time to set up the TUN-device and DNS, which involves interacting
	// with the cluster-side traffic-manager. We know that the traffic-manager is up and
	// responding at this point, so it shouldn't take too long.
	ctx, cancel := client.GetConfig(ctx).Timeouts.TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer cancel()
	if _, err = rd.WaitForNetwork(ctx, &empty.Empty{}); err != nil {
		if se, ok := status.FromError(err); ok {
			err = se.Err()
		}
		return nil, fmt.Errorf("failed to connect to root daemon: %v", err)
	}
	dlog.Debug(ctx, "Connected to root daemon")
	return rd, nil
}
