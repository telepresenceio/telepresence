package trafficmgr

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"os/user"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

type apiServer struct {
	restapi.Server
	cancel context.CancelFunc
}

type apiMatcher struct {
	requestMatcher matcher.Request
	metadata       map[string]string
}

type workloadInfoKey struct {
	kind manager.WorkloadInfo_Kind
	name string
}

type workloadInfo struct {
	uid              k8sTypes.UID
	state            workload.State
	agentState       manager.WorkloadInfo_AgentState
	interceptClients []string
}

type session struct {
	*k8s.Cluster
	rootDaemon         rootdRpc.DaemonClient
	subnetViaWorkloads []*rootdRpc.SubnetViaWorkload

	// local information
	installID string // telepresence's install ID
	clientID  string // "laptop-username@laptop-hostname"

	// manager client
	managerClient manager.ManagerClient

	// manager client connection
	managerConn *grpc.ClientConn

	// name reported by the manager
	managerName string

	// version reported by the manager
	managerVersion semver.Version

	// The identifier for this daemon
	daemonID *daemon.Identifier

	sessionInfo *manager.SessionInfo // sessionInfo returned by the traffic-manager

	workloadsLock sync.Mutex

	// Map of manager.WorkloadInfo split into namespace, key of kind and name, and workloadInfo
	workloads map[string]map[workloadInfoKey]workloadInfo

	workloadSubscribers map[uuid.UUID]chan struct{}

	// currentIngests is tracks the ingests that are active in this session.
	currentIngests *xsync.Map[ingestKey, *ingest]

	ingestTracker *podAccessTracker

	// currentInterceptsLock ensures that all accesses to currentAgents, currentIntercepts, currentMatchers,
	// currentAPIServers, interceptWaiters, and ingressInfo are synchronized
	//
	currentInterceptsLock sync.Mutex

	// currentAgents is the latest snapshot returned by the agents watcher.
	currentAgents []*manager.AgentInfo

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

	isPodDaemon bool

	cancel context.CancelFunc

	// context with a done channel that is closed when the session ends
	context context.Context

	// Synthetic IPs are generated when the targetIP is a hostname, so that we can defer the
	// lookup of that host until the time when it is dialed.
	syntheticIPs map[netip.Addr]string
}

