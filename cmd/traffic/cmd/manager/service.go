package manager

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	dns2 "github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

// Clock is the mechanism used by the Manager state to get the current time.
type Clock interface {
	Now() time.Time
}

type Service interface {
	rpc.ManagerServer
	ID() string
	InstallID() string
	MakeInterceptID(context.Context, string, string) (string, error)
	RegisterServers(*grpc.Server)
	State() state.State
	ClusterInfo() cluster.Info

	// unexported methods.
	runSessionGCLoop(context.Context) error
	serveHTTP(context.Context) error
	servePrometheus(context.Context) error
}

type service struct {
	clock              Clock
	id                 string
	state              state.State
	clusterInfo        cluster.Info
	configWatcher      config.Watcher
	activeHttpRequests int32
	activeGrpcRequests int32
	serviceNameNs      string
	serviceNameFQN     string
	dotClusterDomain   string

	// Possibly extended version of the service. Use when calling interface methods.
	self Service

	rpc.UnsafeManagerServer
}

var _ rpc.ManagerServer = &service{}

type wall struct{}

func (wall) Now() time.Time {
	return time.Now()
}

// checkCompat checks if a CompatibilityVersion has been set for this traffic-manager, and if so, errors with
// an Unimplemented error mentioning the given name if it is less than the required version.
func checkCompat(ctx context.Context, name, requiredVersion string) error {
	if cv := managerutil.GetEnv(ctx).CompatibilityVersion; cv != nil && cv.Compare(semver.MustParse(requiredVersion)) < 0 {
		return status.Error(codes.Unimplemented, fmt.Sprintf("traffic manager of version %s does not implement %s", cv, name))
	}
	return nil
}

func NewService(ctx context.Context, configWatcher config.Watcher) (Service, *dgroup.Group, error) {
	ret := &service{
		clock:         wall{},
		id:            uuid.New().String(),
		configWatcher: configWatcher,
	}

	if managerutil.AgentInjectorEnabled(ctx) {
		var err error
		ctx, err = WithAgentImageRetrieverFunc(ctx, mutator.GetMap(ctx).RegenerateAgentMaps)
		if err != nil {
			dlog.Errorf(ctx, "unable to initialize agent injector: %v", err)
		}
	}
	// These are context dependent so build them once the pool is up
	ret.clusterInfo = cluster.NewInfo(ctx)
	ret.state = state.NewStateFunc(ctx)

	ns := managerutil.GetEnv(ctx).ManagerNamespace
	ret.dotClusterDomain = "." + ret.clusterInfo.ClusterDomain()
	ret.serviceNameNs = fmt.Sprintf("%s.%s.", agentmap.ManagerAppName, ns)
	ret.serviceNameFQN = fmt.Sprintf("%s.%s.svc%s", agentmap.ManagerAppName, ns, ret.dotClusterDomain)

	ret.self = ret
	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
		SoftShutdownTimeout:  5 * time.Second,
	})
	return ret, g, nil
}

func (s *service) SetSelf(self Service) {
	s.self = self
}

func (s *service) ClusterInfo() cluster.Info {
	return s.clusterInfo
}

func (s *service) ID() string {
	return s.id
}

func (s *service) State() state.State {
	return s.state
}

func (s *service) InstallID() string {
	return s.clusterInfo.ID()
}

// Version returns the version information of the Manager.
func (*service) Version(context.Context, *empty.Empty) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Name: DisplayName, Version: version.Version}, nil
}

func (s *service) GetAgentImageFQN(ctx context.Context, _ *empty.Empty) (*rpc.AgentImageFQN, error) {
	if managerutil.AgentInjectorEnabled(ctx) {
		return &rpc.AgentImageFQN{
			FQN: managerutil.GetAgentImage(ctx),
		}, nil
	}
	return nil, status.Error(codes.Unavailable, "")
}

func (s *service) GetAgentConfig(ctx context.Context, request *rpc.AgentConfigRequest) (*rpc.AgentConfigResponse, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.Session)
	sessionID := tunnel.SessionID(request.GetSession().GetSessionId())
	clientInfo := s.state.GetClient(sessionID)
	if clientInfo == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}
	scs, err := s.State().GetOrGenerateAgentConfig(ctx, request.Name, clientInfo.Namespace)
	if err != nil {
		return nil, err
	}
	r := rpc.AgentConfigResponse{}
	r.Data, err = scs.Marshal()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &r, nil
}

