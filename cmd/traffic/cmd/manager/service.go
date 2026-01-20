package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	dns2 "github.com/miekg/dns"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	maps2 "github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/tmconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

type Service interface {
	rpc.ManagerServer
	ID() string
	InstallID() string
	MakeInterceptID(context.Context, string, string) (string, error)
	RegisterServers(*grpc.Server)
	State() *state.State
	ClusterInfo() cluster.Info

	// unexported methods.
	runUpdateTrafficManagerConfigMapLoop(context.Context) error
	serveHTTP(context.Context) error
	servePrometheus(context.Context) error
}

type service struct {
	id                 string
	state              *state.State
	clusterInfo        cluster.Info
	configWatcher      config.Watcher
	activeHttpRequests int32
	activeGrpcRequests int32
	serviceNameNs      string
	serviceNameFQN     string
	dotClusterDomain   string
	tmConfigMapUpdated atomic.Bool

	rpc.UnsafeManagerServer
}

var _ rpc.ManagerServer = &service{}

// checkCompat checks if a CompatibilityVersion has been set for this traffic-manager, and if so, errors with
// an Unimplemented error mentioning the given name if it is less than the required version.
func checkCompat(ctx context.Context, name, requiredVersion string) error {
	if cv := managerutil.GetEnv(ctx).CompatibilityVersion; cv != nil && cv.Compare(semver.MustParse(requiredVersion)) < 0 {
		return status.Error(codes.Unimplemented, fmt.Sprintf("traffic manager of version %s does not implement %s", cv, name))
	}
	return nil
}

func NewService(ctx context.Context, g log.Group, configWatcher config.Watcher) (Service, error) {
	ret := &service{
		id:            uuid.New().String(),
		configWatcher: configWatcher,
	}

	// These are context-dependent, so build them once the pool is up
	var err error
	ret.clusterInfo, err = cluster.NewInfo(ctx)
	if err != nil {
		clog.Errorf(ctx, "unable to initialize cluster info: %v", err)
		return nil, err
	}
	ns := managerutil.GetEnv(ctx).ManagerNamespace
	ret.dotClusterDomain = "." + ret.clusterInfo.ClusterDomain()
	ret.serviceNameNs = fmt.Sprintf("%s.%s.", agentconfig.ManagerAppName, ns)
	ret.serviceNameFQN = fmt.Sprintf("%s.%s.svc%s", agentconfig.ManagerAppName, ns, ret.dotClusterDomain)

	ret.state = state.NewState(ctx, g, configWatcher.AdminCommandChannel())
	return ret, nil
}

func (s *service) ClusterInfo() cluster.Info {
	return s.clusterInfo
}

func (s *service) ID() string {
	return s.id
}

func (s *service) State() *state.State {
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
	ctx, clientInfo, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
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

// GetTelepresenceAPI returns information about the TelepresenceAPI server.
func (s *service) GetTelepresenceAPI(ctx context.Context, e *empty.Empty) (*rpc.TelepresenceAPIInfo, error) {
	env := managerutil.GetEnv(ctx)
	return &rpc.TelepresenceAPIInfo{Port: int32(env.AgentRestApiPort)}, nil
}

// ArriveAsClient establishes a session between a client and the Manager.
func (s *service) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	clog.Debugf(ctx, "Namespace: %s", client.Namespace)

	if !s.State().ManagesNamespace(ctx, client.Namespace) {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("namespace %s is not managed", client.Namespace))
	}

	if val := validateClient(client); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	installId := client.GetInstallId()

	IncrementCounter(ctx, s.state.GetConnectCounter(), client.Name, client.InstallId)
	SetGauge(ctx, s.state.GetConnectActiveStatus(), client.Name, client.InstallId, nil, 1)

	return &rpc.SessionInfo{
		SessionId:        string(s.state.AddClient(client, time.Now())),
		ManagerInstallId: s.clusterInfo.ID(),
		InstallId:        &installId,
	}, nil
}

func (s *service) ReconnectClient(ctx context.Context, info *rpc.ReconnectClientRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, info.Session)
	sessionID := tunnel.SessionID(info.GetSession().GetSessionId())
	if s.state.GetClient(sessionID) != nil {
		// We already know this client, so we don't need to do anything.
		return &empty.Empty{}, nil
	}
	client := info.Client
	st := s.state
	if !st.ManagesNamespace(ctx, client.Namespace) {
		// Sorry, we no longer manage this namespace.
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("namespace %s is not managed", client.Namespace))
	}
	if val := validateClient(client); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}
	now := time.Now()
	st.RestoreClient(sessionID, client, now)
	st.RestoreAgents(info.Agents, now)
	st.RestoreIntercepts(ctx, info.Intercepts, now)
	return &empty.Empty{}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (s *service) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	clog.Debugf(ctx, "Name %s, IP %s", agent.PodName, agent.PodIp)
	if val := validateAgent(agent); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	for _, cn := range agent.Containers {
		s.removeExcludedEnvVars(cn.Environment)
	}

	sessionID, err := s.state.AddAgent(ctx, agent, time.Now())
	if err != nil {
		return nil, err
	}

	return &rpc.SessionInfo{
		SessionId:        string(sessionID),
		ManagerInstallId: s.clusterInfo.ID(),
	}, nil
}