func NewSession(ctx context.Context, cri userd.ConnectRequest, config *client.Kubeconfig, wg *sync.WaitGroup) (session userd.Session, info *rpc.ConnectInfo) {
	dlog.Info(ctx, "-- Starting new session")

	cr := cri.Request()
	dlog.Infof(ctx, "Connecting to k8s context %s (%s) ...", config.Context, config.Server)
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		if session == nil {
			cancel()
		}
	}()

	ctx, cluster, err := k8s.ConnectCluster(ctx, cr, config)
	if err != nil {
		dlog.Errorf(ctx, "unable to track k8s cluster: %+v", err)
		return nil, connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	dlog.Infof(ctx, "Connected to context %s, namespace %s (%s)", cluster.Context, cluster.Namespace, cluster.Server)
	ctx = portforward.WithRestConfig(ctx, cluster.RestConfig)

	ctx = cluster.WithJoinedClientSetInterface(ctx)

	dlog.Info(ctx, "Connecting to traffic manager...")
	installID, err := client.InstallID(ctx)
	if err != nil {
		return nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}
	tmgr, err := connectMgr(ctx, cancel, cluster, installID, cr)
	if err != nil {
		dlog.Errorf(ctx, "Unable to connect to session: %s", err)
		return nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}
	if tmgr.compareFinalizedManagerVersion(2, 21, 0) < 0 {
		return nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED,
			fmt.Errorf("traffic manager version %s is too old. Minimum supported version is 2.21.0, please upgrade", tmgr.managerVersion))
	}

	ctx = withSession(ctx, tmgr)
	var tmCfg client.Config
	cliCfg, err := tmgr.managerClient.GetClientConfig(ctx, &empty.Empty{})
	if err != nil {
		if status.Code(err) != codes.Unimplemented {
			dlog.Warnf(ctx, "Failed to get remote config from traffic manager: %v", err)
		}
		tmCfg = client.GetDefaultConfig()
	} else {
		tmCfg, err = client.ParseConfigYAML(ctx, "client configuration from cluster", cliCfg.ConfigYaml)
		if err != nil {
			dlog.Warn(ctx, err.Error())
		}
	}

	// Merge traffic-manager's reported config, but get priority to the local config.
	cfg := client.GetConfig(ctx)
	if tmCfg != nil {
		cfg = tmCfg.Merge(cfg)
		rt := cfg.Routing()
		rt.NeverProxy = append(rt.NeverProxy, tmCfg.Routing().NeverProxy...)
		ctx = client.WithConfig(ctx, cfg)
		tmgr.context = ctx
	}
	if err = tmgr.ApplyConfig(); err != nil {
		dlog.Warn(ctx, err.Error())
	}
	if dlog.MaxLogLevel(ctx) >= dlog.LogLevelDebug {
		dlog.Debug(ctx, "Applying client configuration")
		buf, _ := client.MarshalJSON(cfg)
		buf, _ = yaml.JSONToYAML(buf)
		sc := bufio.NewScanner(bytes.NewReader(buf))
		for sc.Scan() {
			dlog.Debug(ctx, sc.Text())
		}
	}

	oi := tmgr.getNetworkInfo(ctx, cr)
	if !userd.GetService(ctx).RootSessionInProcess() {
		// Connect to the root daemon if it is running. It's the CLI that starts it initially
		rootRunning, err := socket.IsRunning(ctx, socket.RootDaemonPath(ctx))
		if err != nil {
			return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
		if !rootRunning {
			return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, errors.New("root daemon is not running"))
		}

		// Root daemon needs this to authenticate with the cluster. Potential exec configurations in the kubeconfig
		// must be executed by the user, not by root.
		konfig, err := patcher.CreateExternalKubeConfig(ctx, config.ClientConfig, cluster.Context, func([]string) (string, string, error) {
			return client.GetExe(ctx), userd.GetService(ctx).ListenerAddress(ctx), nil
		}, nil)
		if err != nil {
			return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
		patcher.AnnotateNetworkConfig(ctx, oi, konfig.CurrentContext)
	}

	ctx = tunnel.WithSyntheticIPResolver(ctx, tmgr)
	tmgr.context = ctx

	tmgr.rootDaemon, err = tmgr.connectRootDaemon(ctx, oi, wg, cr.IsPodDaemon)
	if err != nil {
		tmgr.managerConn.Close()
		return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
	}

	// Collect data on how long connection time took
	dlog.Debug(ctx, "Finished connecting to traffic manager")

	tmgr.AddNamespaceListener(ctx, tmgr.updateDaemonNamespaces)
	return tmgr, tmgr.status(true)
}

// Run (1) starts up with ensuring that the manager is installed and running,
// but then for most of its life
//   - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//   - run the intercepts (manager.WatchIntercepts) and then
//   - (3) listen on the appropriate local ports and forward them to the intercepted
//     Services, and
//   - (4) mount the appropriate remote volumes.
func (s *session) Run() error {
	g := dgroup.NewGroup(s.context, dgroup.GroupConfig{})
	defer s.epilogue()
	s.startServices(g)
	return g.Wait()
}

func (s *session) Cancel() {
	s.cancel()
}

func (s *session) RootDaemon() rootdRpc.DaemonClient {
	return s.rootDaemon
}

func (s *session) ManagerClient() manager.ManagerClient {
	return s.managerClient
}

func (s *session) ManagerName() string {
	return s.managerName
}

