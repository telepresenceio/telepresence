package trafficmgr

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"sort"
	"sync"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/header"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type Session interface {
	restapi.AgentState
	AddIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.InterceptResult, error)
	CanIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.InterceptResult, kates.Object)
	GetStatus(context.Context) *rpc.ConnectInfo
	IngressInfos(c context.Context) ([]*manager.IngressInfo, error)
	RemoveIntercept(context.Context, string) error
	Run(context.Context) error
	Uninstall(context.Context, *rpc.UninstallRequest) (*rpc.UninstallResult, error)
	WorkloadInfoSnapshot(context.Context, *rpc.ListRequest) *rpc.WorkloadInfoSnapshot
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

type TrafficManager struct {
	*installer // installer is also a k8sCluster

	// local information
	installID   string // telepresence's install ID
	userAndHost string // "laptop-username@laptop-hostname"

	getCloudAPIKey func(context.Context, string, bool) (string, error)

	// manager client
	managerClient manager.ManagerClient

	sessionInfo *manager.SessionInfo // sessionInfo returned by the traffic-manager

	// Map of desired mount points for intercepts
	mountPoints sync.Map

	// Map of mutexes, so that we don't create and delete
	// mount points concurrently
	mountMutexes sync.Map

	// currentIntercepts is the latest snapshot returned by the intercept watcher
	currentIntercepts     []*manager.InterceptInfo
	currentInterceptsLock sync.Mutex
	currentMatchers       map[string]header.Matcher
	currentAPIServers     map[int]apiServer

	// currentAgents is the latest snapshot returned by the agent watcher
	currentAgents     []*manager.AgentInfo
	currentAgentsLock sync.Mutex

	// activeInterceptsWaiters contains chan interceptResult keyed by intercept name
	activeInterceptsWaiters sync.Map

	// agentWaiters contains chan *manager.AgentInfo keyed by agent <name>.<namespace>
	agentWaiters sync.Map
}

// interceptResult is what gets written to the activeInterceptsWaiters channels
type interceptResult struct {
	intercept *manager.InterceptInfo
	err       error
}

func NewSession(c context.Context, sr *scout.Reporter, cr *rpc.ConnectRequest, svc Service) (Session, *connector.ConnectInfo) {
	sr.Report(c, "connect")

	rootDaemon, err := svc.RootDaemonClient(c)
	if err != nil {
		return nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
	}

	dlog.Info(c, "Connecting to k8s cluster...")
	cluster, err := connectCluster(c, cr, rootDaemon)
	if err != nil {
		dlog.Errorf(c, "unable to track k8s cluster: %+v", err)
		return nil, connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	dlog.Infof(c, "Connected to context %s (%s)", cluster.Context, cluster.Server)

	// Phone home with the information about the size of the cluster
	sr.SetMetadatum(c, "cluster_id", cluster.GetClusterId(c))
	sr.Report(c, "connecting_traffic_manager", scout.Entry{
		Key:   "mapped_namespaces",
		Value: len(cr.MappedNamespaces),
	})

	connectStart := time.Now()

	dlog.Info(c, "Connecting to traffic manager...")
	tmgr, err := connectMgr(c, cluster, sr.InstallID(), svc)

	if err != nil {
		dlog.Errorf(c, "Unable to connect to TrafficManager: %s", err)
		return nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}

	// Must call SetManagerClient before calling daemon.Connect which tells the
	// daemon to use the proxy.
	svc.SetManagerClient(tmgr.managerClient)

	// Tell daemon what it needs to know in order to establish outbound traffic to the cluster
	oi := tmgr.getOutboundInfo(c)
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

	// Collect data on how long connection time took
	sr.Report(c, "finished_connecting_traffic_manager", scout.Entry{
		Key: "connect_duration", Value: time.Since(connectStart).Seconds()})

	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_UNSPECIFIED,
		ClusterContext: cluster.Config.Context,
		ClusterServer:  cluster.Config.Server,
		ClusterId:      cluster.GetClusterId(c),
		SessionInfo:    tmgr.session(),
		Agents:         &manager.AgentInfoSnapshot{Agents: tmgr.getCurrentAgents()},
		Intercepts:     &manager.InterceptInfoSnapshot{Intercepts: tmgr.getCurrentIntercepts()},
	}
	return tmgr, ret
}