func (s *service) ReconnectAgent(ctx context.Context, rq *rpc.ReconnectAgentRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, rq.Session)
	_, err := s.state.RestoreAgent(ctx, tunnel.SessionID(rq.GetSession().SessionId), rq.Agent, time.Now())
	return &empty.Empty{}, err
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
	ctx = managerutil.WithSessionInfo(ctx, req.GetSession())
	sessionID := managerutil.GetSessionID(ctx)
	agent := s.state.GetAgent(sessionID)
	if agent == nil {
		client := s.state.GetClient(sessionID)
		if client == nil {
			return nil, status.Errorf(codes.NotFound, "Session %q not found", sessionID)
		}
		var lastActivity time.Time
		if la := req.LastActivity; la != nil {
			lastActivity = la.AsTime()
		} else {
			lastActivity = time.Now()
		}
		if client.Mark(lastActivity) {
			clog.Tracef(ctx, "Last activity: %s", lastActivity)
		}
		client.ConsumptionMetrics().AddTimeSpent()
		return &empty.Empty{}, nil
	}

	agent.Mark(time.Now())
	workloadKey := &mutator.WorkloadKey{
		Name:      agent.Name,
		Namespace: agent.Namespace,
		Kind:      k8sapi.Kind(agent.Kind),
	}

	err := s.UpdateLastEngagementTime(ctx, workloadKey)
	if err != nil {
		clog.Errorf(ctx, "error updating last engagement time: %v", err)
	}
	err = s.removeUnusedAgent(ctx, workloadKey)
	if err != nil {
		clog.Errorf(ctx, "error removing unused agent: %v", err)
	}

	return &empty.Empty{}, err
}

// Depart terminates a session.
func (s *service) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, session)

	sessionID := tunnel.SessionID(session.GetSessionId())
	// There's no reason for the caller to wait for this removal to complete.
	go s.state.RemoveSession(context.WithoutCancel(ctx), sessionID)
	return &empty.Empty{}, nil
}

func (s *service) removeUnusedAgent(ctx context.Context, workloadKey *mutator.WorkloadKey) error {
	maxIdleTime := managerutil.GetEnv(ctx).AgentMaxIdleTime
	if maxIdleTime == 0 {
		// default aka not set is 0, we don't ever remove agents, skip
		return nil
	}

	activeIntercepts := s.state.CountActiveInterceptsForWorkload(workloadKey)
	if activeIntercepts != 0 {
		// don't remove if there are active intercepts
		return nil
	}

	agentStateFileYAML := s.configWatcher.GetAgentStateYaml(ctx)
	var agentStateFile AgentStateFile
	err := yaml.Unmarshal(agentStateFileYAML, &agentStateFile)
	if err != nil {
		return fmt.Errorf("err in unmarshalling YAML: %w", err)
	}

	agentState, ok := agentStateFile.AgentStates[*workloadKey]
	if !ok {
		return fmt.Errorf("error in looking up workload key %s in agentStateFile, err: %w", workloadKey, err)
	}
	lastEngagementTime := agentState.LastEngagementTime
	clog.Tracef(ctx, "Last engagement time for agent %s: %s", *workloadKey, lastEngagementTime)
	if lastEngagementTime.IsZero() {
		// means it was never engaged
		return nil
	}
	idleTime := time.Since(lastEngagementTime)
	clog.Tracef(ctx, "Idle time for agent %s: %s", *workloadKey, idleTime)
	if idleTime > maxIdleTime {
		// construct uninstall agents request
		clog.Infof(ctx, "Removing agent %s due to idle time %s exceeding max idle time %s", *workloadKey, idleTime, maxIdleTime)
		ns := workloadKey.Namespace
		mm := mutator.GetMap(ctx)
		wl, err := k8sapi.GetWorkload(ctx, workloadKey.Name, ns, "")
		if err != nil {
			return errors.Errorf(codes.NotFound, "Workload %s.%s not found", workloadKey.Name, ns)
		}
		mm.Delete(wl.GetName(), ns)
		if err := mm.EvictPodsWithAgentConfig(ctx, wl); err != nil {
			return errors.Errorf(codes.Internal, "unable to delete agent for workload %s.%s: %v", wl.GetName(), ns, err)
		}
	}
	return nil
}