func (s *session) ManagerVersion() semver.Version {
	return s.managerVersion
}

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(
	longLivedCtx context.Context,
	sessionCancel context.CancelFunc,
	cluster *k8s.Cluster,
	installID string,
	cr *rpc.ConnectRequest,
) (*session, error) {
	cfg := client.GetConfig(longLivedCtx)
	tos := cfg.Timeouts()

	ctx, cancel := tos.TimeoutContext(longLivedCtx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	mgrNs := k8s.GetManagerNamespace(ctx)
	err := checkTrafficManagerService(ctx, mgrNs)
	if err != nil {
		return nil, err
	}

	conn, mClient, vi, err := k8sclient.ConnectToManager(longLivedCtx, ctx, mgrNs)
	if err != nil {
		return nil, err
	}
	if sdc := cfg.Grpc().SimulateDisconnect; sdc > 0 {
		time.AfterFunc(sdc, func() {
			dlog.Info(ctx, "Simulated disconnect from manager")
			conn.Close()
		})
	}
	managerVersion, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return nil, fmt.Errorf("unable to parse manager.Version: %w", err)
	}

	clientID := cr.ClientId
	if clientID == "" {
		userinfo, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("unable to obtain current user: %w", err)
		}
		host, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("unable to obtain hostname: %w", err)
		}

		clientID = fmt.Sprintf("%s@%s", userinfo.Username, host)
	}

	daemonID := daemon.NewIdentifier(cr.Name, cluster.Context, cluster.Namespace, proc.RunningInContainer())
	si, err := LoadSessionInfoFromUserCache(ctx, daemonID)
	if err != nil {
		return nil, err
	}

	if si != nil {
		// Check if the session is still valid in the traffic-manager by calling Remain
		_, err = mClient.Remain(ctx, &manager.RemainRequest{Session: si})
		if err == nil {
			if ctx.Err() != nil {
				// Call timed out, so the traffic-manager isn't responding at all
				return nil, ctx.Err()
			}
			dlog.Debugf(ctx, "traffic-manager port-forward established, client was already known to the traffic-manager as %q", clientID)
		} else {
			si = nil
		}
	}

	if si == nil {
		dlog.Debugf(ctx, "traffic-manager port-forward established, making client known to the traffic-manager as %q", clientID)
		si, err = mClient.ArriveAsClient(ctx, &manager.ClientInfo{
			Name:      clientID,
			Namespace: cluster.Namespace,
			InstallId: installID,
			Product:   "telepresence",
			Version:   client.Version(),
		})
		if err != nil {
			if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
				return nil, errcat.User.New(st.Message())
			}
			return nil, client.CheckTimeout(ctx, fmt.Errorf("manager.ArriveAsClient: %w", err))
		}
		if err = saveSessionInfoToUserCache(ctx, daemonID, si); err != nil {
			return nil, err
		}
	}

	managerName := vi.Name
	if managerName == "" {
		// Older traffic-managers don't distinguish between OSS and pro-versions
		managerName = "Traffic Manager"
	}

	sess := &session{
		Cluster:            cluster,
		installID:          installID,
		daemonID:           daemonID,
		clientID:           clientID,
		managerClient:      mClient,
		managerConn:        conn,
		managerName:        managerName,
		managerVersion:     managerVersion,
		sessionInfo:        si,
		currentIngests:     xsync.NewMap[ingestKey, *ingest](),
		ingestTracker:      newPodAccessTracker(),
		workloads:          make(map[string]map[workloadInfoKey]workloadInfo),
		interceptWaiters:   make(map[string]*awaitIntercept),
		isPodDaemon:        cr.IsPodDaemon,
		subnetViaWorkloads: cr.SubnetViaWorkloads,
		context:            longLivedCtx,
		cancel:             sessionCancel,
	}
	return sess, nil
}