func (s *service) GetLicense(context.Context, *empty.Empty) (*rpc.License, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *service) CanConnectAmbassadorCloud(context.Context, *empty.Empty) (*rpc.AmbassadorCloudConnection, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *service) GetCloudConfig(context.Context, *empty.Empty) (*rpc.AmbassadorCloudConfig, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// GetTelepresenceAPI returns information about the TelepresenceAPI server.
func (s *service) GetTelepresenceAPI(ctx context.Context, e *empty.Empty) (*rpc.TelepresenceAPIInfo, error) {
	env := managerutil.GetEnv(ctx)
	return &rpc.TelepresenceAPIInfo{Port: int32(env.APIPort)}, nil
}

// ArriveAsClient establishes a session between a client and the Manager.
func (s *service) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	dlog.Debugf(ctx, "Namespace: %s", client.Namespace)

	if !s.State().ManagesNamespace(ctx, client.Namespace) {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("namespace %s is not managed", client.Namespace))
	}

	if val := validateClient(client); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	installId := client.GetInstallId()

	IncrementCounter(s.state.GetConnectCounter(), client.Name, client.InstallId)
	SetGauge(s.state.GetConnectActiveStatus(), client.Name, client.InstallId, nil, 1)

	return &rpc.SessionInfo{
		SessionId:        string(s.state.AddClient(client, s.clock.Now())),
		ManagerInstallId: s.clusterInfo.ID(),
		InstallId:        &installId,
	}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (s *service) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	dlog.Debugf(ctx, "Name %s, IP %s", agent.PodName, agent.PodIp)
	if val := validateAgent(agent); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	for _, cn := range agent.Containers {
		s.removeExcludedEnvVars(cn.Environment)
	}

	sessionID, err := s.state.AddAgent(ctx, agent, s.clock.Now())
	if err != nil {
		return nil, err
	}

	return &rpc.SessionInfo{
		SessionId:        string(sessionID),
		ManagerInstallId: s.clusterInfo.ID(),
	}, nil
}

func (s *service) ReportMetrics(ctx context.Context, metrics *rpc.TunnelMetrics) (*empty.Empty, error) {
	s.state.AddSessionConsumptionMetrics(metrics)
	return &empty.Empty{}, nil
}

func (s *service) GetClientConfig(ctx context.Context, _ *empty.Empty) (*rpc.CLIConfig, error) {
	return &rpc.CLIConfig{
		ConfigYaml: s.configWatcher.GetClientConfigYaml(ctx),
	}, nil
}

// Remain indicates that the session is still valid.
func (s *service) Remain(ctx context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	sessionID := tunnel.SessionID(req.GetSession().GetSessionId())
	if ok := s.state.MarkSession(req, s.clock.Now()); !ok {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}
	s.state.RefreshSessionConsumptionMetrics(sessionID)
	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (s *service) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, session)
	sessionID := tunnel.SessionID(session.GetSessionId())

	// There's no reason for the caller to wait for this removal to complete.
	go s.state.RemoveSession(context.WithoutCancel(ctx), sessionID)
	return &empty.Empty{}, nil
}