func (s *service) UpdateLastEngagementTime(ctx context.Context, workloadKey *mutator.WorkloadKey) error {
	// updates last engagement time IN MEMORY, this is persisted to the configmap by another goroutine that runs periodically
	clog.Tracef(ctx, "Logging workloadKey for last engagement time: %s", *workloadKey)

	agentStateFileYAML := s.configWatcher.GetAgentStateYaml(ctx)
	clog.Tracef(ctx, "Logging agentStateFileYAML: %s", agentStateFileYAML)

	var agentStateFile AgentStateFile
	if string(agentStateFileYAML) != "" {
		clog.Tracef(ctx, "Unmarshalling agent states from YAML")
		err := yaml.Unmarshal(agentStateFileYAML, &agentStateFile)
		if err != nil {
			return fmt.Errorf("error unmarshalling agent states: %w", err)
		}
		if !agentStateFile.AgentStates[*workloadKey].LastEngagementTime.IsZero() && s.state.CountActiveInterceptsForWorkload(workloadKey) == 0 {
			// don't update last engagement time if there are no active intercepts and it is not the first time
			return nil
		}
	} else {
		agentStateFile = AgentStateFile{AgentStates: make(map[mutator.WorkloadKey]AgentState)}
	}

	agentStateFile.AgentStates[*workloadKey] = AgentState{LastEngagementTime: time.Now()}

	updatedAgentStateFileYAML, err := yaml.Marshal(agentStateFile)
	if err != nil {
		return fmt.Errorf("error marshalling agent states: %w", err)
	}
	s.configWatcher.SetAgentStateYaml(ctx, updatedAgentStateFileYAML)
	s.tmConfigMapUpdated.Store(true)
	return nil
}

type AgentState struct {
	LastEngagementTime time.Time `json:"lastEngagementTime"`
}
type AgentStateFile struct {
	AgentStates map[mutator.WorkloadKey]AgentState `json:"agentStates"`
}

func (f AgentStateFile) MarshalJSON() ([]byte, error) {
	temp := make(map[string]AgentState)
	for k, v := range f.AgentStates {
		temp[k.String()] = v
	}
	return json.Marshal(map[string]interface{}{
		"agentStates": temp,
	})
}