func (s *session) reconnectManager() (returnedErr error) {
	cfg := client.GetConfig(s.context)
	tos := cfg.Timeouts()
	ctx := s.context
	tc, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	conn, mc, vi, err := k8sclient.ConnectToManager(ctx, tc, k8s.GetManagerNamespace(ctx))
	if err != nil {
		return err
	}
	defer func() {
		if returnedErr != nil {
			conn.Close()
		}
	}()
	managerVersion, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return fmt.Errorf("unable to parse manager.Version: %w", err)
	}

	_, err = mc.ReconnectClient(tc, &manager.ReconnectClientRequest{
		Session: s.sessionInfo,
		Client: &manager.ClientInfo{
			Name:      s.clientID,
			Namespace: s.Namespace,
			InstallId: s.installID,
			Product:   "telepresence",
			Version:   client.Version(),
		},
		Intercepts: s.getCurrentInterceptInfos(),
		Agents:     s.getCurrentAgents(),
	})
	if err != nil {
		return fmt.Errorf("unable to reconnect client: %w", err)
	}

	s.managerClient = mc
	s.managerConn = conn
	s.managerName = vi.Name
	s.managerVersion = managerVersion
	return nil
}

func (s *session) remain(ctx context.Context) error {
	ctx, cancel := client.GetConfig(ctx).Timeouts().TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer cancel()
	_, err := s.ManagerClient().Remain(ctx, &manager.RemainRequest{Session: s.SessionInfo()})
	if err != nil {
		dlog.Errorf(ctx, "error calling Remain: %v", client.CheckTimeout(ctx, err))
	}
	return nil
}

func checkTrafficManagerService(ctx context.Context, namespace string) error {
	dlog.Debug(ctx, "checking that traffic-manager exists")
	coreV1 := k8sapi.GetK8sInterface(ctx).CoreV1()
	if _, err := coreV1.Services(namespace).Get(ctx, agentconfig.ManagerAppName, meta.GetOptions{}); err != nil {
		msg := fmt.Sprintf("unable to get service %s in %s: %v", agentconfig.ManagerAppName, namespace, err)
		se := &k8serrors.StatusError{}
		if errors.As(err, &se) {
			if se.Status().Code == http.StatusNotFound {
				msg = "traffic manager not found, if it is not installed, please run 'telepresence helm install'. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in."
			}
		}
		return errcat.User.New(msg)
	}
	return nil
}

func connectError(t rpc.ConnectInfo_ErrType, err error) *rpc.ConnectInfo {
	st := status.Convert(err)
	for _, detail := range st.Details() {
		if detail, ok := detail.(*common.Result); ok {
			return &rpc.ConnectInfo{
				Error:         t,
				ErrorText:     string(detail.Data),
				ErrorCategory: int32(detail.ErrorCategory),
			}
		}
	}
	return &rpc.ConnectInfo{
		Error:         t,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (s *session) updateDaemonNamespaces(c context.Context) {
	const svcDomain = "svc"

	domains := s.GetCurrentNamespaces(false)
	if !slices.Contains(domains, svcDomain) {
		domains = append(domains, svcDomain)
	}
	dlog.Debugf(c, "posting top-level domains %v to root daemon", domains)

	if _, err := s.rootDaemon.SetDNSTopLevelDomains(c, &rootdRpc.Domains{Domains: domains}); err != nil {
		dlog.Errorf(c, "error posting domains %v to root daemon: %v", domains, err)
	}
	dlog.Debug(c, "domains posted successfully")
}

func (s *session) epilogue() {
	_, _ = s.rootDaemon.Disconnect(s.context, &empty.Empty{})
	dlog.Info(s.context, "-- Session ended")
	s.cancel()
}

func (s *session) startServices(g *dgroup.Group) {
	g.Go("remain", s.remainLoop)
	g.Go("agents", s.watchAgentsLoop)
	g.Go("intercept-port-forward", s.watchInterceptsHandler)
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
		} else {
			break
		}
	}
	return nil
}

func (s *session) Done() <-chan struct{} {
	return s.context.Done()
}

func (s *session) SessionInfo() *manager.SessionInfo {
	return s.sessionInfo
}

func (s *session) ApplyConfig() error {
	ctx := s.context
	err := client.ReloadDaemonLogLevel(ctx, false)
	if err != nil {
		return err
	}
	if len(s.MappedNamespaces) == 0 {
		mns := client.GetConfig(ctx).Cluster().MappedNamespaces
		if len(mns) > 0 {
			s.SetMappedNamespaces(ctx, mns)
		}
	}
	return nil
}