// WatchAgentPods notifies a client of the set of known Agents.
func (s *service) WatchAgentPods(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentPodsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	clientSession := tunnel.SessionID(session.SessionId)
	clientInfo := s.state.GetClient(clientSession)
	if clientInfo == nil {
		return status.Errorf(codes.NotFound, "Client session %q not found", clientSession)
	}
	ns := clientInfo.Namespace

	agentsCh := s.state.WatchAgents(ctx, func(_ tunnel.SessionID, info *state.AgentSession) bool {
		return info.Namespace == ns
	})
	interceptsCh := s.state.WatchIntercepts(ctx, func(_ string, info *state.Intercept) bool {
		return info.ClientSession.SessionId == string(clientSession)
	})
	sessionDone, err := s.state.SessionDone(clientSession)
	if err != nil {
		return err
	}

	var interceptInfos map[string]*state.Intercept
	isIntercepted := func(a *rpc.AgentPodInfo) bool {
		for _, ii := range interceptInfos {
			if a.WorkloadName == ii.Spec.Agent && a.Namespace == ii.Spec.Namespace {
				return true
			}
		}
		return false
	}
	m := mutator.GetMap(ctx)
	var agents []*rpc.AgentPodInfo
	var agentNames []string
	for {
		select {
		case <-sessionDone:
			// Manager believes this session has ended.
			return nil
		case agm, ok := <-agentsCh:
			if !ok {
				return nil
			}
			agents = make([]*rpc.AgentPodInfo, 0, len(agm))
			agentNames = make([]string, 0, len(agm))
			for _, a := range agm {
				if m.IsInactive(types.UID(a.PodUid)) {
					continue
				}
				aip, err := netip.ParseAddr(a.PodIp)
				if err != nil {
					dlog.Errorf(ctx, "error parsing agent pod ip %q: %v", a.PodIp, err)
				}
				ap := &rpc.AgentPodInfo{
					WorkloadName: a.Name,
					PodId:        a.PodUid,
					PodName:      a.PodName,
					Namespace:    a.Namespace,
					PodIp:        aip.AsSlice(),
					ApiPort:      a.ApiPort,
				}
				ap.Intercepted = isIntercepted(ap)
				agents = append(agents, ap)
				agentNames = append(agentNames, fmt.Sprintf("%s(%s)", ap.PodName, net.IP(ap.PodIp)))
			}
		case is, ok := <-interceptsCh:
			if !ok {
				return nil
			}
			interceptInfos = is
			for _, ap := range agents {
				ap.Intercepted = isIntercepted(ap)
			}
		}
		if agents != nil {
			dlog.Debugf(ctx, "Sending update for %s", agentNames)
			if err = stream.Send(&rpc.AgentPodInfoSnapshot{Agents: agents}); err != nil {
				return err
			}
		}
	}
}

// WatchAgents notifies a client of the set of known Agents in the connected namespace.
func (s *service) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	clientInfo := s.state.GetClient(tunnel.SessionID(session.SessionId))
	if clientInfo == nil {
		return status.Errorf(codes.NotFound, "Client session %q not found", session.SessionId)
	}
	ns := clientInfo.Namespace
	return s.watchAgents(ctx, func(_ tunnel.SessionID, a *state.AgentSession) bool { return a.Namespace == ns }, stream)
}

// WatchAgentsNS notifies a client of the set of known Agents in the namespaces given in the request.
func (s *service) WatchAgentsNS(request *rpc.AgentsRequest, stream rpc.Manager_WatchAgentsNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), request.Session)
	return s.watchAgents(ctx, func(_ tunnel.SessionID, a *state.AgentSession) bool {
		return slices.Contains(request.Namespaces, a.Namespace)
	}, stream)
}

func infosEqual(a, b *rpc.AgentInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Name != b.Name || a.Namespace != b.Namespace || a.Product != b.Product || a.Version != b.Version {
		return false
	}
	ams := a.Mechanisms
	bms := b.Mechanisms
	if len(ams) != len(bms) {
		return false
	}
	for i, am := range ams {
		bm := bms[i]
		if am == nil || bm == nil {
			if am != bm {
				return false
			}
		} else if am.Name != bm.Name || am.Product != bm.Product || am.Version != bm.Version {
			return false
		}
	}
	return maps.EqualFunc(a.Containers, b.Containers, func(ac *rpc.AgentInfo_ContainerInfo, bc *rpc.AgentInfo_ContainerInfo) bool {
		if ac == nil || bc == nil {
			return ac == bc
		}
		return ac.MountPoint == bc.MountPoint && maps.Equal(ac.Environment, bc.Environment)
	})
}