// connectCluster returns a configured cluster instance
func connectCluster(c context.Context, cr *rpc.ConnectRequest, rootDaemon daemon.DaemonClient) (*k8s.Cluster, error) {
	config, err := k8s.NewConfig(c, cr.KubeFlags)
	if err != nil {
		return nil, err
	}

	mappedNamespaces := cr.MappedNamespaces
	if len(mappedNamespaces) == 1 && mappedNamespaces[0] == "all" {
		mappedNamespaces = nil
	}
	sort.Strings(mappedNamespaces)

	c, cancel := client.GetConfig(c).Timeouts.TimeoutContext(c, client.TimeoutClusterConnect)
	defer cancel()
	cluster, err := k8s.NewCluster(c,
		config,
		mappedNamespaces,
		rootDaemon,
	)
	if err != nil {
		return nil, err
	}
	return cluster, nil
}

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(c context.Context, cluster *k8s.Cluster, installID string, svc Service) (*TrafficManager, error) {
	clientConfig := client.GetConfig(c)
	tos := &clientConfig.Timeouts

	c, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerConnect)
	defer cancel()

	userinfo, err := user.Current()
	if err != nil {
		return nil, errors.Wrap(err, "user.Current()")
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, errors.Wrap(err, "os.Hostname()")
	}

	apiKey, err := svc.LoginExecutor().GetAPIKey(c, a8rcloud.KeyDescTrafficManager)
	if err != nil {
		dlog.Errorf(c, "unable to get APIKey: %v", err)
	}

	// Ensure that we have a traffic-manager to talk to.
	ti, err := NewTrafficManagerInstaller(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "new installer")
	}

	dlog.Debug(c, "ensure that traffic-manager exists")
	if err = ti.EnsureManager(c); err != nil {
		dlog.Errorf(c, "failed to ensure traffic-manager, %v", err)
		return nil, fmt.Errorf("failed to ensure traffic manager: %w", err)
	}

	dlog.Debug(c, "traffic-manager started, creating port-forward")
	grpcDialer, err := dnet.NewK8sPortForwardDialer(c, cluster.ConfigFlags, cluster.Client())
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
		grpc.WithInsecure(),
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
		installer:      ti.(*installer),
		installID:      installID,
		userAndHost:    userAndHost,
		getCloudAPIKey: svc.LoginExecutor().GetCloudAPIKey,
		managerClient:  mClient,
		sessionInfo:    si,
	}, nil
}

