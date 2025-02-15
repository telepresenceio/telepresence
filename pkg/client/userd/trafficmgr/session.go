package trafficmgr

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
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
	uid              types.UID
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
	currentIngests *xsync.MapOf[ingestKey, *ingest]

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

	ingressInfo []*manager.IngressInfo

	isPodDaemon bool

	// done is closed when the session ends
	done chan struct{}

	// Possibly extended version of the session. Use when calling interface methods.
	self userd.Session
}

func NewSession(
	ctx context.Context,
	cri userd.ConnectRequest,
	config *client.Kubeconfig,
) (rc context.Context, _ userd.Session, info *connector.ConnectInfo) {
	dlog.Info(ctx, "-- Starting new session")

	cr := cri.Request()
	connectStart := time.Now()
	defer func() {
		if info.Error == connector.ConnectInfo_UNSPECIFIED {
			scout.Report(ctx, "connect",
				scout.Entry{
					Key:   "time_to_connect",
					Value: time.Since(connectStart).Seconds(),
				}, scout.Entry{
					Key:   "mapped_namespaces",
					Value: len(cr.MappedNamespaces),
				})
		} else {
			scout.Report(ctx, "connect_error",
				scout.Entry{
					Key:   "error",
					Value: info.ErrorText,
				}, scout.Entry{
					Key:   "error_type",
					Value: info.Error.String(),
				}, scout.Entry{
					Key:   "error_category",
					Value: info.ErrorCategory,
				}, scout.Entry{
					Key:   "time_to_fail",
					Value: time.Since(connectStart).Seconds(),
				}, scout.Entry{
					Key:   "mapped_namespaces",
					Value: len(cr.MappedNamespaces),
				})
		}
	}()

	dlog.Info(ctx, "Connecting to k8s cluster...")
	ctx, cluster, err := k8s.ConnectCluster(ctx, cr, config)
	if err != nil {
		dlog.Errorf(ctx, "unable to track k8s cluster: %+v", err)
		return ctx, nil, connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	dlog.Infof(ctx, "Connected to context %s, namespace %s (%s)", cluster.Context, cluster.Namespace, cluster.Server)
	ctx = portforward.WithRestConfig(ctx, cluster.Kubeconfig.RestConfig)

	ctx = cluster.WithJoinedClientSetInterface(ctx)

	dlog.Info(ctx, "Connecting to traffic manager...")
	installID, err := client.InstallID(ctx)
	if err != nil {
		return ctx, nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}
	tmgr, err := connectMgr(ctx, cluster, installID, cr)
	if err != nil {
		dlog.Errorf(ctx, "Unable to connect to session: %s", err)
		return ctx, nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}

	// store session in ctx for reporting
	ctx = scout.WithSession(ctx, tmgr)

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
	}
	if err = tmgr.ApplyConfig(ctx); err != nil {
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
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
		if !rootRunning {
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, errors.New("root daemon is not running"))
		}

		if client.GetConfig(ctx).Cluster().ConnectFromRootDaemon {
			// Root daemon needs this to authenticate with the cluster. Potential exec configurations in the kubeconfig
			// must be executed by the user, not by root.
			konfig, err := patcher.CreateExternalKubeConfig(ctx, config.ClientConfig, cluster.Context, func([]string) (string, string, error) {
				return client.GetExe(ctx), userd.GetService(ctx).ListenerAddress(ctx), nil
			}, nil)
			if err != nil {
				return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
			}
			patcher.AnnotateNetworkConfig(ctx, oi, konfig.CurrentContext)
		}
	}

	tmgr.rootDaemon, err = tmgr.connectRootDaemon(ctx, oi, cr.IsPodDaemon)
	if err != nil {
		tmgr.managerConn.Close()
		return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
	}

	// Collect data on how long connection time took
	dlog.Debug(ctx, "Finished connecting to traffic manager")

	tmgr.AddNamespaceListener(ctx, tmgr.updateDaemonNamespaces)
	return ctx, tmgr, tmgr.status(ctx, true)
}

// SetSelf is for internal use by extensions.
func (s *session) SetSelf(self userd.Session) {
	s.self = self
}

// RunSession (1) starts up with ensuring that the manager is installed and running,
// but then for most of its life
//   - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//   - run the intercepts (manager.WatchIntercepts) and then
//   - (3) listen on the appropriate local ports and forward them to the intercepted
//     Services, and
//   - (4) mount the appropriate remote volumes.
func (s *session) RunSession(c context.Context) error {
	self := s.self
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	defer func() {
		self.Epilog(c)
	}()
	self.StartServices(g)
	return g.Wait()
}