func (s *service) watchAgents(ctx context.Context, includeAgent func(tunnel.SessionID, *state.AgentSession) bool, stream rpc.Manager_WatchAgentsServer) error {
	snapshotCh := s.state.WatchAgents(ctx, includeAgent)
	sessionDone, err := s.state.SessionDone(managerutil.GetSessionID(ctx))
	if err != nil {
		return err
	}

	// Ensure that the initial snapshot is not equal to lastSnap even if it is empty by
	// creating a lastSnap with one nil entry.
	lastSnap := make([]*rpc.AgentInfo, 1)

	m := mutator.GetMap(ctx)
	for {
		select {
		case snapshot, ok := <-snapshotCh:
			if !ok {
				// The request has been canceled.
				dlog.Debug(ctx, "Request cancelled")
				return nil
			}

			// Sort snapshot by sessionID and discard inactive agents.
			agentSessionIDs := slices.Sorted(maps.Keys(snapshot))
			agents := make([]*rpc.AgentInfo, 0, len(agentSessionIDs))
			for _, agentSessionID := range agentSessionIDs {
				ag := snapshot[agentSessionID]
				if !m.IsInactive(types.UID(ag.PodUid)) {
					agents = append(agents, ag.AgentInfo)
				}
			}
			if slices.EqualFunc(agents, lastSnap, infosEqual) {
				continue
			}
			lastSnap = agents
			if dlog.MaxLogLevel(ctx) >= dlog.LogLevelDebug {
				names := make([]string, len(agents))
				i := 0
				for _, a := range agents {
					names[i] = a.PodName + "." + a.Namespace
					i++
				}
				dlog.Tracef(ctx, "Sending update %v", names)
			}
			resp := &rpc.AgentInfoSnapshot{
				Agents: agents,
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
		case <-sessionDone:
			// Manager believes this session has ended.
			dlog.Debug(ctx, "Session cancelled")
			return nil
		}
	}
}

// WatchIntercepts notifies a client or agent of the set of intercepts
// relevant to that client or agent.
func (s *service) WatchIntercepts(session *rpc.SessionInfo, stream rpc.Manager_WatchInterceptsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	sessionID := tunnel.SessionID(session.GetSessionId())
	var sessionDone <-chan struct{}
	var filter func(id string, info *state.Intercept) bool
	if sessionID == "" {
		filter = func(id string, info *state.Intercept) bool {
			return info.Disposition != rpc.InterceptDispositionType_REMOVED && !state.IsChildIntercept(info.Spec)
		}
	} else {
		var err error
		if sessionDone, err = s.state.SessionDone(sessionID); err != nil {
			return err
		}

		if agent := s.state.GetAgent(sessionID); agent != nil {
			filter = func(id string, info *state.Intercept) bool {
				if info.Spec.Namespace != agent.Namespace || info.Spec.Agent != agent.Name {
					// Don't return intercepts for different agents.
					return false
				}
				if as := s.state.GetAgent(sessionID); as == nil {
					dlog.Debugf(ctx, "Session no longer active")
					return false
				}
				// Don't return intercepts that aren't in a "agent-owned" state.
				switch info.Disposition {
				case rpc.InterceptDispositionType_WAITING,
					rpc.InterceptDispositionType_ACTIVE,
					rpc.InterceptDispositionType_AGENT_ERROR:
					// agent-owned state: include the intercept
					return true
				case rpc.InterceptDispositionType_REMOVED:
					return true
				default:
					// otherwise: don't return this intercept
					return false
				}
			}
		} else {
			// sessionID refers to a client session.
			filter = func(id string, info *state.Intercept) bool {
				return info.ClientSession.SessionId == string(sessionID) &&
					info.Disposition != rpc.InterceptDispositionType_REMOVED &&
					!state.IsChildIntercept(info.Spec)
			}
		}
	}

	snapshotCh := s.state.WatchIntercepts(ctx, filter)
	for {
		select {
		case snapshot, ok := <-snapshotCh:
			if !ok {
				dlog.Debugf(ctx, "Request cancelled")
				return nil
			}
			dlog.Tracef(ctx, "Sending update")
			intercepts := make([]*rpc.InterceptInfo, 0, len(snapshot))
			for _, intercept := range snapshot {
				intercepts = append(intercepts, intercept.InterceptInfo)
			}
			resp := &rpc.InterceptInfoSnapshot{
				Intercepts: intercepts,
			}
			sort.Slice(intercepts, func(i, j int) bool {
				return intercepts[i].Id < intercepts[j].Id
			})
			if err := stream.Send(resp); err != nil {
				dlog.Debugf(ctx, "Encountered a write error: %v", err)
				return err
			}
		case <-ctx.Done():
			dlog.Debugf(ctx, "Context cancelled")
			return nil
		case <-sessionDone:
			dlog.Debugf(ctx, "Session cancelled")
			return nil
		}
	}
}

func (s *service) PrepareIntercept(ctx context.Context, request *rpc.CreateInterceptRequest) (pi *rpc.PreparedIntercept, err error) {
	ctx = managerutil.WithSessionInfo(ctx, request.Session)
	dlog.Debugf(ctx, "Intercept name %s", request.InterceptSpec.Name)
	return s.state.PrepareIntercept(ctx, request)
}

func (s *service) GetKnownWorkloadKinds(ctx context.Context, request *rpc.SessionInfo) (*rpc.KnownWorkloadKinds, error) {
	if err := checkCompat(ctx, "GetKnownWorkloadKinds", "2.20.0"); err != nil {
		return nil, err
	}
	ctx = managerutil.WithSessionInfo(ctx, request)
	enabledWorkloadKinds := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	kinds := make([]rpc.WorkloadInfo_Kind, len(enabledWorkloadKinds))
	for i, wlKind := range enabledWorkloadKinds {
		kinds[i] = workload.RpcKind(wlKind)
	}
	return &rpc.KnownWorkloadKinds{Kinds: kinds}, nil
}

func (s *service) EnsureAgent(ctx context.Context, request *rpc.EnsureAgentRequest) (*rpc.AgentInfoSnapshot, error) {
	session := request.GetSession()
	ctx = managerutil.WithSessionInfo(ctx, session)
	sessionID := tunnel.SessionID(session.GetSessionId())
	client := s.state.GetClient(sessionID)
	if client == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}
	as, err := s.state.EnsureAgent(ctx, request.Name, client.Namespace)
	if err != nil {
		return nil, status.Convert(err).Err()
	}
	if len(as) == 0 {
		return nil, status.Errorf(codes.Internal, "failed to ensure agent for workload %s: no agents became active", request.Name)
	}
	rpcAs := make([]*rpc.AgentInfo, len(as))
	for i, a := range as {
		rpcAs[i] = a.AgentInfo
	}
	return &rpc.AgentInfoSnapshot{Agents: rpcAs}, nil
}

