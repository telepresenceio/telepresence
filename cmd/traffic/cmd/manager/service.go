package manager

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"time"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	dns2 "github.com/miekg/dns"
	"golang.org/x/exp/maps"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
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
	ctx                context.Context
	clock              Clock
	id                 string
	state              state.State
	clusterInfo        cluster.Info
	configWatcher      config.Watcher
	activeHttpRequests int32
	activeGrpcRequests int32

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
	ret.ctx = ctx
	// These are context dependent so build them once the pool is up
	ret.clusterInfo = cluster.NewInfo(ctx)
	ret.state = state.NewStateFunc(ctx)
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
	dlog.Debug(ctx, "GetAgentConfig called")
	ctx = managerutil.WithSessionInfo(ctx, request.Session)
	sessionID := request.GetSession().GetSessionId()
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
	dlog.Debugf(ctx, "ArriveAsClient called, namespace: %s", client.Namespace)

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
		SessionId:        s.state.AddClient(client, s.clock.Now()),
		ManagerInstallId: s.clusterInfo.ID(),
		InstallId:        &installId,
	}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (s *service) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	dlog.Debugf(ctx, "ArriveAsAgent %s called", agent.PodName)

	if val := validateAgent(agent); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}
	mutator.GetMap(ctx).Whitelist(agent.PodName, agent.Namespace)

	for _, cn := range agent.Containers {
		s.removeExcludedEnvVars(cn.Environment)
	}
	sessionID := s.state.AddAgent(agent, s.clock.Now())

	return &rpc.SessionInfo{
		SessionId:        sessionID,
		ManagerInstallId: s.clusterInfo.ID(),
	}, nil
}

func (s *service) ReportMetrics(ctx context.Context, metrics *rpc.TunnelMetrics) (*empty.Empty, error) {
	s.state.AddSessionConsumptionMetrics(metrics)
	return &empty.Empty{}, nil
}

func (s *service) GetClientConfig(ctx context.Context, _ *empty.Empty) (*rpc.CLIConfig, error) {
	dlog.Debug(ctx, "GetClientConfig called")

	return &rpc.CLIConfig{
		ConfigYaml: s.configWatcher.GetClientConfigYaml(ctx),
	}, nil
}

// Remain indicates that the session is still valid.
func (s *service) Remain(ctx context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	// ctx = WithSessionInfo(ctx, req.GetSession())
	// dlog.Debug(ctx, "Remain called")
	sessionID := req.GetSession().GetSessionId()
	if ok := s.state.MarkSession(req, s.clock.Now()); !ok {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}

	s.state.RefreshSessionConsumptionMetrics(sessionID)

	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (s *service) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, session)
	sessionID := session.GetSessionId()
	dlog.Debug(ctx, "Depart called")

	// There's reason for the caller to wait for this removal to complete.
	go s.state.RemoveSession(context.WithoutCancel(ctx), sessionID)
	return &empty.Empty{}, nil
}

// WatchAgentPods notifies a client of the set of known Agents.
func (s *service) WatchAgentPods(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentPodsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debug(ctx, "WatchAgentPods called")
	defer dlog.Debug(ctx, "WatchAgentPods ended")

	clientSession := session.SessionId
	clientInfo := s.state.GetClient(clientSession)
	if clientInfo == nil {
		return status.Errorf(codes.NotFound, "Client session %q not found", clientSession)
	}
	ns := clientInfo.Namespace

	agentsCh := s.state.WatchAgents(ctx, func(_ string, info *rpc.AgentInfo) bool {
		return info.Namespace == ns
	})
	interceptsCh := s.state.WatchIntercepts(ctx, func(_ string, info *rpc.InterceptInfo) bool {
		return info.ClientSession.SessionId == clientSession
	})
	sessionDone, err := s.state.SessionDone(clientSession)
	if err != nil {
		return err
	}

	var interceptInfos map[string]*rpc.InterceptInfo
	isIntercepted := func(name, namespace string) bool {
		for _, ii := range interceptInfos {
			if name == ii.Spec.Agent && namespace == ii.Spec.Namespace {
				return true
			}
		}
		return false
	}
	var agents []*rpc.AgentPodInfo
	var agentNames []string
	for {
		select {
		case <-sessionDone:
			// Manager believes this session has ended.
			return nil
		case as, ok := <-agentsCh:
			if !ok {
				return nil
			}
			agm := as.State
			agents = make([]*rpc.AgentPodInfo, len(agm))
			agentNames = make([]string, len(agm))
			i := 0
			for _, a := range agm {
				aip, err := netip.ParseAddr(a.PodIp)
				if err != nil {
					dlog.Errorf(ctx, "error parsing agent pod ip %q: %v", a.PodIp, err)
				}
				agents[i] = &rpc.AgentPodInfo{
					WorkloadName: a.Name,
					PodName:      a.PodName,
					Namespace:    a.Namespace,
					PodIp:        aip.AsSlice(),
					ApiPort:      a.ApiPort,
					Intercepted:  isIntercepted(a.Name, a.Namespace),
				}
				agentNames[i] = a.Name
				i++
			}
		case is, ok := <-interceptsCh:
			if !ok {
				return nil
			}
			interceptInfos = is.State
			for i, a := range agents {
				a.Intercepted = isIntercepted(agentNames[i], a.Namespace)
			}
		}
		if agents != nil {
			if err = stream.Send(&rpc.AgentPodInfoSnapshot{Agents: agents}); err != nil {
				return err
			}
		}
	}
}