func (s *session) RootDaemon() rootdRpc.DaemonClient {
	return s.rootDaemon
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

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(
	ctx context.Context,
	cluster *k8s.Cluster,
	installID string,
	cr *rpc.ConnectRequest,
) (*session, error) {
	tos := client.GetConfig(ctx).Timeouts()

	ctx, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	mgrNs := k8s.GetManagerNamespace(ctx)
	err := CheckTrafficManagerService(ctx, mgrNs)
	if err != nil {
		return nil, err
	}

	conn, mClient, vi, err := k8sclient.ConnectToManager(ctx, mgrNs)
	if err != nil {
		return nil, err
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

	daemonID, err := daemon.NewIdentifier(cr.Name, cluster.Context, cluster.Namespace, proc.RunningInContainer())
	if err != nil {
		return nil, err
	}
	si, err := LoadSessionInfoFromUserCache(ctx, daemonID)
	if err != nil {
		return nil, err
	}

	svc := userd.GetService(ctx)
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
		if err = SaveSessionInfoToUserCache(ctx, daemonID, si); err != nil {
			return nil, err
		}
	}

	var opts []grpc.CallOption
	cfg := client.GetConfig(ctx)
	if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
		opts = append(opts, grpc.MaxCallRecvMsgSize(int(mz)))
	}
	svc.SetManagerClient(mClient, opts...)

	managerName := vi.Name
	if managerName == "" {
		// Older traffic-managers doesn't distinguish between OSS and pro versions
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
		currentIngests:     xsync.NewMapOf[ingestKey, *ingest](),
		ingestTracker:      newPodAccessTracker(),
		workloads:          make(map[string]map[workloadInfoKey]workloadInfo),
		interceptWaiters:   make(map[string]*awaitIntercept),
		isPodDaemon:        cr.IsPodDaemon,
		done:               make(chan struct{}),
		subnetViaWorkloads: cr.SubnetViaWorkloads,
	}
	sess.self = sess
	return sess, nil
}

func (s *session) NewRemainRequest() *manager.RemainRequest {
	return &manager.RemainRequest{Session: s.SessionInfo()}
}

func (s *session) Remain(ctx context.Context) error {
	self := s.self
	ctx, cancel := client.GetConfig(ctx).Timeouts().TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer cancel()
	_, err := self.ManagerClient().Remain(ctx, self.NewRemainRequest())
	if err != nil {
		if status.Code(err) == codes.NotFound || status.Code(err) == codes.Unavailable {
			// The session has expired. We need to cancel the owner session and reconnect.
			return ErrSessionExpired
		}
		dlog.Errorf(ctx, "error calling Remain: %v", client.CheckTimeout(ctx, err))
	}
	return nil
}

func CheckTrafficManagerService(ctx context.Context, namespace string) error {
	dlog.Debug(ctx, "checking that traffic-manager exists")
	coreV1 := k8sapi.GetK8sInterface(ctx).CoreV1()
	if _, err := coreV1.Services(namespace).Get(ctx, agentmap.ManagerAppName, meta.GetOptions{}); err != nil {
		msg := fmt.Sprintf("unable to get service %s in %s: %v", agentmap.ManagerAppName, namespace, err)
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

func (s *session) Epilog(ctx context.Context) {
	_, _ = s.rootDaemon.Disconnect(ctx, &empty.Empty{})
	dlog.Info(ctx, "-- Session ended")
	close(s.done)
}

func (s *session) StartServices(g *dgroup.Group) {
	g.Go("remain", s.remainLoop)
	g.Go("agents", s.watchAgentsLoop)
	g.Go("intercept-port-forward", s.watchInterceptsHandler)
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
		} else {
			break
		}
	}
	return nil
}

func (s *session) Done() <-chan struct{} {
	return s.done
}

func (s *session) SessionInfo() *manager.SessionInfo {
	return s.sessionInfo
}