// CreateIntercept lets a client create an intercept.
func (s *service) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	ctx = managerutil.WithSessionInfo(ctx, ciReq.GetSession())
	spec := ciReq.InterceptSpec
	dlog.Debugf(ctx, "Intercept name %s", ciReq.InterceptSpec.Name)

	if val := validateIntercept(spec); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	client, interceptInfo, err := s.state.AddIntercept(ctx, ciReq)
	if err != nil {
		return nil, err
	}

	SetGauge(s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, &spec.Name, 1)

	IncrementInterceptCounterFunc(s.state.GetInterceptCounter(), client.Name, client.InstallId, spec)

	return interceptInfo, nil
}

func (s *service) MakeInterceptID(_ context.Context, sessionID string, name string) (string, error) {
	// When something without a session ID (e.g. System A) calls this function,
	// it is sending the intercept ID as the name, so we use that.
	//
	// TODO: Look at cmd/traffic/cmd/manager/internal/state API and see if it makes
	// sense to make more / all functions use intercept ID instead of session ID + name.
	// Or at least functions outside services (e.g. SystemA), which don't know about sessions,
	// use in requests.
	if sessionID == "" {
		return name, nil
	} else {
		if s.state.GetClient(tunnel.SessionID(sessionID)) == nil {
			return "", status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
		}
		return sessionID + ":" + name, nil
	}
}

func (s *service) UpdateIntercept(context.Context, *rpc.UpdateInterceptRequest) (*rpc.InterceptInfo, error) { //nolint:gocognit
	return nil, status.Error(codes.Unimplemented, "")
}

// RemoveIntercept lets a client remove an intercept.
func (s *service) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, riReq.GetSession())
	sessionID := tunnel.SessionID(riReq.GetSession().GetSessionId())
	name := riReq.Name

	dlog.Debugf(ctx, "Intercept name %s", name)

	client := s.state.GetClient(sessionID)
	if client == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	SetGauge(s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, &name, 0)

	s.state.RemoveIntercept(ctx, string(sessionID)+":"+name)
	return &empty.Empty{}, nil
}

// GetIntercept gets an intercept info from intercept name.
func (s *service) GetIntercept(ctx context.Context, request *rpc.GetInterceptRequest) (*rpc.InterceptInfo, error) {
	interceptID, err := s.MakeInterceptID(ctx, request.GetSession().GetSessionId(), request.GetName())
	if err != nil {
		return nil, err
	}
	if intercept, ok := s.state.GetIntercept(interceptID); ok {
		return intercept.InterceptInfo, nil
	} else {
		return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", request.Name)
	}
}