// WatchAgents notifies a client of the set of known Agents in the connected namespace.
func (s *service) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debug(ctx, "WatchAgents called")
	clientInfo := s.state.GetClient(session.SessionId)
	if clientInfo == nil {
		return status.Errorf(codes.NotFound, "Client session %q not found", session.SessionId)
	}
	ns := clientInfo.Namespace
	return s.watchAgents(ctx, func(_ string, a *rpc.AgentInfo) bool { return a.Namespace == ns }, stream)
}

// WatchAgentsNS notifies a client of the set of known Agents in the namespaces given in the request.
func (s *service) WatchAgentsNS(request *rpc.AgentsRequest, stream rpc.Manager_WatchAgentsNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), request.Session)
	dlog.Debug(ctx, "WatchAgentsNS called")
	return s.watchAgents(ctx, func(_ string, a *rpc.AgentInfo) bool { return slices.Contains(request.Namespaces, a.Namespace) }, stream)
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

func (s *service) watchAgents(ctx context.Context, includeAgent func(string, *rpc.AgentInfo) bool, stream rpc.Manager_WatchAgentsServer) error {
	snapshotCh := s.state.WatchAgents(ctx, includeAgent)
	sessionDone, err := s.state.SessionDone(managerutil.GetSessionID(ctx))
	if err != nil {
		return err
	}

	// Ensure that the initial snapshot is not equal to lastSnap even if it is empty by
	// creating a lastSnap with one nil entry.
	lastSnap := make([]*rpc.AgentInfo, 1)

	for {
		select {
		case snapshot, ok := <-snapshotCh:
			if !ok {
				// The request has been canceled.
				dlog.Debug(ctx, "WatchAgentsNS request cancelled")
				return nil
			}
			agentSessionIDs := maps.Keys(snapshot.State)
			sort.Strings(agentSessionIDs)
			agents := make([]*rpc.AgentInfo, len(agentSessionIDs))
			for i, agentSessionID := range agentSessionIDs {
				agents[i] = snapshot.State[agentSessionID]
			}
			if slices.EqualFunc(agents, lastSnap, infosEqual) {
				continue
			}
			lastSnap = agents
			if dlog.MaxLogLevel(ctx) >= dlog.LogLevelDebug {
				names := make([]string, len(agents))
				i := 0
				for _, a := range agents {
					names[i] = a.Name + "." + a.Namespace
					i++
				}
				dlog.Debugf(ctx, "WatchAgentsNS sending update %v", names)
			}
			resp := &rpc.AgentInfoSnapshot{
				Agents: agents,
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
		case <-sessionDone:
			// Manager believes this session has ended.
			dlog.Debug(ctx, "WatchAgentsNS session cancelled")
			return nil
		}
	}
}

// WatchIntercepts notifies a client or agent of the set of intercepts
// relevant to that client or agent.
func (s *service) WatchIntercepts(session *rpc.SessionInfo, stream rpc.Manager_WatchInterceptsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	sessionID := session.GetSessionId()

	dlog.Debug(ctx, "WatchIntercepts called")

	var sessionDone <-chan struct{}
	var filter func(id string, info *rpc.InterceptInfo) bool
	if sessionID == "" {
		filter = func(id string, info *rpc.InterceptInfo) bool {
			return info.Disposition != rpc.InterceptDispositionType_REMOVED && !state.IsChildIntercept(info.Spec)
		}
	} else {
		var err error
		if sessionDone, err = s.state.SessionDone(sessionID); err != nil {
			return err
		}

		if agent := s.state.GetAgent(sessionID); agent != nil {
			// sessionID refers to an agent session. Include everything for the agent, including pod-port children.
			filter = func(id string, info *rpc.InterceptInfo) bool {
				if info.Spec.Namespace != agent.Namespace || info.Spec.Agent != agent.Name {
					// Don't return intercepts for different agents.
					return false
				}
				// Don't return intercepts that aren't in a "agent-owned" state.
				switch info.Disposition {
				case rpc.InterceptDispositionType_WAITING,
					rpc.InterceptDispositionType_ACTIVE,
					rpc.InterceptDispositionType_AGENT_ERROR:
					// agent-owned state: include the intercept
					dlog.Debugf(ctx, "Intercept %s.%s valid. Disposition: %s", info.Spec.Agent, info.Spec.Namespace, info.Disposition)
					return true
				case rpc.InterceptDispositionType_REMOVED:
					dlog.Debugf(ctx, "Intercept %s.%s valid but removed", info.Spec.Agent, info.Spec.Namespace)
					return true
				default:
					// otherwise: don't return this intercept
					dlog.Debugf(ctx, "Intercept %s.%s is not in agent-owned state. Disposition: %s", info.Spec.Agent, info.Spec.Namespace, info.Disposition)
					return false
				}
			}
		} else {
			// sessionID refers to a client session.
			filter = func(id string, info *rpc.InterceptInfo) bool {
				return info.ClientSession.SessionId == sessionID &&
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
				dlog.Debugf(ctx, "WatchIntercepts request cancelled")
				return nil
			}
			dlog.Debugf(ctx, "WatchIntercepts sending update")
			intercepts := make([]*rpc.InterceptInfo, 0, len(snapshot.State))
			for _, intercept := range snapshot.State {
				intercepts = append(intercepts, intercept)
			}
			resp := &rpc.InterceptInfoSnapshot{
				Intercepts: intercepts,
			}
			sort.Slice(intercepts, func(i, j int) bool {
				return intercepts[i].Id < intercepts[j].Id
			})
			if err := stream.Send(resp); err != nil {
				dlog.Debugf(ctx, "WatchIntercepts encountered a write error: %v", err)
				return err
			}
		case <-ctx.Done():
			dlog.Debugf(ctx, "WatchIntercepts context cancelled")
			return nil
		case <-sessionDone:
			dlog.Debugf(ctx, "WatchIntercepts session cancelled")
			return nil
		}
	}
}

func (s *service) PrepareIntercept(ctx context.Context, request *rpc.CreateInterceptRequest) (pi *rpc.PreparedIntercept, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = derror.PanicToError(r)
			dlog.Errorf(ctx, "%+v", err)
		}
	}()
	ctx = managerutil.WithSessionInfo(ctx, request.Session)
	dlog.Debugf(ctx, "PrepareIntercept %s called", request.InterceptSpec.Name)
	return s.state.PrepareIntercept(ctx, request)
}

