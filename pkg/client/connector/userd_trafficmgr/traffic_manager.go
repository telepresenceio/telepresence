package userd_trafficmgr

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"
	errors2 "k8s.io/apimachinery/pkg/api/errors"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/actions"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type Callbacks struct {
	GetAPIKey       func(context.Context, string, bool) (string, error)
	SetClient       func(client manager.ManagerClient, callOptions ...grpc.CallOption)
	SetOutboundInfo func(ctx context.Context, in *daemon.OutboundInfo, opts ...grpc.CallOption) (*empty.Empty, error)
}

// trafficManager is a handle to access the Traffic Manager in a
// cluster.
type trafficManager struct {
	*installer // installer is also a k8sCluster
	callbacks  Callbacks

	// local information
	env         client.Env
	installID   string // telepresence's install ID
	userAndHost string // "laptop-username@laptop-hostname"

	// manager client
	managerClient manager.ManagerClient
	managerErr    error         // if managerClient is nil, why it's nil
	startup       chan struct{} // gets closed when managerClient is fully initialized (or managerErr is set)
	//
	// What you should read in to the above: It isn't safe to read .managerClient or .managerErr
	// until .startup is closed, and it isn't safe to mutate them after .startup is closed.

	sessionInfo *manager.SessionInfo // sessionInfo returned by the traffic-manager

	// Map of desired mount points for intercepts
	mountPoints sync.Map

	// currentIntercepts is the latest snapshot returned by the intercept watcher
	currentIntercepts     []*manager.InterceptInfo
	currentInterceptsLock sync.Mutex

	// activeInterceptsWaiters contains chan interceptResult keyed by intercept name
	activeInterceptsWaiters sync.Map
}

// interceptResult is what gets written to the activeInterceptsWaiters channels
type interceptResult struct {
	intercept *manager.InterceptInfo
	err       error
}

// New returns a TrafficManager resource for the given cluster if it has a Traffic Manager service.
func New(
	_ context.Context,
	env client.Env,
	cluster *userd_k8s.Cluster,
	installID string,
	callbacks Callbacks,
) (*trafficManager, error) {
	userinfo, err := user.Current()
	if err != nil {
		return nil, errors.Wrap(err, "user.Current()")
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, errors.Wrap(err, "os.Hostname()")
	}

	// Ensure that we have a traffic-manager to talk to.
	ti, err := newTrafficManagerInstaller(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "new installer")
	}
	tm := &trafficManager{
		installer:   ti,
		env:         env,
		installID:   installID,
		startup:     make(chan struct{}),
		userAndHost: fmt.Sprintf("%s@%s", userinfo.Username, host),
		callbacks:   callbacks,
	}

	return tm, nil
}

func (tm *trafficManager) Run(c context.Context) error {
	err := tm.ensureManager(c, &tm.env)
	if err != nil {
		tm.managerErr = fmt.Errorf("failed to start traffic manager: %w", err)
		close(tm.startup)
		return err
	}

	grpcDialer, err := dnet.NewK8sPortForwardDialer(tm.ConfigFlags, tm.Client())
	if err != nil {
		return err
	}
	grpcAddr := net.JoinHostPort(
		"svc/traffic-manager."+tm.GetManagerNamespace(),
		fmt.Sprint(install.ManagerPortHTTP))

	// First check. Establish connection
	clientConfig := client.GetConfig(c)
	tos := &clientConfig.Timeouts
	tc, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer cancel()

	var conn *grpc.ClientConn
	defer func() {
		if err != nil && conn != nil {
			conn.Close()
		}
		select {
		case <-tm.startup:
			// closed, nothing to do
		default:
			if err != nil && tm.managerClient == nil {
				tm.managerErr = err
			}
			close(tm.startup)
		}
	}()

	opts := []grpc.DialOption{grpc.WithContextDialer(grpcDialer),
		grpc.WithInsecure(),
		grpc.WithNoProxy(),
		grpc.WithBlock()}

	if mxRecvSize := clientConfig.Grpc.MaxReceiveSize; mxRecvSize != nil {
		if mz, ok := mxRecvSize.AsInt64(); ok {
			opts = append(opts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(int(mz))))
		}
	}
	conn, err = grpc.DialContext(tc, grpcAddr, opts...)
	if err != nil {
		return client.CheckTimeout(tc, fmt.Errorf("dial manager: %w", err))
	}

	mClient := manager.NewManagerClient(conn)
	si, err := mClient.ArriveAsClient(tc, &manager.ClientInfo{
		Name:      tm.userAndHost,
		InstallId: tm.installID,
		Product:   "telepresence",
		Version:   client.Version(),
		ApiKey:    func() string { tok, _ := tm.callbacks.GetAPIKey(c, "manager", false); return tok }(),
	})
	if err != nil {
		return client.CheckTimeout(tc, fmt.Errorf("manager.ArriveAsClient: %w", err))
	}
	tm.managerClient = mClient
	tm.sessionInfo = si

	// Gotta call mgrProxy.SetClient before we call daemon.SetOutboundInfo which tells the
	// daemon to use the proxy.
	tm.callbacks.SetClient(tm.managerClient)

	// Tell daemon what it needs to know in order to establish outbound traffic to the cluster
	if _, err := tm.callbacks.SetOutboundInfo(c, tm.getOutboundInfo()); err != nil {
		tm.managerClient = nil
		tm.callbacks.SetClient(nil)
		return fmt.Errorf("daemon.SetOutboundInfo: %w", err)
	}

	close(tm.startup)

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("remain", tm.remain)
	g.Go("intercept-port-forward", tm.workerPortForwardIntercepts)
	return g.Wait()
}

