package trafficmgr

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/user"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tmconfig"
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
	service            userd.Service
	rootDaemon         rootdRpc.DaemonClient
	subnetViaWorkloads []*rootdRpc.SubnetViaWorkload

	// local information
	installID string // telepresence's install ID
	clientID  string // "laptop-username@laptop-hostname"

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

	// Synthetic IPs are generated when the targetIP is a hostname, so that we can defer the
	// lookup of that host until the time when it is dialed.
	syntheticIPs map[netip.Addr]string

	// lastActivity is set when something happens to the session that counts as proof that the
	// client is in use. All gRPC calls involving a session will update this timestamp, and
	// the root daemon will also update this timestamp when a new TCP tunnel is created to
	// a traffic-agent. The root daemon will not update this timestamp when resolving DNS
	// calls because they often arrive sporadically due to activity that isn't related to
	// Telepresence at all.
	lastActivity int64
}

func (s *session) RevokeIntercept(ctx context.Context, interceptID string) error {
	return tmconfig.AddCommand(s, k8s.GetManagerNamespace(ctx), tmconfig.AdminCommand{
		Name:      tmconfig.RemoveIntercept,
		Args:      []string{interceptID},
		Timestamp: time.Now().UnixNano(),
	})
}

func NewSession(
	service userd.Service,
	ctx context.Context,
	cr *rpc.ConnectRequest,
	config *k8s.Kubeconfig,
	wg *sync.WaitGroup,
) (session userd.Session, info *rpc.ConnectInfo, err error) {
	clog.Info(config, "-- Starting new session")

	clog.Infof(config, "Connecting to k8s context %s (%s) ...", config.KubeContext, config.Server)

	cluster, err := k8s.ConnectCluster(cr, config)
	if err != nil {
		clog.Errorf(config, "unable to track k8s cluster: %+v", err)
		return nil, nil, err
	}
	clog.Infof(cluster, "Connected to context %s, namespace %s (%s)", cluster.KubeContext, cluster.Namespace, cluster.Server)

	clog.Info(cluster, "Connecting to traffic manager...")
	installID, err := client.InstallID(cluster)
	if err != nil {
		return nil, nil, err
	}
	cfg := client.GetConfig(cluster)
	tos := cfg.Timeouts()
	ctx, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	tmgr, err := connectMgr(ctx, service, cluster, installID, cr)
	if err != nil {
		clog.Errorf(config, "Unable to connect to session: %s", err)
		return nil, nil, err
	}
	if tmgr.compareFinalizedManagerVersion(2, 21, 0) < 0 {
		return nil, nil,
			fmt.Errorf("traffic manager version %s is too old. Minimum supported version is 2.21.0, please upgrade", tmgr.managerVersion)
	}
	tmgr.updateClientConfig(ctx, cr.MappedNamespaces)

	oi := tmgr.getNetworkInfo(cr)
	if !service.RootSessionInProcess() {
		// Root daemon needs this to authenticate with the cluster. Potential exec configurations in the kubeconfig
		// must be executed by the user, not by root.
		oi.KubeconfigData, err = patcher.CreateExternalKubeConfig(tmgr.Context, config.ClientConfig, tmgr.KubeContext, func([]string) (string, string, string, error) {
			return client.GetExe(tmgr), service.ListenerAddress().String(), client.GetConfigFile(tmgr), nil
		}, nil)
		if err != nil {
			return nil, nil, err
		}
	}

	tmgr.Context = tunnel.WithSyntheticIPResolver(tmgr.Context, tmgr)

	tmgr.rootDaemon, err = tmgr.connectRootDaemon(ctx, oi, wg, cr.IsPodDaemon)
	if err != nil {
		tmgr.managerConn.Close()
		return nil, nil, err
	}

	// Collect data on how long connection time took
	clog.Debug(tmgr, "Finished connecting to traffic manager")

	tmgr.AddNamespaceEventHandler(tmgr.updateDaemonNamespaces)
	ci, err := tmgr.status(ctx, true)
	return tmgr, ci, err
}