func connectError(t rpc.ConnectInfo_ErrType, err error) *rpc.ConnectInfo {
	return &rpc.ConnectInfo{
		Error:         t,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

// Run (1) starts up with ensuring that the manager is installed and running,
// but then for most of its life
//  - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//  - watch the intercepts (manager.WatchIntercepts) and then
//    + (3) listen on the appropriate local ports and forward them to the intercepted
//      Services, and
//    + (4) mount the appropriate remote volumes.
func (tm *TrafficManager) Run(c context.Context) error {
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("remain", tm.remain)
	g.Go("intercept-port-forward", tm.workerPortForwardIntercepts)
	g.Go("agent-watcher", tm.agentInfoWatcher)
	g.Go("dial-request-watcher", tm.dialRequestWatcher)
	return g.Wait()
}

func (tm *TrafficManager) session() *manager.SessionInfo {
	return tm.sessionInfo
}

// hasOwner parses an object and determines whether the object has an
// owner that is of a kind we prefer. Currently the only owner that we
// prefer is a Deployment, but this may grow in the future
func (tm *TrafficManager) hasOwner(obj kates.Object) bool {
	for _, owner := range obj.GetOwnerReferences() {
		if owner.Kind == "Deployment" {
			return true
		}
	}
	return false
}

// getReasonAndLabels gets the workload's associated labels, as well as a reason
// it cannot be intercepted if that is the case.
func (tm *TrafficManager) getReasonAndLabels(workload kates.Object, namespace, name string) (map[string]string, string, error) {
	var labels map[string]string
	var reason string
	switch workload := workload.(type) {
	case *kates.Deployment:
		if workload.Status.Replicas == int32(0) {
			reason = "Has 0 replicas"
		}
		labels = workload.Spec.Template.Labels

	case *kates.ReplicaSet:
		if workload.Status.Replicas == int32(0) {
			reason = "Has 0 replicas"
		}
		labels = workload.Spec.Template.Labels

	case *kates.StatefulSet:
		if workload.Status.Replicas == int32(0) {
			reason = "Has 0 replicas"
		}
		labels = workload.Spec.Template.Labels
	default:
		reason = "No workload telepresence knows how to intercept"
	}
	return labels, reason, nil
}

// getInfosForWorkload creates a WorkloadInfo for every workload in names
// of the given objectKind.  Additionally, it uses information about the
// filter param, which is configurable, to decide which workloads to add
// or ignore based on the filter criteria.
func (tm *TrafficManager) getInfosForWorkloads(
	ctx context.Context,
	workloads []kates.Object,
	namespace string,
	iMap map[string]*manager.InterceptInfo,
	aMap map[string]*manager.AgentInfo,
	filter rpc.ListRequest_Filter,
) []*rpc.WorkloadInfo {
	workloadInfos := make([]*rpc.WorkloadInfo, 0)
	for _, workload := range workloads {
		name := workload.GetName()
		iCept, ok := iMap[name]
		if !ok && filter <= rpc.ListRequest_INTERCEPTS {
			continue
		}
		agent, ok := aMap[name]
		if !ok && filter <= rpc.ListRequest_INSTALLED_AGENTS {
			continue
		}
		reason := ""
		if agent == nil && iCept == nil {
			var labels map[string]string
			var err error
			if labels, reason, err = tm.getReasonAndLabels(workload, namespace, name); err != nil {
				continue
			}
			if reason == "" {
				// If an object is owned by a higher level workload, then users should
				// intercept that workload so we will not include it in our slice.
				if tm.hasOwner(workload) {
					dlog.Infof(ctx, "Not including snapshot for object as it has an owner: %s.%s", name, workload.GetNamespace())
					continue
				}

				matchingSvcs, err := install.FindMatchingServices(ctx, tm.Client(), "", "", namespace, labels)
				if err != nil {
					continue
				}
				if len(matchingSvcs) == 0 {
					reason = "No service with matching selector"
				}
			}

			// If we have a reason, that means it's not interceptable, so we only
			// pass the workload through if they want to see all workloads, not
			// just the interceptable ones
			if !ok && filter <= rpc.ListRequest_INTERCEPTABLE && reason != "" {
				continue
			}
		}

		workloadInfos = append(workloadInfos, &rpc.WorkloadInfo{
			Name:                   name,
			NotInterceptableReason: reason,
			AgentInfo:              agent,
			InterceptInfo:          iCept,
			WorkloadResourceType:   workload.GetObjectKind().GroupVersionKind().Kind,
		})
	}
	return workloadInfos
}

func (tm *TrafficManager) WorkloadInfoSnapshot(ctx context.Context, rq *rpc.ListRequest) *rpc.WorkloadInfoSnapshot {
	var iMap map[string]*manager.InterceptInfo

	namespace := tm.ActualNamespace(rq.Namespace)
	if namespace == "" {
		// namespace is not currently mapped
		return &rpc.WorkloadInfoSnapshot{}
	}

	is := tm.getCurrentIntercepts()
	iMap = make(map[string]*manager.InterceptInfo, len(is))
	for _, i := range is {
		if i.Spec.Namespace == namespace {
			iMap[i.Spec.Agent] = i
		}
	}
	aMap := tm.getCurrentAgentsInNamespace(namespace)
	filter := rq.Filter
	workloadInfos := make([]*rpc.WorkloadInfo, 0)

	// These are all the workloads we care about and their associated function
	// to get the names of those workloads
	workloadsToGet := map[string]func(context.Context, string) ([]kates.Object, error){
		"Deployment":  tm.Deployments,
		"ReplicaSet":  tm.ReplicaSets,
		"StatefulSet": tm.StatefulSets,
	}

	for workloadKind, getFunc := range workloadsToGet {
		workloads, err := getFunc(ctx, namespace)
		if err != nil {
			dlog.Error(ctx, err)
			dlog.Infof(ctx, "Skipping getting info for workloads: %s", workloadKind)
			continue
		}
		newWorkloadInfos := tm.getInfosForWorkloads(ctx, workloads, namespace, iMap, aMap, filter)
		workloadInfos = append(workloadInfos, newWorkloadInfos...)
	}

	for localIntercept, localNs := range tm.LocalIntercepts {
		if localNs == namespace {
			workloadInfos = append(workloadInfos, &rpc.WorkloadInfo{InterceptInfo: &manager.InterceptInfo{
				Spec:              &manager.InterceptSpec{Name: localIntercept, Namespace: localNs},
				Disposition:       manager.InterceptDispositionType_ACTIVE,
				MechanismArgsDesc: "as local-only",
			}})
		}
	}

	return &rpc.WorkloadInfoSnapshot{Workloads: workloadInfos}
}

func (tm *TrafficManager) remain(c context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.Done():
			_ = tm.clearIntercepts(dcontext.WithoutCancel(c))
			_, _ = tm.managerClient.Depart(dcontext.WithoutCancel(c), tm.session())
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
			}
		}
	}
}

func (tm *TrafficManager) GetStatus(c context.Context) *rpc.ConnectInfo {
	cfg := tm.Config
	ret := &rpc.ConnectInfo{
		Error:          rpc.ConnectInfo_ALREADY_CONNECTED,
		ClusterContext: cfg.Context,
		ClusterServer:  cfg.Server,
		ClusterId:      tm.GetClusterId(c),
		SessionInfo:    tm.session(),
		Agents:         &manager.AgentInfoSnapshot{Agents: tm.getCurrentAgents()},
		Intercepts:     &manager.InterceptInfoSnapshot{Intercepts: tm.getCurrentIntercepts()},
	}
	return ret
}

// Given a slice of AgentInfo, this returns another slice of agents with one
// agent per namespace, name pair.
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

func (tm *TrafficManager) Uninstall(c context.Context, ur *rpc.UninstallRequest) (*rpc.UninstallResult, error) {
	result := &rpc.UninstallResult{}
	agents := tm.getCurrentAgents()

	// Since workloads can have more than one replica, we get a slice of agents
	// where the agent to workload mapping is 1-to-1.  This is important
	// because in the ALL_AGENTS or default case, we could edit the same
	// workload n times for n replicas, which could cause race conditions
	agents = getRepresentativeAgents(c, agents)

	_ = tm.clearIntercepts(c)
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