func (s *session) ApplyConfig(ctx context.Context) error {
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
	ctx context.Context,
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

		filterMatch &= ^(rpc.ListRequest_REPLACEMENTS | rpc.ListRequest_INTERCEPTS)
		if wlInfo.InterceptInfos, ok = iMap[name]; ok {
			for _, ii := range wlInfo.InterceptInfos {
				if ii.Spec.NoDefaultPort {
					filterMatch |= rpc.ListRequest_REPLACEMENTS
				} else {
					filterMatch |= rpc.ListRequest_INTERCEPTS
				}
			}
		}
		if wlInfo.IngestInfos, ok = gMap[name]; !ok {
			filterMatch &= ^rpc.ListRequest_INGESTS
		}
		if wlInfo.AgentVersion, ok = sMap[name]; !ok {
			filterMatch &= ^rpc.ListRequest_INSTALLED_AGENTS
		}
		dlog.Debugf(ctx, "filter %d, filterMatch %d", filter, filterMatch)
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

func (s *session) WatchWorkloads(c context.Context, wr *rpc.WatchWorkloadsRequest, stream userd.WatchWorkloadsStream) error {
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
		ws, err := s.WorkloadInfoSnapshot(c, wr.Namespaces, rpc.ListRequest_UNSPECIFIED)
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
		case <-c.Done():
			return nil
		case <-ch:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

func (s *session) ensureWatchers(ctx context.Context,
	namespaces []string,
) {
	managerHasWatcherSupport := s.compareFinalizedManagerVersion(2, 20, 0) > 0

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
				var err error
				if managerHasWatcherSupport {
					err = s.workloadsWatcher(ctx, ns, &wg)
				} else {
					err = s.localWorkloadsWatcher(ctx, ns, &wg)
				}
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
	ctx context.Context,
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
		dlog.Debug(ctx, "No namespaces are mapped")
		return &rpc.WorkloadInfoSnapshot{}, nil
	}
	if len(nss) == 1 && nss[0] == s.Namespace {
		cas := s.getCurrentAgents()
		sMap = make(map[string]string, len(cas))
		for _, a := range cas {
			sMap[a.Name] = a.Version
		}
	}
	s.ensureWatchers(ctx, nss)
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

	workloadInfos := s.getInfosForWorkloads(ctx, nss, iMap, gMap, sMap, filter)
	return &rpc.WorkloadInfoSnapshot{Workloads: workloadInfos}, nil
}

var ErrSessionExpired = errors.New("session expired")

func (s *session) remainLoop(c context.Context) error {
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
			if err = DeleteSessionInfoFromUserCache(c, s.daemonID); err != nil {
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
			if err := s.self.Remain(c); err != nil {
				return err
			}
		}
	}
}