func (s *session) GetService() userd.Service {
	return s.service
}

// Run (1) starts up with ensuring that the manager is installed and running,
// but then for most of its life
//   - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//   - run the intercepts (manager.WatchIntercepts) and then
//   - (3) listen on the appropriate local ports and forward them to the intercepted
//     Services, and
//   - (4) mount the appropriate remote volumes.
func (s *session) Run() {
	g := log.NewGroup(s)
	defer func() {
		_ = s.WithRootClient(context.WithoutCancel(s), func(ctx context.Context, rd rootdRpc.DaemonClient) error {
			_, _ = rd.Disconnect(ctx, &empty.Empty{})
			return nil
		})
		clog.Info(s, "-- session ended")
	}()
	s.startServices(g)
	err := g.Wait()
	if err != nil {
		clog.Errorf(s, "session ended with error: %v", err)
	}
}

func (s *session) WithRootClient(ctx context.Context, f func(context.Context, rootdRpc.DaemonClient) error) error {
	return f(ctx, s.rootDaemon)
}

func (s *session) ManagerClient() manager.ManagerClient {
	return manager.NewManagerClient(s.managerConn)
}

func (s *session) ManagerName() string {
	return s.managerName
}

func (s *session) ManagerVersion() semver.Version {
	return s.managerVersion
}