func (tm *trafficManager) session() *manager.SessionInfo {
	return tm.sessionInfo
}

// hasOwner parses an object and determines whether the object has an
// owner that is of a kind we prefer. Currently the only owner that we
// prefer is a Deployment, but this may grow in the future
func (tm *trafficManager) hasOwner(obj kates.Object) bool {
	for _, owner := range obj.GetOwnerReferences() {
		if owner.Kind == "Deployment" {
			return true
		}
	}
	return false
}

// getObjectAndLabels gets the object + its associated labels, as well as a reason
// it cannot be intercepted if that is the case.
func (tm *trafficManager) getObjectAndLabels(ctx context.Context, objectKind, namespace, name string) (kates.Object, map[string]string, string, error) {
	var object kates.Object
	var labels map[string]string
	var reason string
	switch objectKind {
	case "Deployment":
		dep, err := tm.FindDeployment(ctx, namespace, name)
		if err != nil {
			// Removed from snapshot since the name slice was obtained
			if !errors2.IsNotFound(err) {
				dlog.Error(ctx, err)
			}

			return nil, nil, "", err
		}

		if dep.Status.Replicas == int32(0) {
			reason = "Has 0 replicas"
		}
		object = dep
		labels = dep.Spec.Template.Labels

	case "ReplicaSet":
		rs, err := tm.FindReplicaSet(ctx, namespace, name)
		if err != nil {
			// Removed from snapshot since the name slice was obtained
			if !errors2.IsNotFound(err) {
				dlog.Error(ctx, err)
			}
			return nil, nil, "", err
		}

		if rs.Status.Replicas == int32(0) {
			reason = "Has 0 replicas"
		}
		object = rs
		labels = rs.Spec.Template.Labels

	case "StatefulSet":
		statefulSet, err := tm.FindStatefulSet(ctx, namespace, name)
		if err != nil {
			// Removed from snapshot since the name slice was obtained
			if !errors2.IsNotFound(err) {
				dlog.Error(ctx, err)
			}
			return nil, nil, "", err
		}

		if statefulSet.Status.Replicas == int32(0) {
			reason = "Has 0 replicas"
		}
		object = statefulSet
		labels = statefulSet.Spec.Template.Labels
	default:
		reason = "No workload telepresence knows how to intercept"
	}
	return object, labels, reason, nil
}

// getInfosForWorkload creates a WorkloadInfo for every workload in names
// of the given objectKind.  Additionally, it uses information about the
// filter param, which is configurable, to decide which workloads to add
// or ignore based on the filter criteria.
func (tm *trafficManager) getInfosForWorkload(
	ctx context.Context,
	names []string,
	objectKind,
	namespace string,
	iMap map[string]*manager.InterceptInfo,
	aMap map[string]*manager.AgentInfo,
	filter rpc.ListRequest_Filter,
) []*rpc.WorkloadInfo {
	workloadInfos := make([]*rpc.WorkloadInfo, 0)
	for _, name := range names {
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
			object, labels, reason, err := tm.getObjectAndLabels(ctx, objectKind, namespace, name)
			if err != nil {
				continue
			}
			if reason == "" {
				// If an object is owned by a higher level workload, then users should
				// intercept that workload so we will not include it in our slice.
				if tm.hasOwner(object) {
					dlog.Infof(ctx, "Not including snapshot for object as it has an owner: %s.%s", object.GetName(), object.GetNamespace())
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
			WorkloadResourceType:   objectKind,
		})
	}
	return workloadInfos
}