// getInfosForWorkloads returns a list of workloads found in the given namespace that fulfils the given filter criteria.
func (s *session) getInfosForWorkloads(
	namespaces []string,
	iMap map[string][]*manager.InterceptInfo,
	gMap map[string][]*rpc.IngestInfo,
	sMap map[string]string,
	filter rpc.ListRequest_Filter,
) []*rpc.WorkloadInfo {
	wiMap := make(map[string]*rpc.WorkloadInfo)
	s.eachWorkload(namespaces, func(wlKind manager.WorkloadInfo_Kind, name, namespace string, info workloadInfo) {
		kind := wlKind.String()
		wlInfo := &rpc.WorkloadInfo{
			Name:                 name,
			Namespace:            namespace,
			WorkloadResourceType: kind,
			Uid:                  string(info.uid),
		}
		if info.state != workload.StateAvailable {
			wlInfo.NotInterceptableReason = info.state.String()
		}

		var ok bool
		filterMatch := rpc.ListRequest_EVERYTHING

		filterMatch &= ^(rpc.ListRequest_REPLACEMENTS | rpc.ListRequest_INTERCEPTS | rpc.ListRequest_WIRETAPS)
		iis, ok := iMap[name]
		if ok {
			for _, ii := range iis {
				include := false
				switch {
				case ii.Spec.NoDefaultPort:
					filterMatch |= rpc.ListRequest_REPLACEMENTS
					include = filter&rpc.ListRequest_REPLACEMENTS != 0
				case ii.Spec.Wiretap:
					filterMatch |= rpc.ListRequest_WIRETAPS
					include = filter&rpc.ListRequest_WIRETAPS != 0
				default:
					filterMatch |= rpc.ListRequest_INTERCEPTS
					include = filter&rpc.ListRequest_INTERCEPTS != 0
				}
				if include || filter == 0 {
					wlInfo.InterceptInfo = append(wlInfo.InterceptInfo, ii)
				}
			}
		}
		if wlInfo.IngestInfo, ok = gMap[name]; !ok {
			filterMatch &= ^rpc.ListRequest_INGESTS
		}
		if wlInfo.AgentVersion, ok = sMap[name]; !ok {
			filterMatch &= ^rpc.ListRequest_INSTALLED_AGENTS
		}
		dlog.Debugf(s.context, "filter %d, filterMatch %d", filter, filterMatch)
		if filter != 0 && filter&filterMatch == 0 {
			return
		}
		wiMap[fmt.Sprintf("%s:%s.%s", kind, name, namespace)] = wlInfo
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

func (s *session) WatchWorkloads(wr *rpc.WatchWorkloadsRequest, stream userd.WatchWorkloadsStream) error {
	id := uuid.New()
	ch := make(chan struct{})
	s.workloadsLock.Lock()
	if s.workloadSubscribers == nil {
		s.workloadSubscribers = make(map[uuid.UUID]chan struct{})
	}
	s.workloadSubscribers[id] = ch
	s.workloadsLock.Unlock()

	defer func() {
		s.workloadsLock.Lock()
		delete(s.workloadSubscribers, id)
		s.workloadsLock.Unlock()
	}()

	send := func() error {
		ws, err := s.WorkloadInfoSnapshot(wr.Namespaces, rpc.ListRequest_UNSPECIFIED)
		if err != nil {
			return err
		}
		return stream.Send(ws)
	}

	// Send initial snapshot
	if err := send(); err != nil {
		return err
	}
	for {
		select {
		case <-s.Done():
			return nil
		case <-ch:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

func (s *session) ensureWatchers(namespaces []string) {
	wg := sync.WaitGroup{}
	wg.Add(len(namespaces))
	ctx := s.context
	for _, ns := range namespaces {
		s.workloadsLock.Lock()
		_, ok := s.workloads[ns]
		s.workloadsLock.Unlock()
		if ok {
			wg.Done()
		} else {
			go func() {
				err := s.workloadsWatcher(ctx, ns, &wg)
				if err != nil {
					dlog.Errorf(ctx, "error ensuring watcher for namespace %s: %v", ns, err)
					return
				}
			}()
			dlog.Debugf(ctx, "watcher for namespace %s started", ns)
		}
	}
	wg.Wait()
}

func (s *session) WorkloadInfoSnapshot(
	namespaces []string,
	filter rpc.ListRequest_Filter,
) (*rpc.WorkloadInfoSnapshot, error) {
	is := s.getCurrentIntercepts()

	var nss []string
	var sMap map[string]string
	nss = make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		ns = s.ActualNamespace(ns)
		if ns != "" {
			nss = append(nss, ns)
		}
	}
	if len(nss) == 0 {
		// none of the namespaces are currently mapped
		dlog.Debug(s.context, "No namespaces are mapped")
		return &rpc.WorkloadInfoSnapshot{}, nil
	}
	if len(nss) == 1 && nss[0] == s.Namespace {
		cas := s.getCurrentAgents()
		sMap = make(map[string]string, len(cas))
		for _, a := range cas {
			sMap[a.Name] = a.Version
		}
	}
	s.ensureWatchers(nss)
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
	gMap := make(map[string][]*rpc.IngestInfo, s.currentIngests.Size())
	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		gMap[key.workload] = append(gMap[key.workload], ig.response())
		return true
	})

	workloadInfos := s.getInfosForWorkloads(nss, iMap, gMap, sMap, filter)
	return &rpc.WorkloadInfoSnapshot{Workloads: workloadInfos}, nil
}

func (s *session) remainLoop(c context.Context) error {
	ticker := time.NewTicker(60 * time.Second)
	defer func() {
		ticker.Stop()
		c = dcontext.WithoutCancel(c)
		c, cancel := context.WithTimeout(c, 3*time.Second)
		defer cancel()
		if _, err := s.managerClient.Depart(c, s.SessionInfo()); err != nil {
			dlog.Errorf(c, "failed to depart from manager: %v", err)
		} else {
			// Depart succeeded, so the traffic-manager has dropped the session. We should too
			if err = deleteSessionInfoFromUserCache(c, s.daemonID); err != nil {
				dlog.Errorf(c, "failed to delete session from user cache: %v", err)
			}
		}
		// Call Close() in separate go-routine because it might block.
		go s.managerConn.Close()
	}()

	for {
		select {
		case <-c.Done():
			return nil
		case <-ticker.C:
			if err := s.remain(c); err != nil {
				return err
			}
		}
	}
}

func (s *session) UpdateStatus(cri userd.ConnectRequest) *rpc.ConnectInfo {
	cr := cri.Request()
	c, config, err := client.DaemonKubeconfig(s.context, cr)
	if err != nil {
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}

	if !cr.IsPodDaemon {
		envEQ := true
		for k, v := range cr.Environment {
			if k[0] == '-' {
				if _, ok := os.LookupEnv(k[:1]); ok {
					envEQ = false
					break
				}
			} else {
				if ov, ok := os.LookupEnv(k); !ok || ov != v {
					envEQ = false
					break
				}
			}
		}
		if !(envEQ && s.ContextServiceAndFlagsEqual(config)) {
			return &rpc.ConnectInfo{
				Error:            rpc.ConnectInfo_MUST_RESTART,
				ClusterContext:   s.Context,
				ClusterServer:    s.Server,
				ManagerInstallId: s.GetManagerInstallId(c),
			}
		}
	}

	namespaces := cr.MappedNamespaces
	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	}
	if len(namespaces) == 0 {
		namespaces = client.GetConfig(c).Cluster().MappedNamespaces
	}

	if s.SetMappedNamespaces(c, namespaces) {
		if len(namespaces) == 0 && k8sapi.CanWatchNamespaces(c) {
			s.StartNamespaceWatcher(c)
		}
	}
	s.subnetViaWorkloads = cr.SubnetViaWorkloads
	return s.Status()
}

func (s *session) Status() *rpc.ConnectInfo {
	return s.status(false)
}

func (s *session) status(initial bool) *rpc.ConnectInfo {
	cfg := s.Kubeconfig
	c := s.context
	ret := &rpc.ConnectInfo{
		ClusterContext:   cfg.Context,
		ClusterServer:    cfg.Server,
		ManagerInstallId: s.GetManagerInstallId(c),
		SessionInfo:      s.SessionInfo(),
		ConnectionName:   s.daemonID.Name,
		KubeFlags:        s.OriginalFlagMap,
		Namespace:        s.Namespace,
		Ingests:          s.getCurrentIngests(),
		Intercepts:       &manager.InterceptInfoSnapshot{Intercepts: s.getCurrentInterceptInfos()},
		ManagerVersion: &manager.VersionInfo2{
			Name:    s.managerName,
			Version: "v" + s.managerVersion.String(),
		},
		ManagerNamespace:   k8s.GetManagerNamespace(c),
		SubnetViaWorkloads: s.subnetViaWorkloads,
		Version: &common.VersionInfo{
			ApiVersion: client.APIVersion,
			Version:    client.Version(),
			Executable: client.GetExe(c),
			Name:       client.DisplayName,
		},
	}
	if !initial {
		ret.Error = rpc.ConnectInfo_ALREADY_CONNECTED
	}
	if len(s.MappedNamespaces) > 0 || len(client.GetConfig(c).Cluster().MappedNamespaces) > 0 {
		ret.MappedNamespaces = s.GetCurrentNamespaces(true)
	}
	var err error
	ret.DaemonStatus, err = s.rootDaemon.Status(c, &empty.Empty{})
	if err != nil {
		return connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
	}
	return ret
}

// Uninstall one or all traffic-agents from the cluster if the client has sufficient credentials to do so.
//
// Uninstalling all or specific agents require that the client can get and update the agents ConfigMap.
func (s *session) Uninstall(ur *rpc.UninstallRequest) (*common.Result, error) {
	_, err := s.managerClient.UninstallAgents(s.context, &manager.UninstallAgentsRequest{
		SessionInfo: s.sessionInfo,
		Agents:      ur.Agents,
	})
	return errcat.ToResult(err), nil
}

func (s *session) getNetworkInfo(ctx context.Context, cr *rpc.ConnectRequest) *rootdRpc.NetworkConfig {
	cfg := client.GetConfig(ctx)
	jsonCfg, _ := client.MarshalJSON(cfg)
	return &rootdRpc.NetworkConfig{
		Session:            s.sessionInfo,
		ClientConfig:       jsonCfg,
		HomeDir:            homedir.HomeDir(),
		Namespace:          s.Namespace,
		SubnetViaWorkloads: s.subnetViaWorkloads,
		KubeFlags:          cr.KubeFlags,
		KubeconfigData:     cr.KubeconfigData,
	}
}

func (s *session) connectRootDaemon(ctx context.Context, nc *rootdRpc.NetworkConfig, wg *sync.WaitGroup, isPodDaemon bool) (rd rootdRpc.DaemonClient, err error) {
	// establish a connection to the root daemon gRPC grpcService
	dlog.Info(ctx, "Connecting to root daemon...")
	svc := userd.GetService(ctx)
	if svc.RootSessionInProcess() {
		// Just run the root session in-process.
		_, rootSession, err := rootd.NewInProcSession(ctx, nc, s.managerClient, s.managerVersion, isPodDaemon)
		if err != nil {
			return nil, err
		}
		g := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
		if err = rootSession.Start(ctx, g, svc.TeleroutePort()); err != nil {
			return nil, err
		}
		rd = rootSession

		// Give in-proc root session services a chance to clean up.
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := g.Wait()
			if err != nil && !errors.Is(err, context.Canceled) {
				dlog.Errorf(ctx, "root session exited with error: %v", err)
			}
		}()
	} else {
		var conn *grpc.ClientConn
		conn, err = socket.Dial(ctx, socket.RootDaemonPath(ctx), true)
		if err != nil {
			return nil, fmt.Errorf("unable open root daemon socket: %w", err)
		}
		defer func() {
			if err != nil {
				conn.Close()
			}
		}()
		rd = rootdRpc.NewDaemonClient(conn)

		tmTimeout := client.GetConfig(ctx).Timeouts().Get(client.TimeoutTrafficManagerConnect)
		for attempt := 1; ; attempt++ {
			var rootStatus *rootdRpc.DaemonStatus
			tCtx, tCancel := context.WithTimeout(ctx, tmTimeout/2)
			rootStatus, err = rd.Connect(tCtx, nc)
			tCancel()
			if err != nil {
				return nil, fmt.Errorf("failed to connect to root daemon: %w", err)
			}
			oc := rootStatus.OutboundConfig
			if oc == nil || oc.Session == nil {
				// This is an internal error. Something is wrong with the root daemon.
				return nil, errors.New("root daemon's OutboundConfig has no Session")
			}
			if oc.Session.SessionId == nc.Session.SessionId {
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
	ctx, cancel := client.GetConfig(ctx).Timeouts().TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
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

func (s *session) eachWorkload(namespaces []string, do func(kind manager.WorkloadInfo_Kind, name, namespace string, info workloadInfo)) {
	s.workloadsLock.Lock()
	for _, ns := range namespaces {
		if workloads, ok := s.workloads[ns]; ok {
			for key, info := range workloads {
				do(key.kind, key.name, ns, info)
			}
		}
	}
	s.workloadsLock.Unlock()
}

func (s *session) RerouteLocalPort(ap types.AddrPortProto, srcPort uint16) {
	fw := forwarder.NewInterceptor(types.PortAndProto{
		Port:  srcPort,
		Proto: ap.Proto,
	}, tunnel.ClientToAgent, ap.AddrPort)

	go func() {
		ctx := dgroup.WithGoroutineName(s.context, fmt.Sprintf("/%d=>%s", srcPort, ap))
		err := fw.Serve(ctx, nil)
		if err != nil && ctx.Err() == nil {
			dlog.Errorf(ctx, "port-forwarder failed with %v", err)
		}
	}()
}

func (s *session) workloadsWatcher(ctx context.Context, namespace string, synced *sync.WaitGroup) error {
	defer func() {
		if synced != nil {
			synced.Done()
		}
	}()
	return watcher.WatchWithRetry(ctx, "WatchAgentPods", client.GetConfig(ctx).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.WorkloadEventsDelta], error) {
			return s.managerClient.WatchWorkloads(ctx, &manager.WorkloadEventsRequest{SessionInfo: s.sessionInfo, Namespace: namespace})
		},
		func(wls *manager.WorkloadEventsDelta) error {
			s.workloadsLock.Lock()
			workloads, ok := s.workloads[namespace]
			if !ok {
				workloads = make(map[workloadInfoKey]workloadInfo)
				s.workloads[namespace] = workloads
			}

			for _, we := range wls.GetEvents() {
				w := we.Workload
				key := workloadInfoKey{kind: w.Kind, name: w.Name}
				if we.Type == manager.WorkloadEvent_DELETED {
					dlog.Debugf(ctx, "Deleting workload %s/%s.%s", key.kind, key.name, namespace)
					delete(workloads, key)
				} else {
					var clients []string
					if lc := len(w.InterceptClients); lc > 0 {
						clients = make([]string, lc)
						for i, ic := range w.InterceptClients {
							clients[i] = ic.Client
						}
					}
					state := workload.StateFromRPC(w.State)
					dlog.Debugf(ctx, "Adding workload %s/%s.%s %s %s %s", key.kind, key.name, namespace, state, w.AgentState, clients)
					workloads[key] = workloadInfo{
						uid:              k8sTypes.UID(w.Uid),
						state:            state,
						agentState:       w.AgentState,
						interceptClients: clients,
					}
				}
			}
			for _, subscriber := range s.workloadSubscribers {
				select {
				case subscriber <- struct{}{}:
				default:
				}
			}
			s.workloadsLock.Unlock()
			if synced != nil {
				synced.Done()
				synced = nil
			}
			return nil
		}, nil)
}