func (s *service) GetKnownWorkloadKinds(ctx context.Context, request *rpc.SessionInfo) (*rpc.KnownWorkloadKinds, error) {
	if err := checkCompat(ctx, "GetKnownWorkloadKinds", "2.20.0"); err != nil {
		return nil, err
	}
	ctx = managerutil.WithSessionInfo(ctx, request)
	dlog.Debugf(ctx, "GetKnownWorkloadKinds called")
	enabledWorkloadKinds := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	kinds := make([]rpc.WorkloadInfo_Kind, len(enabledWorkloadKinds))
	for i, wlKind := range enabledWorkloadKinds {
		switch wlKind {
		case workload.DeploymentKind:
			kinds[i] = rpc.WorkloadInfo_DEPLOYMENT
		case workload.ReplicaSetKind:
			kinds[i] = rpc.WorkloadInfo_REPLICASET
		case workload.StatefulSetKind:
			kinds[i] = rpc.WorkloadInfo_STATEFULSET
		case workload.RolloutKind:
			kinds[i] = rpc.WorkloadInfo_ROLLOUT
		}
	}
	return &rpc.KnownWorkloadKinds{Kinds: kinds}, nil
}

func (s *service) EnsureAgent(ctx context.Context, request *rpc.EnsureAgentRequest) (*rpc.AgentInfoSnapshot, error) {
	session := request.GetSession()
	ctx = managerutil.WithSessionInfo(ctx, session)
	dlog.Debugf(ctx, "EnsureAgent called")
	sessionID := session.GetSessionId()
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
	return &rpc.AgentInfoSnapshot{Agents: as}, nil
}