func (f *AgentStateFile) UnmarshalJSON(data []byte) error {
	var raw struct {
		AgentStates map[string]AgentState `json:"agentStates"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	f.AgentStates = make(map[mutator.WorkloadKey]AgentState)
	for keyStr, val := range raw.AgentStates {
		parts := strings.Split(keyStr, ".")
		if len(parts) != 3 {
			return fmt.Errorf("invalid key format: %s", keyStr)
		}
		f.AgentStates[mutator.WorkloadKey{
			Kind:      k8sapi.Kind(parts[0]),
			Name:      parts[1],
			Namespace: parts[2],
		}] = val
	}
	return nil
}

func (s *service) createAgentPodWatchers(ctx context.Context, ns string) (
	<-chan cache.Delta[tunnel.SessionID, *state.AgentSession],
	<-chan cache.Delta[string, *state.Intercept],
	<-chan struct{},
) {
	agentsCh := s.state.WatchAgents(ctx, func(_ tunnel.SessionID, info *state.AgentSession) bool {
		return info.Namespace == ns
	})
	sessionID := managerutil.GetSessionID(ctx)
	interceptsCh := s.state.WatchIntercepts(ctx, func(_ string, info *state.Intercept) bool {
		return info.ClientSession.SessionId == string(sessionID)
	})
	sessionDone, _ := s.state.SessionDone(sessionID)
	return agentsCh, interceptsCh, sessionDone
}

// WatchAgentPods notifies a client of the set of known Agents.
func (s *service) WatchAgentPods(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentPodInfoSnapshot]) error {
	ctx, clientInfo, err := s.ensureClientSession(stream.Context(), session)
	if err != nil {
		return err
	}
	clientSessionID := managerutil.GetSessionID(ctx)
	agentsCh, interceptsCh, sessionDone := s.createAgentPodWatchers(ctx, clientInfo.Namespace)
	agentSessions := cache.NewClientMap[tunnel.SessionID, *state.AgentSession]()

	interceptInfos := cache.NewClientMap[string, *state.Intercept]()
	lock := sync.Mutex{}
	var lastAgents []*rpc.AgentPodInfo

	onChanged := func() (err error) {
		lock.Lock()
		defer lock.Unlock()
		m := mutator.GetMap(ctx)
		agents := make([]*rpc.AgentPodInfo, 0, agentSessions.Size())
		agentSessions.Range(func(id tunnel.SessionID, a *state.AgentSession) bool {
			if m.IsInactive(types.UID(a.PodUid)) {
				return true
			}
			aip, parseErr := netip.ParseAddr(a.PodIp)
			if parseErr != nil {
				clog.Errorf(ctx, "error parsing agent pod ip %q: %v", a.PodIp, parseErr)
			}
			ap := &rpc.AgentPodInfo{
				WorkloadName: a.Name,
				PodId:        a.PodUid,
				PodName:      a.PodName,
				Namespace:    a.Namespace,
				PodIp:        aip.AsSlice(),
				ApiPort:      a.ApiPort,
				Intercepted:  s.state.IsInterceptedBy(aip, clientSessionID),
			}
			agents = append(agents, ap)
			return true
		})
		if !slices.Equal(lastAgents, agents) {
			lastAgents = agents
			err = stream.Send(&rpc.AgentPodInfoSnapshot{Agents: agents})
		}
		return err
	}

	go func() {
		_ = interceptInfos.Watch(sessionDone, interceptsCh, onChanged)
	}()
	return agentSessions.Watch(sessionDone, agentsCh, onChanged)
}

func (s *service) WatchAgentPodsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentPodInfoDelta]) error {
	ctx, clientInfo, err := s.ensureClientSession(stream.Context(), session)
	if err != nil {
		return err
	}
	clientSessionID := managerutil.GetSessionID(ctx)
	agentsCh, interceptsCh, sessionDone := s.createAgentPodWatchers(ctx, clientInfo.Namespace)
	agentPodInfos := cache.NewMap[string, *rpc.AgentPodInfo](func(a *rpc.AgentPodInfo, b *rpc.AgentPodInfo) bool {
		return proto.Equal(a, b)
	}, time.Millisecond)

	m := mutator.GetMap(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sessionDone:
				return
			case delta := <-agentsCh:
				for k, a := range delta.Upserts {
					if m.IsInactive(types.UID(a.PodUid)) {
						continue
					}
					aip, parseErr := netip.ParseAddr(a.PodIp)
					if parseErr != nil {
						clog.Errorf(ctx, "error parsing agent pod ip %q: %v", a.PodIp, parseErr)
						continue
					}
					ap := &rpc.AgentPodInfo{
						WorkloadName: a.Name,
						PodId:        a.PodUid,
						PodName:      a.PodName,
						Namespace:    a.Namespace,
						PodIp:        aip.AsSlice(),
						ApiPort:      a.ApiPort,
						Intercepted:  s.state.IsInterceptedBy(aip, clientSessionID),
					}
					agentPodInfos.Store(string(k), ap)
				}
				for k := range delta.Removals {
					agentPodInfos.Delete(string(k))
				}
			}
		}
	}()

	refreshIntercepted := func() {
		agentPodInfos.Range(func(k string, a *rpc.AgentPodInfo) bool {
			if m.IsInactive(types.UID(a.PodId)) {
				return true
			}
			aip, _ := netip.AddrFromSlice(a.PodIp)
			intercepted := s.state.IsInterceptedBy(aip, clientSessionID)
			agentPodInfos.Compute(k, func(a *rpc.AgentPodInfo, loaded bool) (*rpc.AgentPodInfo, xsync.ComputeOp) {
				if loaded && a.Intercepted != intercepted {
					a := proto.Clone(a).(*rpc.AgentPodInfo)
					a.Intercepted = intercepted
					return a, xsync.UpdateOp
				}
				return a, xsync.CancelOp
			})
			return true
		})
	}

	agentPodInfosCh := agentPodInfos.Subscribe(ctx.Done(), nil)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case <-interceptsCh:
			refreshIntercepted()
		case delta := <-agentPodInfosCh:
			err = stream.Send(&rpc.AgentPodInfoDelta{Upserts: delta.Upserts, Removals: maps2.KeySlice(delta.Removals)})
			if err != nil {
				return err
			}
		}
	}
}

// WatchAgents notifies a client of the set of known Agents in the connected namespace.
func (s *service) WatchAgents(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentInfoSnapshot]) error {
	ctx, clientInfo, err := s.ensureClientSession(stream.Context(), session)
	if err != nil {
		return err
	}
	ns := clientInfo.Namespace
	return s.watchAgents(ctx, func(_ tunnel.SessionID, a *state.AgentSession) bool { return a.Namespace == ns }, stream)
}

func infosEqual(a, b *rpc.AgentInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	return proto.Equal(a, b)
}

func (s *service) watchAgents(ctx context.Context, includeAgent func(tunnel.SessionID, *state.AgentSession) bool, stream grpc.ServerStreamingServer[rpc.AgentInfoSnapshot]) error {
	deltaCh := s.state.WatchAgents(ctx, includeAgent)
	sessionDone, err := s.state.SessionDone(managerutil.GetSessionID(ctx))
	if err != nil {
		return err
	}
	snapshot := cache.NewClientMap[tunnel.SessionID, *state.AgentSession]()
	// Ensure that the initial snapshot is not equal to lastSnap even if it is empty by
	// creating a lastSnap with one nil entry.
	var lastSnap []*rpc.AgentInfo
	firstSnap := true

	return snapshot.Watch(sessionDone, deltaCh, func() error {
		m := mutator.GetMap(ctx)

		// Sort snapshot by sessionID and discard inactive agents.
		agentSessionIDs := make([]tunnel.SessionID, 0, snapshot.Size())
		snapshot.Range(func(id tunnel.SessionID, _ *state.AgentSession) bool {
			agentSessionIDs = append(agentSessionIDs, id)
			return true
		})
		slices.Sort(agentSessionIDs)
		agents := make([]*rpc.AgentInfo, 0, len(agentSessionIDs))
		for _, agentSessionID := range agentSessionIDs {
			ag, ok := snapshot.Load(agentSessionID)
			if ok && !m.IsInactive(types.UID(ag.PodUid)) {
				agents = append(agents, ag.AgentInfo)
			}
		}
		if firstSnap {
			firstSnap = false
		} else if slices.EqualFunc(agents, lastSnap, infosEqual) {
			return nil
		}
		lastSnap = agents
		if clog.Enabled(ctx, slog.LevelDebug) {
			names := make([]string, len(agents))
			i := 0
			for _, a := range agents {
				names[i] = a.PodName + "." + a.Namespace
				i++
			}
			clog.Debugf(ctx, "Sending update %v", names)
		}
		resp := &rpc.AgentInfoSnapshot{
			Agents: agents,
		}
		return stream.Send(resp)
	})
}

func (s *service) WatchAgentsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentInfoDelta]) error {
	ctx, clientInfo, err := s.ensureClientSession(stream.Context(), session)
	if err != nil {
		return err
	}
	ns := clientInfo.Namespace
	deltaCh := s.state.WatchAgents(ctx, func(_ tunnel.SessionID, a *state.AgentSession) bool { return a.Namespace == ns })
	sessionDone, err := s.state.SessionDone(managerutil.GetSessionID(ctx))
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case delta := <-deltaCh:
			aid := rpc.AgentInfoDelta{}
			if rl := len(delta.Upserts); rl > 0 {
				aid.Upserts = make(map[string]*rpc.AgentInfo, rl)
				for k, v := range delta.Upserts {
					aid.Upserts[string(k)] = v.AgentInfo
				}
			}
			if rl := len(delta.Removals); rl > 0 {
				aid.Removals = make([]string, rl)
				for k := range delta.Removals {
					rl--
					aid.Removals[rl] = string(k)
				}
			}
			clog.Debugf(ctx, "Sending %d upserts and %d removals", len(aid.Upserts), len(aid.Removals))
			err = stream.Send(&aid)
			if err != nil {
				return err
			}
		}
	}
}

func (s *service) watchIntercepts(ctx context.Context, session *rpc.SessionInfo) (<-chan cache.Delta[string, *state.Intercept], <-chan struct{}, error) {
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
			return nil, nil, err
		}

		if agent := s.state.GetAgent(sessionID); agent != nil {
			filter = func(id string, info *state.Intercept) bool {
				if info.Spec.Namespace != agent.Namespace || info.Spec.Agent != agent.Name {
					// Don't return intercepts for different agents.
					return false
				}
				if as := s.state.GetAgent(sessionID); as == nil {
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
					clog.Debugf(ctx, "Intercept %q is in state %s", info.Spec.Name, info.Disposition)
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

	return s.state.WatchIntercepts(ctx, filter), sessionDone, nil
}

// WatchIntercepts notifies a client or agent of the set of intercepts
// relevant to that client or agent.
func (s *service) WatchIntercepts(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.InterceptInfoSnapshot]) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	deltaCh, sessionDone, err := s.watchIntercepts(ctx, session)
	if err != nil {
		return err
	}
	snapshot := cache.NewClientMap[string, *state.Intercept]()
	return snapshot.Watch(sessionDone, deltaCh, func() error {
		clog.Debug(ctx, "Sending update")
		intercepts := make([]*rpc.InterceptInfo, 0, snapshot.Size())
		snapshot.Range(func(_ string, intercept *state.Intercept) bool {
			intercepts = append(intercepts, intercept.InterceptInfo)
			return true
		})
		sort.Slice(intercepts, func(i, j int) bool {
			return intercepts[i].Id < intercepts[j].Id
		})
		return stream.Send(&rpc.InterceptInfoSnapshot{
			Intercepts: intercepts,
		})
	})
}

func (s *service) WatchInterceptsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.InterceptInfoDelta]) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	deltaCh, sessionDone, err := s.watchIntercepts(ctx, session)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case delta := <-deltaCh:
			iid := rpc.InterceptInfoDelta{Removals: maps2.KeySlice(delta.Removals)}
			if rl := len(delta.Upserts); rl > 0 {
				iid.Upserts = make(map[string]*rpc.InterceptInfo, rl)
				for k, v := range delta.Upserts {
					iid.Upserts[k] = v.InterceptInfo
				}
			}
			clog.Debugf(ctx, "Sending %d upserts and %d removals", len(iid.Upserts), len(iid.Removals))
			err = stream.Send(&iid)
			if err != nil {
				return err
			}
		}
	}
}

func (s *service) PrepareIntercept(ctx context.Context, request *rpc.CreateInterceptRequest) (*rpc.PreparedIntercept, error) {
	clog.Debugf(ctx, "Intercept name %s", request.InterceptSpec.Name)
	ctx, client, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}
	return s.state.PrepareIntercept(ctx, request, client)
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
	ctx, client, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}
	as, err := s.state.EnsureAgent(ctx, request.Name, client.Namespace)
	if err != nil {
		return nil, status.Convert(err).Err()
	}
	if len(as) == 0 {
		return nil, errors.Errorf(codes.Internal, "failed to ensure agent for workload %s: no agents became active", request.Name)
	}
	rpcAs := make([]*rpc.AgentInfo, len(as))
	for i, a := range as {
		rpcAs[i] = a.AgentInfo
	}
	lastActivity := time.Now()
	if client.Mark(lastActivity) {
		clog.Tracef(ctx, "Last activity %s", lastActivity)
	}
	return &rpc.AgentInfoSnapshot{Agents: rpcAs}, nil
}

// CreateIntercept lets a client create an intercept.
func (s *service) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	ctx = managerutil.WithSessionInfo(ctx, ciReq.GetSession())
	spec := ciReq.InterceptSpec
	clog.Debugf(ctx, "Intercept name %s", ciReq.InterceptSpec.Name)

	if val := validateIntercept(spec); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	client, interceptInfo, err := s.state.AddIntercept(ctx, ciReq)
	if err != nil {
		return nil, err
	}

	SetGauge(ctx, s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, &spec.Name, 1)

	IncrementInterceptCounterFunc(ctx, s.state.GetInterceptCounter(), client.Name, client.InstallId, spec)
	lastActivity := time.Now()
	if client.Mark(lastActivity) {
		clog.Tracef(ctx, "Last activity %s", lastActivity)
	}

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
	}
	if s.state.GetClient(tunnel.SessionID(sessionID)) == nil {
		return "", errors.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}
	return sessionID + ":" + name, nil
}

// RemoveIntercept lets a client remove an intercept.
func (s *service) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	name := riReq.Name
	clog.Debugf(ctx, "Intercept name %s", name)

	ctx, client, err := s.ensureClientSession(ctx, riReq.Session)
	if err != nil {
		return nil, err
	}
	SetGauge(ctx, s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, &name, 0)

	s.state.RemoveIntercept(string(managerutil.GetSessionID(ctx)) + ":" + name)
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
	}
	return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", request.Name)
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
		clog.Errorf(ctx, "%s - %s: %s", ceptID, rIReq.Disposition, rIReq.Message)
	} else {
		clog.Debugf(ctx, "%s - %s", ceptID, rIReq.Disposition)
	}

	s.removeExcludedEnvVars(rIReq.Environment)

	intercept := s.state.UpdateIntercept(ceptID, func(intercept *state.Intercept) {
		// Sanity check: The reviewing agent must be an agent for the intercept.
		if intercept.Spec.Namespace != agent.Namespace || intercept.Spec.Agent != agent.Name {
			return
		}
		if mutator.GetMap(ctx).IsInactive(types.UID(agent.PodUid)) {
			clog.Debugf(ctx, "Pod %s(%s) is blacklisted", agent.PodName, agent.PodIp)
			return
		}

		// Only update intercepts in the waiting or no agent states.  Agents race to review an intercept, but we
		// expect they will always produce compatible answers.
		if intercept.Disposition == rpc.InterceptDispositionType_NO_AGENT || intercept.Disposition == rpc.InterceptDispositionType_WAITING {
			intercept.Disposition = rIReq.Disposition
			intercept.Message = rIReq.Message
			intercept.PodIp = rIReq.PodIp
			intercept.PodName = agent.PodName
			intercept.FtpPort = rIReq.FtpPort
			intercept.SftpPort = rIReq.SftpPort
			intercept.MountPoint = rIReq.MountPoint
			intercept.MechanismArgsDesc = rIReq.MechanismArgsDesc
			intercept.Environment = rIReq.Environment
			intercept.Mounts = rIReq.Mounts
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

func (s *service) Tunnel(server grpc.BidiStreamingServer[rpc.TunnelMessage, rpc.TunnelMessage]) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, tunnel.ClientToManager, server)
	if err != nil {
		return errors.FromError(err, codes.FailedPrecondition, fmt.Sprintf("failed to connect stream: %v", err))
	}
	return s.state.Tunnel(ctx, stream)
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

func (s *service) Lookup(ctx context.Context, request *rpc.LookupRequest) (response *rpc.LookupResponse, err error) {
	clog.Debugf(ctx, "lookup %q", request.Name)
	var ips []netip.Addr
	defer func() {
		if err == nil {
			clog.Debugf(ctx, "lookup %q => %v", request.Name, ips)
		}
	}()
	ctx, client, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}

	if svcIP := s.clusterInfo.ServiceIP(); svcIP != nil && (request.Name == s.serviceNameNs || request.Name == s.serviceNameFQN) {
		addr, _ := netip.AddrFromSlice(svcIP)
		b, _ := addr.MarshalBinary()
		return &rpc.LookupResponse{Ips: [][]byte{b}}, nil
	}
	tmNamespace := managerutil.GetEnv(ctx).ManagerNamespace
	name := request.Name
	// Name must be at least one character long and end with a dot.
	nl := len(name)
	if nl < 2 || name[nl-1] != '.' {
		return nil, status.Errorf(codes.InvalidArgument, "empty name")
	}
	nDots := 0
	for _, c := range name {
		if c == '.' {
			nDots++
		}
	}
	if nDots == 1 && client.Namespace != tmNamespace {
		name += client.Namespace
	} else {
		// Strip trailing dot in query.
		name = name[:nl-1]
	}
	clog.Debugf(ctx, `LookupNetIP("ip", %q)`, name)
	ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	if err != nil {
		_, err = dnsproxy.MakeDNSError(err)
		if err != nil {
			return nil, err
		}
	}
	response = &rpc.LookupResponse{}
	if len(ips) > 0 {
		response.Ips = make([][]byte, len(ips))
		for i, ip := range ips {
			if ip.Is4In6() {
				ip = netip.AddrFrom4(ip.As4())
				ips[i] = ip
			}
			response.Ips[i], _ = ip.MarshalBinary()
		}
	}
	return response, nil
}

func (s *service) LookupDNS(ctx context.Context, request *rpc.DNSRequest) (response *rpc.DNSResponse, err error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	qType := uint16(request.Type)
	qtn := dns2.TypeToString[qType]
	var rrs dnsproxy.RRs
	if clog.Enabled(ctx, slog.LevelDebug) {
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
			clog.Debugf(ctx, "%s %s -> %s", request.Name, qtn, result)
		}()
	}

	if svcIP := s.clusterInfo.ServiceIP(); svcIP != nil && (request.Name == s.serviceNameNs || request.Name == s.serviceNameFQN) {
		rrs = s.resolveSelfDNS(svcIP, request)
		if len(rrs) > 0 {
			return dnsproxy.ToRPC(rrs, dns2.RcodeSuccess)
		}
	}

	sessionID := tunnel.SessionID(request.GetSession().GetSessionId())
	noSearchDomain := s.dotClusterDomain
	rrs, rCode := s.lookupFromManager(ctx, sessionID, qType, request.Name, noSearchDomain)
	return dnsproxy.ToRPC(rrs, rCode)
}

func (s *service) lookupFromManager(ctx context.Context, sessionID tunnel.SessionID, qType uint16, qName, noSearchDomain string) (dnsproxy.RRs, int) {
	name := qName
	client := s.state.GetClient(sessionID)
	tmNamespace := managerutil.GetEnv(ctx).ManagerNamespace
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
	clog.Tracef(ctx, "traffic-manager: %s", name)
	qtn := dns2.TypeToString[qType]
	rrs, rCode, err := dnsproxy.Lookup(ctx, qType, name, noSearchDomain)
	if err == nil && rCode == dns2.RcodeNameError {
		// Could still be x.y.<client namespace>, but let's avoid x.<cluster domain>.<client namespace> and x.<client-namespace>.<client namespace>
		if client != nil && nDots > 1 && client.Namespace != tmNamespace && !strings.HasSuffix(name, s.dotClusterDomain) && !hasDomainSuffix(name, client.Namespace) {
			name += client.Namespace + "."
			restoreName = true
			clog.Debugf(ctx, "traffic-manager: %s", name)
			rrs, rCode, err = dnsproxy.Lookup(ctx, qType, name, noSearchDomain)
		}
	}
	if err != nil {
		clog.Errorf(ctx, "traffic-manager: %s %s -> %s %s", qName, qtn, dns2.RcodeToString[rCode], err)
		return nil, rCode
	}
	if len(rrs) == 0 {
		clog.Tracef(ctx, "traffic-manager: %s %s -> %s", qName, qtn, dns2.RcodeToString[rCode])
	} else {
		if restoreName {
			clog.Tracef(ctx, "traffic-manager: restore %s to %s", name, qName)
			for _, rr := range rrs {
				rr.Header().Name = qName
			}
		}
		clog.Tracef(ctx, "traffic-manager: %s %s -> %s", qName, qtn, rrs)
	}
	return rrs, rCode
}

// GetLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// GetLogsRequest and returns them to the caller
//
// Deprecated: Clients should use the user daemon's GatherLogs method.
func (s *service) GetLogs(_ context.Context, _ *rpc.GetLogsRequest) (*rpc.LogsResponse, error) {
	return &rpc.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
		ErrMsg:  "traffic-manager.GetLogs is deprecated. Please upgrade your telepresence client",
	}, nil
}

func (s *service) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	err := s.state.SetTempLogLevel(ctx, request)
	if err != nil {
		err = errors.FromError(err, codes.InvalidArgument, err.Error())
	}
	return &empty.Empty{}, err
}

func (s *service) UninstallAgents(ctx context.Context, request *rpc.UninstallAgentsRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSessionInfo())
	clog.Debugf(ctx, "%s", request.Agents)
	return &empty.Empty{}, s.state.UninstallAgents(ctx, request)
}

func (s *service) WatchLogLevel(_ *empty.Empty, stream grpc.ServerStreamingServer[rpc.LogLevelRequest]) error {
	return s.state.WaitForTempLogLevel(stream)
}

func (s *service) WatchClusterInfo(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.ClusterInfo]) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	sessionDone, err := s.state.SessionDone(tunnel.SessionID(session.SessionId))
	if err != nil {
		return err
	}
	return s.clusterInfo.Watch(ctx, sessionDone, stream)
}

func (s *service) WatchWorkloads(request *rpc.WorkloadEventsRequest, stream grpc.ServerStreamingServer[rpc.WorkloadEventsDelta]) (err error) {
	ctx := stream.Context()
	// Dysfunctional prior to 2.21.0 because no initial snapshot was sent.
	if err := checkCompat(ctx, "WatchWorkloads", "2.21.0-alpha.4"); err != nil {
		return err
	}
	ctx = managerutil.WithSessionInfo(ctx, request.SessionInfo)
	clog.Debugf(ctx, "Namespace %q", request.Namespace)

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

func (s *service) updateTrafficManagerConfigMap(ctx context.Context) error {
	updatedAgentStateFileYAML := s.configWatcher.GetAgentStateYaml(ctx)
	patch := map[string]interface{}{
		"data": map[string]string{
			tmconfig.AgentStateFileName: string(updatedAgentStateFileYAML),
		},
	}

	namespace := managerutil.GetEnv(ctx).ManagerNamespace
	client := k8sapi.GetK8sInterface(ctx).CoreV1()
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = client.ConfigMaps(namespace).Patch(ctx, agentconfig.ManagerAppName, types.StrategicMergePatchType,
		patchBytes,
		metav1.PatchOptions{})

	if err == nil {
		s.tmConfigMapUpdated.Store(false)
	}
	return err
}

func (s *service) ensureClientSession(ctx context.Context, sessionInfo *rpc.SessionInfo) (context.Context, *state.ClientSession, error) {
	ctx = managerutil.WithSessionInfo(ctx, sessionInfo)
	sessionID := tunnel.SessionID(sessionInfo.SessionId)
	session := s.state.GetClient(sessionID)
	if session == nil {
		return ctx, nil, errors.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}
	return ctx, session, nil
}