// ReviewIntercept lets an agent approve or reject an intercept.
func (s *service) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, rIReq.GetSession())
	sessionID := tunnel.SessionID(rIReq.GetSession().GetSessionId())
	ceptID := rIReq.Id

	agent := s.state.GetAgent(sessionID)
	if agent == nil {
		return &empty.Empty{}, nil
	}

	if rIReq.Disposition == rpc.InterceptDispositionType_AGENT_ERROR {
		dlog.Errorf(ctx, "%s - %s: %s", ceptID, rIReq.Disposition, rIReq.Message)
	} else {
		dlog.Debugf(ctx, "%s - %s", ceptID, rIReq.Disposition)
	}

	s.removeExcludedEnvVars(rIReq.Environment)

	intercept := s.state.UpdateIntercept(ceptID, func(intercept *state.Intercept) {
		// Sanity check: The reviewing agent must be an agent for the intercept.
		if intercept.Spec.Namespace != agent.Namespace || intercept.Spec.Agent != agent.Name {
			return
		}
		if mutator.GetMap(ctx).IsInactive(types.UID(agent.PodUid)) {
			dlog.Debugf(ctx, "Pod %s(%s) is blacklisted", agent.PodName, agent.PodIp)
			return
		}

		// Only update intercepts in the waiting state.  Agents race to review an intercept, but we
		// expect they will always compatible answers.
		if intercept.Disposition == rpc.InterceptDispositionType_WAITING {
			intercept.Disposition = rIReq.Disposition
			intercept.Message = rIReq.Message
			intercept.PodIp = rIReq.PodIp
			intercept.PodName = agent.PodName
			intercept.FtpPort = rIReq.FtpPort
			intercept.SftpPort = rIReq.SftpPort
			intercept.MountPoint = rIReq.MountPoint
			intercept.MechanismArgsDesc = rIReq.MechanismArgsDesc
			intercept.Headers = rIReq.Headers
			intercept.Metadata = rIReq.Metadata
			intercept.Environment = rIReq.Environment
		}
	})

	if intercept == nil {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

func (s *service) removeExcludedEnvVars(envVars map[string]string) {
	for _, key := range s.configWatcher.GetAgentEnv().Excluded {
		delete(envVars, key)
	}
}

func (s *service) Tunnel(server rpc.Manager_TunnelServer) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, tunnel.ClientToManager, server)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to connect stream: %v", err)
	}
	if a := s.state.GetAgent(stream.SessionID()); a != nil {
		// This is actually an AgentToManager tunnel.
		stream.SetTag(tunnel.AgentToManager)
	}
	return s.state.Tunnel(ctx, stream)
}

func (s *service) WatchDial(session *rpc.SessionInfo, stream rpc.Manager_WatchDialServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	lrCh := s.state.WatchDial(tunnel.SessionID(session.SessionId))
	for {
		select {
		// connection broken
		case <-ctx.Done():
			return nil
		case lr := <-lrCh:
			if lr == nil {
				return nil
			}
			if err := stream.Send(lr); err != nil {
				dlog.Errorf(ctx, "failed to send dial request: %v", err)
				// We couldn't stream the dial request. This likely means
				// that we lost connection.
				return nil
			}
		}
	}
}

// hasDomainSuffix checks if the given name is suffixed with the given suffix. The following
// rules apply:
//
//   - The name must end with a dot.
//   - The suffix may optionally end with a dot.
//   - The suffix may not be empty.
//   - The suffix match must follow after a dot in the name, or match the whole name.
func hasDomainSuffix(name, suffix string) bool {
	sl := len(suffix)
	if sl == 0 {
		return false
	}
	nl := len(name)
	sfp := nl - sl
	if sfp < 0 {
		return false
	}
	if name[nl-1] != '.' {
		return false
	}
	if suffix[sl-1] != '.' {
		if sfp == 0 {
			return false
		}
		sfp--
		name = name[0 : nl-1]
	}
	if sfp == 0 {
		return name == suffix
	}
	return name[sfp-1] == '.' && name[sfp:] == suffix
}

func (s *service) resolveSelfDNS(svcIP net.IP, request *rpc.DNSRequest) (rrs dnsproxy.RRs) {
	qType := uint16(request.Type)
	switch qType {
	case dns2.TypeA:
		rrs = dnsproxy.RRs{&dns2.A{
			Hdr: dns2.RR_Header{
				Name:   request.Name,
				Rrtype: qType,
				Class:  dns2.ClassINET,
			},
			A: svcIP.To4(),
		}}
	case dns2.TypeAAAA:
		var ip net.IP
		if svcIP.To4() == nil {
			ip = svcIP.To16()
		}
		rrs = dnsproxy.RRs{&dns2.AAAA{
			Hdr: dns2.RR_Header{
				Name:   request.Name,
				Rrtype: qType,
				Class:  dns2.ClassINET,
			},
			AAAA: ip,
		}}
	case dns2.TypeCNAME:
		rrs = dnsproxy.RRs{&dns2.CNAME{
			Hdr: dns2.RR_Header{
				Name:   request.Name,
				Rrtype: qType,
				Class:  dns2.ClassINET,
			},
			Target: s.serviceNameFQN,
		}}
	}
	return rrs
}