func (tm *trafficManager) WorkloadInfoSnapshot(ctx context.Context, rq *rpc.ListRequest) *rpc.WorkloadInfoSnapshot {
	var iMap map[string]*manager.InterceptInfo

	namespace := tm.ActualNamespace(rq.Namespace)
	if namespace == "" {
		// namespace is not currently mapped
		return &rpc.WorkloadInfoSnapshot{}
	}

	<-tm.startup
	is := tm.getCurrentIntercepts()
	iMap = make(map[string]*manager.InterceptInfo, len(is))
	for _, i := range is {
		if i.Spec.Namespace == namespace {
			iMap[i.Spec.Agent] = i
		}
	}
	var aMap map[string]*manager.AgentInfo
	if as, _ := actions.ListAllAgents(ctx, tm.managerClient, tm.session().SessionId); as != nil {
		aMap = make(map[string]*manager.AgentInfo, len(as))
		for _, a := range as {
			if a.Namespace == namespace {
				aMap[a.Name] = a
			}
		}
	} else {
		aMap = map[string]*manager.AgentInfo{}
	}

	filter := rq.Filter
	workloadInfos := make([]*rpc.WorkloadInfo, 0)

	// These are all the workloads we care about and their associated function
	// to get the names of those workloads
	workloadsToGet := map[string]func(context.Context, string) ([]string, error){
		"Deployment":  tm.DeploymentNames,
		"ReplicaSet":  tm.ReplicaSetNames,
		"StatefulSet": tm.StatefulSetNames,
	}

	for workloadKind, namesFunc := range workloadsToGet {
		workloadNames, err := namesFunc(ctx, namespace)
		if err != nil {
			dlog.Error(ctx, err)
			dlog.Infof(ctx, "Skipping getting info for workloads: %s", workloadKind)
			continue
		}
		newWorkloadInfos := tm.getInfosForWorkload(ctx, workloadNames, workloadKind, namespace, iMap, aMap, filter)
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

func (tm *trafficManager) remain(c context.Context) error {
	<-tm.startup
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
				ApiKey:  func() string { tok, _ := tm.callbacks.GetAPIKey(c, "manager", false); return tok }(),
			})
			if err != nil && c.Err() == nil {
				dlog.Error(c, err)
			}
		}
	}
}

func (tm *trafficManager) SetStatus(ctx context.Context, r *rpc.ConnectInfo) {
	if tm == nil {
		return
	}
	<-tm.startup
	if tm.managerClient == nil {
		r.BridgeOk = false
		r.Intercepts = &manager.InterceptInfoSnapshot{}
		r.Agents = &manager.AgentInfoSnapshot{}
		if err := tm.managerErr; err != nil {
			r.ErrorText = err.Error()
		}
	} else {
		agents, _ := actions.ListAllAgents(ctx, tm.managerClient, tm.session().SessionId)
		r.Agents = &manager.AgentInfoSnapshot{Agents: agents}
		r.Intercepts = &manager.InterceptInfoSnapshot{Intercepts: tm.getCurrentIntercepts()}
		r.SessionInfo = tm.session()
		r.BridgeOk = true
	}
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

func (tm *trafficManager) Uninstall(c context.Context, ur *rpc.UninstallRequest) (*rpc.UninstallResult, error) {
	result := &rpc.UninstallResult{}
	<-tm.startup
	agents, _ := actions.ListAllAgents(c, tm.managerClient, tm.session().SessionId)

	// Since workloads can have more than one replica, we get a slice of agents
	// where the agent to workload mapping is 1-to-1.  This is important
	// because in the ALL_AGENTS or default case, we could edit the same
	// workload n times for n replicas, which could cause race conditions
	agents = getRepresentativeAgents(c, agents)

	_ = tm.clearIntercepts(c)
	switch ur.UninstallType {
	case rpc.UninstallRequest_UNSPECIFIED:
		return nil, errors.New("invalid uninstall request")
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
			}
		}
		agents = selectedAgents
		fallthrough
	case rpc.UninstallRequest_ALL_AGENTS:
		if len(agents) > 0 {
			if err := tm.removeManagerAndAgents(c, true, agents, &tm.env); err != nil {
				result.ErrorText = err.Error()
			}
		}
	default:
		// Cancel all communication with the manager
		if err := tm.removeManagerAndAgents(c, false, agents, &tm.env); err != nil {
			result.ErrorText = err.Error()
		}
	}
	return result, nil
}

// getClusterCIDRs finds the service CIDR and the pod CIDRs of all nodes in the cluster
func (tm *trafficManager) getOutboundInfo() *daemon.OutboundInfo {
	info := &daemon.OutboundInfo{
		Session: tm.sessionInfo,
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

func (tm *trafficManager) GetClientBlocking(ctx context.Context) (manager.ManagerClient, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-tm.startup:
		return tm.managerClient, tm.managerErr
	}
}

func (tm *trafficManager) GetClientNonBlocking() (manager.ManagerClient, error) {
	select {
	case <-tm.startup:
		return tm.managerClient, tm.managerErr
	default:
		return nil, nil
	}
}