func (s *session) UpdateStatus(c context.Context, cri userd.ConnectRequest) *rpc.ConnectInfo {
	cr := cri.Request()
	c, config, err := client.DaemonKubeconfig(c, cr)
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
		if !(envEQ && s.Kubeconfig.ContextServiceAndFlagsEqual(config)) {
			return &rpc.ConnectInfo{
				Error:            rpc.ConnectInfo_MUST_RESTART,
				ClusterContext:   s.Kubeconfig.Context,
				ClusterServer:    s.Kubeconfig.Server,
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
		s.currentInterceptsLock.Lock()
		s.ingressInfo = nil
		s.currentInterceptsLock.Unlock()
	}
	s.subnetViaWorkloads = cr.SubnetViaWorkloads
	return s.Status(c)
}

func (s *session) Status(c context.Context) *rpc.ConnectInfo {
	return s.status(c, false)
}

func (s *session) status(c context.Context, initial bool) *rpc.ConnectInfo {
	cfg := s.Kubeconfig
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
func (s *session) Uninstall(ctx context.Context, ur *rpc.UninstallRequest) (*common.Result, error) {
	_, err := s.managerClient.UninstallAgents(ctx, &manager.UninstallAgentsRequest{
		SessionInfo: s.sessionInfo,
		Agents:      ur.Agents,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return s.legacyUninstall(ctx, ur)
		}
		dlog.Errorf(ctx, "uninstall agents failed: %v", err)
	}
	return errcat.ToResult(err), nil
}

func (s *session) legacyUninstall(ctx context.Context, ur *rpc.UninstallRequest) (*common.Result, error) {
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
		if ur.Namespace == "" {
			ur.Namespace = s.Namespace
		}
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

	_ = s.ClearIngestsAndIntercepts(ctx)
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
		if ur.Namespace == "" {
			ur.Namespace = s.Namespace
		}
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

func (s *session) connectRootDaemon(ctx context.Context, nc *rootdRpc.NetworkConfig, isPodDaemon bool) (rd rootdRpc.DaemonClient, err error) {
	// establish a connection to the root daemon gRPC grpcService
	dlog.Info(ctx, "Connecting to root daemon...")
	svc := userd.GetService(ctx)
	if svc.RootSessionInProcess() {
		// Just run the root session in-process.
		_, rootSession, err := rootd.NewInProcSession(ctx, nc, s.managerClient, s.managerVersion, isPodDaemon)
		if err != nil {
			return nil, err
		}
		if err = rootSession.Start(ctx, dgroup.NewGroup(ctx, dgroup.GroupConfig{})); err != nil {
			return nil, err
		}
		rd = rootSession
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

func (s *session) localWorkloadsWatcher(ctx context.Context, namespace string, synced *sync.WaitGroup) error {
	defer func() {
		if synced != nil {
			synced.Done()
		}
		dlog.Debug(ctx, "client workload watcher ended")
	}()

	knownWorkloadKinds, err := s.managerClient.GetKnownWorkloadKinds(ctx, s.sessionInfo)
	if err != nil {
		if status.Code(err) != codes.Unimplemented {
			return fmt.Errorf("failed to get known workload kinds: %w", err)
		}
		// Talking to an older traffic-manager, use legacy default types
		knownWorkloadKinds = &manager.KnownWorkloadKinds{Kinds: []manager.WorkloadInfo_Kind{
			manager.WorkloadInfo_DEPLOYMENT,
			manager.WorkloadInfo_REPLICASET,
			manager.WorkloadInfo_STATEFULSET,
		}}
	}

	dlog.Debugf(ctx, "Watching workloads from client due to lack of workload watcher support in traffic-manager %s", s.managerVersion)
	fc := informer.GetFactory(ctx, namespace)
	if fc == nil {
		ctx = informer.WithFactory(ctx, namespace)
		fc = informer.GetFactory(ctx, namespace)
	}

	enabledWorkloadKinds := make(k8sapi.Kinds, len(knownWorkloadKinds.Kinds))
	for i, kind := range knownWorkloadKinds.Kinds {
		switch kind {
		case manager.WorkloadInfo_DEPLOYMENT:
			enabledWorkloadKinds[i] = k8sapi.DeploymentKind
			workload.StartDeployments(ctx, namespace)
		case manager.WorkloadInfo_REPLICASET:
			enabledWorkloadKinds[i] = k8sapi.ReplicaSetKind
			workload.StartReplicaSets(ctx, namespace)
		case manager.WorkloadInfo_STATEFULSET:
			enabledWorkloadKinds[i] = k8sapi.StatefulSetKind
			workload.StartStatefulSets(ctx, namespace)
		case manager.WorkloadInfo_ROLLOUT:
			enabledWorkloadKinds[i] = k8sapi.RolloutKind
			workload.StartRollouts(ctx, namespace)
			af := fc.GetArgoRolloutsInformerFactory()
			af.Start(ctx.Done())
		}
	}

	kf := fc.GetK8sInformerFactory()
	kf.Start(ctx.Done())

	ww, err := workload.NewWatcher(ctx, namespace, enabledWorkloadKinds)
	if err != nil {
		return err
	}
	kf.WaitForCacheSync(ctx.Done())

	wlCh := ww.Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case wls := <-wlCh:
			if wls == nil {
				return nil
			}
			s.workloadsLock.Lock()
			workloads, ok := s.workloads[namespace]
			if !ok {
				workloads = make(map[workloadInfoKey]workloadInfo)
				s.workloads[namespace] = workloads
			}
			for _, we := range wls {
				w := we.Workload
				key := workloadInfoKey{kind: workload.RpcKind(w.GetKind()), name: w.GetName()}
				if we.Type == workload.EventTypeDelete {
					delete(workloads, key)
				} else {
					workloads[key] = workloadInfo{
						state: workload.GetWorkloadState(w),
						uid:   w.GetUID(),
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
		}
	}
}

func (s *session) workloadsWatcher(ctx context.Context, namespace string, synced *sync.WaitGroup) error {
	defer func() {
		if synced != nil {
			synced.Done()
		}
	}()
	wlc, err := s.managerClient.WatchWorkloads(ctx, &manager.WorkloadEventsRequest{SessionInfo: s.sessionInfo, Namespace: namespace})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
			return errcat.User.New(st.Message())
		}
		return err
	}

	for ctx.Err() == nil {
		wls, err := wlc.Recv()
		if err != nil {
			return err
		}

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
					uid:              types.UID(w.Uid),
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
	}
	return nil
}