func (s *service) LookupDNS(ctx context.Context, request *rpc.DNSRequest) (response *rpc.DNSResponse, err error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	qType := uint16(request.Type)
	qtn := dns2.TypeToString[qType]
	var rrs dnsproxy.RRs
	if dlog.MaxLogLevel(ctx) >= dlog.LogLevelDebug {
		defer func() {
			var result string
			switch {
			case err != nil:
				result = err.Error()
			case len(rrs) == 0:
				result = dns2.RcodeToString[int(response.RCode)]
			default:
				result = rrs.String()
			}
			dlog.Debugf(ctx, "LookupDNS: %s %s -> %s", request.Name, qtn, result)
		}()
	}

	if svcIP := s.clusterInfo.ServiceIP(); svcIP != nil && (request.Name == s.serviceNameNs || request.Name == s.serviceNameFQN) {
		rrs = s.resolveSelfDNS(svcIP, request)
		if len(rrs) > 0 {
			return dnsproxy.ToRPC(rrs, dns2.RcodeSuccess)
		}
	}

	sessionID := tunnel.SessionID(request.GetSession().GetSessionId())
	tmNamespace := managerutil.GetEnv(ctx).ManagerNamespace
	noSearchDomain := s.dotClusterDomain
	var rCode int
	switch {
	case request.Name == "tel2-recursion-check.kube-system.":
		rCode = state.RcodeNoAgents
		noSearchDomain = ".kube-system."
	case hasDomainSuffix(request.Name, tmNamespace):
		// It's enough to propagate this one to the traffic-manager
		noSearchDomain = tmNamespace + "."
		rCode = state.RcodeNoAgents
	case strings.HasSuffix(request.Name, s.dotClusterDomain):
		// It's enough to propagate this one to the traffic-manager
		rCode = state.RcodeNoAgents
	default:
		rrs, rCode, err = s.state.AgentsLookupDNS(ctx, sessionID, request)
		if err != nil {
			dlog.Errorf(ctx, "AgentsLookupDNS %s %s: %v", request.Name, qtn, err)
		} else if rCode != state.RcodeNoAgents {
			if len(rrs) == 0 {
				dlog.Tracef(ctx, "LookupDNS on agents: %s %s -> %s", request.Name, qtn, dns2.RcodeToString[rCode])
			} else {
				dlog.Tracef(ctx, "LookupDNS on agents: %s %s -> %s", request.Name, qtn, rrs)
			}
		}
	}

	if rCode == state.RcodeNoAgents {
		client := s.state.GetClient(sessionID)
		name := request.Name
		restoreName := false
		nDots := 0
		if client != nil {
			for _, c := range name {
				if c == '.' {
					nDots++
				}
			}
			if nDots == 1 && client.Namespace != tmNamespace {
				noSearchDomain = client.Namespace + "."
				name += noSearchDomain
				restoreName = true
			}
		}
		dlog.Tracef(ctx, "LookupDNS on traffic-manager: %s", name)
		rrs, rCode, err = dnsproxy.Lookup(ctx, qType, name, noSearchDomain)
		if err != nil {
			// Could still be x.y.<client namespace>, but let's avoid x.<cluster domain>.<client namespace> and x.<client-namespace>.<client namespace>
			if client != nil && nDots > 1 && client.Namespace != tmNamespace && !strings.HasSuffix(name, s.dotClusterDomain) && !hasDomainSuffix(name, client.Namespace) {
				name += client.Namespace + "."
				restoreName = true
				dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s", name)
				rrs, rCode, err = dnsproxy.Lookup(ctx, qType, name, noSearchDomain)
			}
			if err != nil {
				dlog.Tracef(ctx, "LookupDNS on traffic-manager: %s %s -> %s %s", request.Name, qtn, dns2.RcodeToString[rCode], err)
				return nil, err
			}
		}
		if len(rrs) == 0 {
			dlog.Tracef(ctx, "LookupDNS on traffic-manager: %s %s -> %s", request.Name, qtn, dns2.RcodeToString[rCode])
		} else {
			if restoreName {
				dlog.Tracef(ctx, "LookupDNS on traffic-manager: restore %s to %s", name, request.Name)
				for _, rr := range rrs {
					rr.Header().Name = request.Name
				}
			}
			dlog.Tracef(ctx, "LookupDNS on traffic-manager: %s %s -> %s", request.Name, qtn, rrs)
		}
	}
	return dnsproxy.ToRPC(rrs, rCode)
}