func (s *session) MarkActivity() {
	atomic.StoreInt64(&s.lastActivity, time.Now().UnixNano())
}

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(
	timeoutCtx context.Context,
	service userd.Service,
	cluster *k8s.Cluster,
	installID string,
	cr *rpc.ConnectRequest,
) (*session, error) {
	cfg := client.GetConfig(cluster)
	mgrNs := k8s.GetManagerNamespace(cluster)
	conn, managerName, managerVersion, err := cluster.ConnectToManager(timeoutCtx, mgrNs)
	if err != nil {
		return nil, err
	}
	if sdc := cfg.Grpc().SimulateDisconnect; sdc > 0 {
		time.AfterFunc(sdc, func() {
			clog.Info(cluster, "Simulated disconnect from manager")
			conn.Close()
		})
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

	daemonID := daemon.NewIdentifier(cr.Name, cluster.KubeContext, cluster.Namespace, proc.RunningInContainer())
	si, err := LoadSessionInfoFromUserCache(cluster, daemonID)
	if err != nil {
		return nil, err
	}

	mClient := manager.NewManagerClient(conn)
	if si != nil {
		// Check if the session is still valid in the traffic-manager by calling Remain
		_, err = mClient.Remain(timeoutCtx, &manager.RemainRequest{Session: si})
		if err == nil {
			if timeoutCtx.Err() != nil {
				// Call timed out, so the traffic-manager isn't responding at all
				return nil, timeoutCtx.Err()
			}
			clog.Debugf(cluster, "traffic-manager port-forward established, client was already known to the traffic-manager as %q", clientID)
		} else {
			si = nil
		}
	}

	if si == nil {
		clog.Debugf(cluster, "traffic-manager port-forward established, making client known to the traffic-manager as %q", clientID)
		si, err = mClient.ArriveAsClient(timeoutCtx, &manager.ClientInfo{
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
			return nil, err
		}
		if err = saveSessionInfoToUserCache(timeoutCtx, daemonID, si); err != nil {
			return nil, err
		}
	}

	sess := &session{
		Cluster:            cluster,
		service:            service,
		installID:          installID,
		daemonID:           daemonID,
		clientID:           clientID,
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
	}
	sess.Context = withSession(sess.Context, sess)
	return sess, nil
}

func (s *session) reconnectManager() (returnedErr error) {
	cfg := client.GetConfig(s)
	tos := cfg.Timeouts()
	tc, cancel := tos.TimeoutContext(s, client.TimeoutTrafficManagerConnect)
	defer cancel()

	conn, managerName, managerVersion, err := s.ConnectToManager(tc, k8s.GetManagerNamespace(s))
	if err != nil {
		return err
	}
	defer func() {
		if returnedErr != nil {
			conn.Close()
		}
	}()

	_, err = manager.NewManagerClient(conn).ReconnectClient(tc, &manager.ReconnectClientRequest{
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

	s.managerConn = conn
	s.managerName = managerName
	s.managerVersion = managerVersion
	return nil
}

func (s *session) remain() error {
	ctx, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutTrafficManagerAPI)
	defer cancel()
	var lastActivity *timestamppb.Timestamp
	ln := atomic.LoadInt64(&s.lastActivity)
	if ln != 0 {
		lastActivity = timestamppb.New(time.Unix(0, ln))
	}
	_, err := s.ManagerClient().Remain(ctx, &manager.RemainRequest{
		Session:      s.SessionInfo(),
		LastActivity: lastActivity,
	})
	if err != nil {
		clog.Errorf(ctx, "error calling Remain: %v", client.CheckTimeout(ctx, err))
	}
	return nil
}

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (s *session) updateDaemonNamespaces() {
	const svcDomain = "svc"

	domains := s.GetCurrentNamespaces(false)
	if !slices.Contains(domains, svcDomain) {
		domains = append(domains, svcDomain)
	}
	clog.Debugf(s, "posting top-level domains %v to root daemon", domains)

	err := s.WithRootClient(s, func(ctx context.Context, rd rootdRpc.DaemonClient) (err error) {
		_, err = rd.SetDNSTopLevelDomains(ctx, &rootdRpc.Domains{Domains: domains})
		return err
	})
	if err != nil {
		clog.Errorf(s, "error posting domains %v to root daemon: %v", domains, err)
	}
	clog.Debug(s, "domains posted successfully")
}

func (s *session) startServices(g log.Group) {
	g.Go("remain", s.remainLoop)
	g.Go("agents", s.watchAgentsLoop)
	g.Go("intercept-port-forward", s.watchInterceptsHandler)
}

func runWithRetry(ctx context.Context, f func(context.Context) error) error {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		if err := f(ctx); err != nil {
			clog.Error(ctx, err)
			time.Sleep(backoff)
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

func (s *session) SessionInfo() *manager.SessionInfo {
	return s.sessionInfo
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
		clog.Debugf(s, "filter %d, filterMatch %d", filter, filterMatch)
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
	for _, ns := range namespaces {
		s.workloadsLock.Lock()
		_, ok := s.workloads[ns]
		s.workloadsLock.Unlock()
		if ok {
			wg.Done()
		} else {
			go func() {
				err := s.workloadsWatcher(ns, &wg)
				if err != nil {
					clog.Errorf(s, "error ensuring watcher for namespace %s: %v", ns, err)
					return
				}
			}()
			clog.Debugf(s, "watcher for namespace %s started", ns)
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
		clog.Debug(s, "No namespaces are mapped")
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

func (s *session) remainLoop(ctx context.Context) error {
	ticker := time.NewTicker(client.GetConfig(ctx).Grpc().PingInterval)
	defer func() {
		ticker.Stop()
		c, cancel := context.WithTimeout(context.WithoutCancel(s), 3*time.Second)
		defer cancel()
		if _, err := s.ManagerClient().Depart(c, s.SessionInfo()); err != nil {
			clog.Errorf(c, "failed to depart from manager: %v", err)
		} else {
			// Depart succeeded, so the traffic-manager has dropped the session. We should too
			if err = deleteSessionInfoFromUserCache(c, s.daemonID); err != nil {
				clog.Errorf(c, "failed to delete session from user cache: %v", err)
			}
		}
		// Call Close() in separate go-routine because it might block.
		go s.managerConn.Close()
	}()

	for {
		select {
		case <-s.Done():
			return nil
		case <-ticker.C:
			if err := s.remain(); err != nil {
				return err
			}
		}
	}
}

// CheckStatus checks that the given ConnectRequest is aligned with the current status of the session.
// If the request is not aligned, it returns a ConnectInfo with the error code set to MUST_RESTART.
// If the request is aligned, it returns nil.
func (s *session) CheckStatus(cr *rpc.ConnectRequest) error {
	config, err := k8s.DaemonKubeconfig(s, cr)
	if err != nil {
		return err
	}
	if len(cr.MappedNamespaces) == 1 && cr.MappedNamespaces[0] == "all" {
		cr.MappedNamespaces = nil
	}
	// If namespaces are specified in the request, then we must ensure that they are the same as the current ones
	// because the request takes precedence over namespaces configured in the client configuration or by the traffic-manager.
	if len(cr.MappedNamespaces) == 0 || slices.Equal(cr.MappedNamespaces, s.MappedNamespaces) {
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
		if envEQ && s.ContextServiceAndFlagsEqual(config) && s.subnetViaWorkloadsEqual(cr.SubnetViaWorkloads) {
			return nil
		}
	}
	return errcat.User.New("Cluster configuration changed, please quit telepresence and reconnect")
}

func (s *session) subnetViaWorkloadsEqual(workloads []*rootdRpc.SubnetViaWorkload) bool {
	return slices.EqualFunc(s.subnetViaWorkloads, workloads, func(a, b *rootdRpc.SubnetViaWorkload) bool {
		return proto.Equal(a, b)
	})
}

// UpdateStatus checks if the given ConnectRequest matches the current status of the session. If it does, then the status of the session is updated
// to match the current client configuration and the traffic-manager.
// If the request is not aligned with the current status of the session, it returns a ConnectInfo with the error code set to MUST_RESTART.
// If the request is aligned, it returns the current status.
func (s *session) UpdateStatus(ctx context.Context, cr *rpc.ConnectRequest) (*rpc.ConnectInfo, error) {
	err := s.CheckStatus(cr)
	if err != nil {
		return nil, err
	}
	s.updateClientConfig(ctx, cr.MappedNamespaces)
	return s.Status(ctx)
}

func (s *session) Status(ctx context.Context) (*rpc.ConnectInfo, error) {
	return s.status(ctx, false)
}

func (s *session) updateClientConfig(ctx context.Context, namespaces []string) {
	var tmCfg client.Config
	cliCfg, err := s.ManagerClient().GetClientConfig(ctx, &empty.Empty{})
	if err != nil {
		if status.Code(err) != codes.Unimplemented {
			clog.Warnf(s, "Failed to get remote config from traffic manager: %v", err)
		}
	} else {
		tmCfg, err = client.ParseConfigYAML(ctx, "client configuration from cluster", cliCfg.ConfigYaml)
		if err != nil {
			clog.Warn(s, err.Error())
		}
	}

	// Merge traffic-manager's reported config, but get priority to the local config.
	cfg := client.GetConfig(s)
	if tmCfg != nil {
		tmMappedNamespaces := tmCfg.Cluster().MappedNamespaces
		clientMappedNamespaces := cfg.Cluster().MappedNamespaces
		cfg = tmCfg.Merge(cfg)

		// We do not want to override the local config with the traffic-manager's config even if the local config is empty.
		cfg.Cluster().MappedNamespaces = clientMappedNamespaces
		switch {
		case len(namespaces) > 0:
			// Use the namespaces specified by the user.
		case len(clientMappedNamespaces) > 0:
			// Use the namespaces specified by the client configuration.
			namespaces = clientMappedNamespaces
		case len(tmMappedNamespaces) > 0:
			// Use the namespaces specified by the traffic-manager.
			namespaces = tmMappedNamespaces
		}
		if s.SetMappedNamespaces(namespaces) {
			if len(namespaces) == 0 {
				if k8sapi.CanWatchNamespaces(s) {
					clog.Infof(s, "Will watch all namespaces")
					s.StartNamespaceWatcher()
				} else {
					clog.Warnf(s, "Unable to watch all namespaces")
				}
			} else {
				clog.Infof(s, "Will use mapped namespaces %s", namespaces)
			}
		}
		rt := cfg.Routing()
		rt.NeverProxy = subnet.Unique(append(rt.NeverProxy, tmCfg.Routing().NeverProxy...))
		client.ReplaceConfig(s, cfg)
	}
	if clog.Enabled(s, slog.LevelDebug) {
		clog.Debug(s, "Client configuration")
		buf, _ := json.Marshal(cfg)
		buf, _ = yaml.JSONToYAML(buf)
		sc := bufio.NewScanner(bytes.NewReader(buf))
		for sc.Scan() {
			clog.Debug(s, sc.Text())
		}
	}
}

func (s *session) status(ctx context.Context, initial bool) (*rpc.ConnectInfo, error) {
	cfg := s.Kubeconfig
	ret := &rpc.ConnectInfo{
		Initial:          initial,
		ClusterContext:   cfg.KubeContext,
		ClusterServer:    cfg.Server,
		ManagerInstallId: s.GetManagerInstallId(),
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
		ManagerNamespace:   k8s.GetManagerNamespace(s),
		SubnetViaWorkloads: s.subnetViaWorkloads,
		Version:            client.VersionInfo(s),
	}
	if len(s.MappedNamespaces) > 0 || len(client.GetConfig(s).Cluster().MappedNamespaces) > 0 {
		ret.MappedNamespaces = s.GetCurrentNamespaces(true)
	}
	err := s.WithRootClient(ctx, func(ctx context.Context, rd rootdRpc.DaemonClient) (err error) {
		ret.DaemonStatus, err = rd.Status(s, &empty.Empty{})
		return err
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// Uninstall one or all traffic-agents from the cluster if the client has sufficient credentials to do so.
//
// Uninstalling all or specific agents require that the client can get and update the agents ConfigMap.
func (s *session) Uninstall(ctx context.Context, ur *rpc.UninstallRequest) error {
	_, err := s.ManagerClient().UninstallAgents(ctx, &manager.UninstallAgentsRequest{
		SessionInfo: s.sessionInfo,
		Agents:      ur.Agents,
	})
	return err
}

func (s *session) getNetworkInfo(cr *rpc.ConnectRequest) *rootdRpc.NetworkConfig {
	cfg := client.GetConfig(s)
	jsonCfg, _ := json.Marshal(cfg)
	return &rootdRpc.NetworkConfig{
		KubeFlags:          cr.KubeFlags,
		KubeconfigData:     cr.KubeconfigData,
		Namespace:          s.Namespace,
		ManagerNamespace:   k8s.GetManagerNamespace(s),
		MappedNamespaces:   s.GetCurrentNamespaces(true),
		Session:            s.sessionInfo,
		SubnetViaWorkloads: s.subnetViaWorkloads,
		HomeDir:            homedir.HomeDir(),
		ClientConfig:       jsonCfg,
	}
}

func (s *session) connectRootDaemon(timeoutCtx context.Context, nc *rootdRpc.NetworkConfig, wg *sync.WaitGroup, isPodDaemon bool) (rd rootdRpc.DaemonClient, err error) {
	// establish a connection to the root daemon gRPC grpcService
	clog.Info(s, "Connecting to root daemon...")
	svc := s.GetService()
	if svc.RootSessionInProcess() {
		// Just run the root session in-process.
		activity := make(chan time.Time)
		defer close(activity)
		rootSession, err := rootd.NewInProcSession(s.Cluster, nc, s.managerConn, s.managerVersion, activity, isPodDaemon)
		if err != nil {
			return nil, err
		}
		go func() {
			for {
				select {
				case <-s.Done():
					return
				case ats, ok := <-activity:
					if !ok {
						return
					}
					clog.Debugf(s, "root session last activity: %v", ats)
					atomic.StoreInt64(&s.lastActivity, ats.UnixNano())
				}
			}
		}()

		g := log.NewGroup(rootSession)
		if err = rootSession.Start(g, svc.TeleroutePort()); err != nil {
			return nil, err
		}
		rd = rootSession

		// Give in-proc root session services a chance to clean up.
		wg.Go(func() {
			err := g.Wait()
			if err != nil && !errors.Is(err, context.Canceled) {
				clog.Errorf(s, "root session exited with error: %v", err)
			}
		})
	} else {
		var conn *grpc.ClientConn
		conn, err = daemon.DialRootDaemon(timeoutCtx, true)
		if err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				conn.Close()
			}
		}()
		rd = rootdRpc.NewDaemonClient(conn)

		for attempt := 1; ; attempt++ {
			var rootStatus *rootdRpc.DaemonStatus
			rootStatus, err = rd.Connect(timeoutCtx, nc)
			if err != nil {
				return nil, fmt.Errorf("failed to connect to root daemon: %w", err)
			}
			oc := rootStatus.OutboundConfig
			if oc == nil || oc.Session == nil {
				// This is an internal error. Something is wrong with the root daemon.
				return nil, errors.New("root daemon's OutboundConfig has no session")
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
			if _, err = rd.Disconnect(s, &empty.Empty{}); err != nil {
				return nil, fmt.Errorf("failed to disconnect from the root daemon: %w", err)
			}
		}
		aw, err := rd.ActivityWatcher(s, &empty.Empty{})
		if err != nil {
			return nil, fmt.Errorf("failed to get activity watcher: %w", err)
		}
		go func() {
			for {
				at, err := aw.Recv()
				if err != nil {
					clog.Errorf(s, "activity watcher failed: %v", err)
					return
				}
				ats := at.Activity.AsTime()
				clog.Debugf(s, "root session last activity: %v", ats)
				atomic.StoreInt64(&s.lastActivity, ats.UnixNano())
			}
		}()
	}

	// The root daemon needs time to set up the TUN-device and DNS, which involves interacting
	// with the cluster-side traffic-manager.
	if _, err = rd.WaitForNetwork(timeoutCtx, &empty.Empty{}); err != nil {
		if se, ok := status.FromError(err); ok {
			err = se.Err()
		}
		return nil, fmt.Errorf("failed to connect to root daemon: %v", err)
	}
	clog.Debug(s, "Connected to root daemon")
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
	fw := forwarder.New(types.PortAndProto{
		Port:  srcPort,
		Proto: ap.Proto,
	}, tunnel.ClientToAgent, ap.AddrPort)

	go func() {
		ctx := clog.WithGroup(s, fmt.Sprintf("%d=>%s", srcPort, ap))
		err := fw.Serve(ctx, nil)
		if err != nil && ctx.Err() == nil {
			clog.Errorf(ctx, "port-forwarder failed with %v", err)
		}
	}()
}

func (s *session) workloadsWatcher(namespace string, synced *sync.WaitGroup) error {
	defer func() {
		if synced != nil {
			synced.Done()
		}
	}()
	return watcher.WatchWithRetry(s, "WatchWorkloads", client.GetConfig(s).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.WorkloadEventsDelta], error) {
			return s.ManagerClient().WatchWorkloads(ctx, &manager.WorkloadEventsRequest{SessionInfo: s.sessionInfo, Namespace: namespace})
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
					clog.Debugf(s, "Deleting workload %s/%s.%s", key.kind, key.name, namespace)
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
					clog.Debugf(s, "Adding workload %s/%s.%s %s %s %s", key.kind, key.name, namespace, state, w.AgentState, clients)
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