// CreateIntercept lets a client create an intercept.
func (s *service) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	ctx = managerutil.WithSessionInfo(ctx, ciReq.GetSession())
	spec := ciReq.InterceptSpec
	dlog.Debugf(ctx, "CreateIntercept %s called", ciReq.InterceptSpec.Name)

	if val := validateIntercept(spec); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	if ciReq.InterceptSpec.Replace {
		_, err := s.state.PrepareIntercept(ctx, ciReq)
		if err != nil {
			return nil, err
		}
	}

	client, interceptInfo, err := s.state.AddIntercept(ctx, ciReq)
	if err != nil {
		return nil, err
	}

	if ciReq.InterceptSpec.Replace {
		err = s.state.AddInterceptFinalizer(interceptInfo.Id, s.state.RestoreAppContainer)
		if err != nil {
			// The intercept's been created but we can't finalize it...
			dlog.Errorf(ctx, "Failed to add finalizer for %s: %v", interceptInfo.Id, err)
		}
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
		if s.state.GetClient(sessionID) == nil {
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
	sessionID := riReq.GetSession().GetSessionId()
	name := riReq.Name

	dlog.Debugf(ctx, "RemoveIntercept called: %s", name)

	client := s.state.GetClient(sessionID)
	if client == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	SetGauge(s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, &name, 0)

	s.state.RemoveIntercept(ctx, sessionID+":"+name)
	return &empty.Empty{}, nil
}

// GetIntercept gets an intercept info from intercept name.
func (s *service) GetIntercept(ctx context.Context, request *rpc.GetInterceptRequest) (*rpc.InterceptInfo, error) {
	interceptID, err := s.MakeInterceptID(ctx, request.GetSession().GetSessionId(), request.GetName())
	if err != nil {
		return nil, err
	}
	if intercept, ok := s.state.GetIntercept(interceptID); ok {
		return intercept, nil
	} else {
		return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", request.Name)
	}
}

// ReviewIntercept lets an agent approve or reject an intercept.
func (s *service) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, rIReq.GetSession())
	sessionID := rIReq.GetSession().GetSessionId()
	ceptID := rIReq.Id

	if rIReq.Disposition == rpc.InterceptDispositionType_AGENT_ERROR {
		dlog.Errorf(ctx, "ReviewIntercept called: %s - %s: %s", ceptID, rIReq.Disposition, rIReq.Message)
	} else {
		dlog.Debugf(ctx, "ReviewIntercept called: %s - %s", ceptID, rIReq.Disposition)
	}

	agent := s.state.GetActiveAgent(sessionID)
	if agent == nil {
		return &empty.Empty{}, nil
	}

	s.removeExcludedEnvVars(rIReq.Environment)

	intercept := s.state.UpdateIntercept(ceptID, func(intercept *rpc.InterceptInfo) {
		// Sanity check: The reviewing agent must be an agent for the intercept.
		if intercept.Spec.Namespace != agent.Namespace || intercept.Spec.Agent != agent.Name {
			return
		}
		if mutator.GetMap(ctx).IsBlacklisted(agent.PodName, agent.Namespace) {
			dlog.Debugf(ctx, "Pod %s.%s is blacklisted", agent.PodName, agent.Namespace)
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
	stream, err := tunnel.NewServerStream(ctx, server)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to connect stream: %v", err)
	}
	return s.state.Tunnel(ctx, stream)
}

func (s *service) WatchDial(session *rpc.SessionInfo, stream rpc.Manager_WatchDialServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchDial called")
	lrCh := s.state.WatchDial(session.SessionId)
	for {
		select {
		// connection broken
		case <-ctx.Done():
			return nil
		// service stopped
		case <-s.ctx.Done():
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

func (s *service) LookupDNS(ctx context.Context, request *rpc.DNSRequest) (*rpc.DNSResponse, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	qType := uint16(request.Type)
	qtn := dns2.TypeToString[qType]
	dlog.Debugf(ctx, "LookupDNS %s %s", request.Name, qtn)

	rrs, rCode, err := s.state.AgentsLookupDNS(ctx, request.GetSession().GetSessionId(), request)
	if err != nil {
		dlog.Errorf(ctx, "AgentsLookupDNS %s %s: %v", request.Name, qtn, err)
	} else if rCode != state.RcodeNoAgents {
		if len(rrs) == 0 {
			dlog.Debugf(ctx, "LookupDNS on agents: %s %s -> %s", request.Name, qtn, dns2.RcodeToString[rCode])
		} else {
			dlog.Debugf(ctx, "LookupDNS on agents: %s %s -> %s", request.Name, qtn, rrs)
		}
	}
	if rCode == state.RcodeNoAgents {
		tmNamespace := managerutil.GetEnv(ctx).ManagerNamespace
		client := s.state.GetClient(request.GetSession().GetSessionId())
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
				name += client.Namespace + "."
				restoreName = true
			}
		}
		dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s", name)
		rrs, rCode, err = dnsproxy.Lookup(ctx, qType, name)
		if err != nil {
			// Could still be x.y.<client namespace>, but let's avoid x.<cluster domain>.<client namespace> and x.<client-namespace>.<client namespace>
			if client != nil && nDots > 1 && client.Namespace != tmNamespace && !hasDomainSuffix(name, s.ClusterInfo().ClusterDomain()) && !hasDomainSuffix(name, client.Namespace) {
				name += client.Namespace + "."
				restoreName = true
				dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s", name)
				rrs, rCode, err = dnsproxy.Lookup(ctx, qType, name)
			}
			if err != nil {
				dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s %s -> %s %s", request.Name, qtn, dns2.RcodeToString[rCode], err)
				return nil, err
			}
		}
		if len(rrs) == 0 {
			dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s %s -> %s", request.Name, qtn, dns2.RcodeToString[rCode])
		} else {
			if restoreName {
				dlog.Debugf(ctx, "LookupDNS on traffic-manager: restore %s to %s", name, request.Name)
				for _, rr := range rrs {
					rr.Header().Name = request.Name
				}
			}
			dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s %s -> %s", request.Name, qtn, rrs)
		}
	}
	return dnsproxy.ToRPC(rrs, rCode)
}

func (s *service) AgentLookupDNSResponse(ctx context.Context, response *rpc.DNSAgentResponse) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, response.GetSession())
	dlog.Debugf(ctx, "AgentLookupDNSResponse called %s", response.Request.Name)
	s.state.PostLookupDNSResponse(ctx, response)
	return &empty.Empty{}, nil
}

func (s *service) WatchLookupDNS(session *rpc.SessionInfo, stream rpc.Manager_WatchLookupDNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchLookupDNS called")
	rqCh := s.state.WatchLookupDNS(session.SessionId)
	for {
		select {
		case <-s.ctx.Done():
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

func (s *service) WatchLogLevel(_ *empty.Empty, stream rpc.Manager_WatchLogLevelServer) error {
	dlog.Debugf(stream.Context(), "WatchLogLevel called")
	return s.state.WaitForTempLogLevel(stream)
}

func (s *service) WatchClusterInfo(session *rpc.SessionInfo, stream rpc.Manager_WatchClusterInfoServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchClusterInfo called")
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
		dlog.Debugf(ctx, "WatchWorkloads ended")
	}()
	dlog.Debugf(ctx, "WatchWorkloads called")

	if request.SessionInfo == nil {
		return status.Error(codes.InvalidArgument, "SessionInfo is required")
	}
	clientSession := request.SessionInfo.SessionId
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