func (s *service) AgentLookupDNSResponse(ctx context.Context, response *rpc.DNSAgentResponse) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, response.GetSession())
	dlog.Debugf(ctx, "name: %s", response.Request.Name)
	s.state.PostLookupDNSResponse(ctx, response)
	return &empty.Empty{}, nil
}

func (s *service) WatchLookupDNS(session *rpc.SessionInfo, stream rpc.Manager_WatchLookupDNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	rqCh := s.state.WatchLookupDNS(tunnel.SessionID(session.SessionId))
	for {
		select {
		case <-ctx.Done():
			return nil
		case rq := <-rqCh:
			if rq == nil {
				return nil
			}
			if err := stream.Send(rq); err != nil {
				dlog.Errorf(ctx, "WatchLookupDNS.Send() failed: %v", err)
				return nil
			}
		}
	}
}

// GetLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// GetLogsRequest and returns them to the caller
// Deprecated: Clients should use the user daemon's GatherLogs method.
func (s *service) GetLogs(_ context.Context, _ *rpc.GetLogsRequest) (*rpc.LogsResponse, error) {
	return &rpc.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
		ErrMsg:  "traffic-manager.GetLogs is deprecated. Please upgrade your telepresence client",
	}, nil
}

func (s *service) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	s.state.SetTempLogLevel(ctx, request)
	return &empty.Empty{}, nil
}

func (s *service) UninstallAgents(ctx context.Context, request *rpc.UninstallAgentsRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSessionInfo())
	dlog.Debugf(ctx, "%s", request.Agents)
	return &empty.Empty{}, s.state.UninstallAgents(ctx, request)
}

func (s *service) WatchLogLevel(_ *empty.Empty, stream rpc.Manager_WatchLogLevelServer) error {
	return s.state.WaitForTempLogLevel(stream)
}

func (s *service) WatchClusterInfo(session *rpc.SessionInfo, stream rpc.Manager_WatchClusterInfoServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	return s.clusterInfo.Watch(ctx, stream)
}

func (s *service) WatchWorkloads(request *rpc.WorkloadEventsRequest, stream rpc.Manager_WatchWorkloadsServer) (err error) {
	ctx := stream.Context()
	// Dysfunctional prior to 2.21.0 because no initial snapshot was sent.
	if err := checkCompat(ctx, "WatchWorkloads", "2.21.0-alpha.4"); err != nil {
		return err
	}
	ctx = managerutil.WithSessionInfo(ctx, request.SessionInfo)
	defer func() {
		if r := recover(); r != nil {
			err = derror.PanicToError(r)
			dlog.Errorf(ctx, "WatchWorkloads panic: %+v", err)
			err = status.Error(codes.Internal, err.Error())
		}
	}()
	dlog.Debugf(ctx, "Namespace %q", request.Namespace)

	if request.SessionInfo == nil {
		return status.Error(codes.InvalidArgument, "SessionInfo is required")
	}
	clientSession := tunnel.SessionID(request.SessionInfo.SessionId)
	namespace := request.Namespace
	if namespace == "" {
		clientInfo := s.state.GetClient(clientSession)
		if clientInfo == nil {
			return status.Errorf(codes.NotFound, "Client session %q not found", clientSession)
		}
		namespace = clientInfo.Namespace
	} else if !s.State().ManagesNamespace(ctx, namespace) {
		return status.Error(codes.FailedPrecondition, fmt.Sprintf("namespace %s is not managed", namespace))
	}
	ww := s.state.NewWorkloadInfoWatcher(clientSession, namespace)
	return ww.Watch(ctx, stream)
}

const agentSessionTTL = 15 * time.Second

// expire removes stale sessions.
func (s *service) expire(ctx context.Context) {
	now := s.clock.Now()
	s.state.ExpireSessions(ctx, now.Add(-managerutil.GetEnv(ctx).ClientConnectionTTL), now.Add(-agentSessionTTL))
}
