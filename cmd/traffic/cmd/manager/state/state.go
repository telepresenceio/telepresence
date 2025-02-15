package state

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/puzpuzpuz/xsync/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/watchable"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

type InterceptFinalizer func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error

type Intercept struct {
	*rpc.InterceptInfo
	finalizers []InterceptFinalizer
}

func (is *Intercept) Clone() *Intercept {
	return &Intercept{
		InterceptInfo: proto.Clone(is.InterceptInfo).(*rpc.InterceptInfo),
		finalizers:    slices.Clone(is.finalizers),
	}
}

func (is *Intercept) addFinalizer(finalizer InterceptFinalizer) {
	is.finalizers = append(is.finalizers, finalizer)
}

func (is *Intercept) terminate(ctx context.Context) {
	for i := len(is.finalizers) - 1; i >= 0; i-- {
		f := is.finalizers[i]
		if err := f(ctx, is.InterceptInfo); err != nil {
			dlog.Errorf(ctx, "finalizer for intercept %s failed: %v", is.Id, err)
		}
	}
}

type State interface {
	AddAgent(context.Context, *rpc.AgentInfo, time.Time) (tunnel.SessionID, error)
	AddClient(*rpc.ClientInfo, time.Time) tunnel.SessionID
	AddIntercept(context.Context, *rpc.CreateInterceptRequest) (*ClientSession, *rpc.InterceptInfo, error)
	AddInterceptFinalizer(string, InterceptFinalizer) error
	AddSessionConsumptionMetrics(metrics *rpc.TunnelMetrics)
	AgentsLookupDNS(context.Context, tunnel.SessionID, *rpc.DNSRequest) (dnsproxy.RRs, int, error)
	CountAgents() int
	CountClients() int
	CountIntercepts() int
	CountSessions() int
	CountTunnels() int
	CountTunnelIngress() uint64
	CountTunnelEgress() uint64
	ExpireSessions(context.Context, time.Time, time.Time)
	GetAgent(sessionID tunnel.SessionID) *AgentSession
	GetOrGenerateAgentConfig(ctx context.Context, name, namespace string) (agentconfig.SidecarExt, error)
	EachClient(f func(tunnel.SessionID, *ClientSession) bool)
	GetClient(sessionID tunnel.SessionID) *ClientSession
	GetSessionConsumptionMetrics(tunnel.SessionID) *SessionConsumptionMetrics
	GetAllSessionConsumptionMetrics() map[tunnel.SessionID]*SessionConsumptionMetrics
	GetIntercept(string) (*Intercept, bool)
	GetConnectCounter() *prometheus.CounterVec
	GetConnectActiveStatus() *prometheus.GaugeVec
	GetInterceptCounter() *prometheus.CounterVec
	GetInterceptActiveStatus() *prometheus.GaugeVec
	HasAgent(name, namespace string) bool
	MarkSession(*rpc.RemainRequest, time.Time) bool
	NewInterceptInfo(string, *rpc.CreateInterceptRequest) *Intercept
	PostLookupDNSResponse(context.Context, *rpc.DNSAgentResponse)
	EnsureAgent(context.Context, string, string) ([]*AgentSession, error)
	PrepareIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.PreparedIntercept, error)
	RemoveIntercept(context.Context, string)
	RemoveSession(context.Context, tunnel.SessionID)
	SessionDone(tunnel.SessionID) (<-chan struct{}, error)
	SetTempLogLevel(context.Context, *rpc.LogLevelRequest)
	SetAllClientSessionsFinalizer(finalizer allClientSessionsFinalizer)
	SetAllInterceptsFinalizer(finalizer allInterceptsFinalizer)
	SetPrometheusMetrics(connectCounterVec *prometheus.CounterVec,
		connectStatusGaugeVec *prometheus.GaugeVec,
		interceptCounterVec *prometheus.CounterVec,
		interceptStatusGaugeVec *prometheus.GaugeVec)
	Tunnel(context.Context, tunnel.Stream) error
	UpdateIntercept(string, func(*Intercept)) *Intercept
	RefreshSessionConsumptionMetrics(sessionID tunnel.SessionID)
	ValidateAgentImage(string, bool) error
	WaitForTempLogLevel(rpc.Manager_WatchLogLevelServer) error
	WatchAgents(context.Context, func(tunnel.SessionID, *AgentSession) bool) <-chan map[tunnel.SessionID]*AgentSession
	WatchDial(tunnel.SessionID) <-chan *rpc.DialRequest
	WatchIntercepts(context.Context, func(sessionID string, intercept *Intercept) bool) <-chan map[string]*Intercept
	WatchWorkloads(ctx context.Context, namespace string) (ch <-chan []workload.Event, err error)
	WatchLookupDNS(id tunnel.SessionID) <-chan *rpc.DNSRequest
	ValidateCreateAgent(context.Context, k8sapi.Workload, agentconfig.SidecarExt) error
	NewWorkloadInfoWatcher(clientSession tunnel.SessionID, namespace string) WorkloadInfoWatcher
	ManagesNamespace(context.Context, string) bool
	UninstallAgents(context.Context, *rpc.UninstallAgentsRequest) error
}

type (
	allClientSessionsFinalizer func(client *ClientSession)
	allInterceptsFinalizer     func(client *ClientSession, workload *string)
)

// state is the total state of the Traffic Manager.  A zero state is invalid; you must call
// NewState.
type state struct {
	// backgroundCtx is the context passed into the state by its owner. It's used for things that
	// need to exceed the context of a request into the state object, e.g. session contexts.
	backgroundCtx context.Context

	allClientSessionsFinalizer allClientSessionsFinalizer
	allInterceptsFinalizer     allInterceptsFinalizer
	intercepts                 *watchable.Map[string, *Intercept]              // info for intercepts, keyed by intercept id
	agents                     *watchable.Map[tunnel.SessionID, *AgentSession] // info for agent sessions, keyed by session id
	clients                    *xsync.MapOf[tunnel.SessionID, *ClientSession]  // info for client sessions, keyed by session id
	timedLogLevel              log.TimedLevel
	llSubs                     *loglevelSubscribers
	workloadWatchers           *xsync.MapOf[string, workload.Watcher] // workload watchers, created on demand and keyed by namespace
	tunnelCounter              int32
	tunnelIngressCounter       uint64
	tunnelEgressCounter        uint64
	connectCounter             *prometheus.CounterVec
	connectActiveStatusGauge   *prometheus.GaugeVec
	interceptCounter           *prometheus.CounterVec
	interceptActiveStatusGauge *prometheus.GaugeVec

	// Possibly extended version of the state. Use when calling interface methods.
	self State
}

func (s *state) ManagesNamespace(ctx context.Context, ns string) bool {
	return slices.Contains(namespaces.Get(ctx), ns)
}

var NewStateFunc = NewState //nolint:gochecknoglobals // extension point

func interceptEqual(a, b *Intercept) bool {
	return proto.Equal(a.InterceptInfo, b.InterceptInfo)
}

func agentsEqual(a, b *AgentSession) bool {
	return proto.Equal(a.AgentInfo, b.AgentInfo)
}

func NewState(ctx context.Context) State {
	loglevel := os.Getenv("LOG_LEVEL")
	s := &state{
		backgroundCtx:    ctx,
		intercepts:       watchable.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           watchable.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMapOf[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMapOf[string, workload.Watcher](),
		timedLogLevel:    log.NewTimedLevel(loglevel, log.SetLevel),
		llSubs:           newLoglevelSubscribers(),
	}
	s.self = s
	go func() {
		sid, nsChanges := namespaces.Subscribe(ctx)
		defer namespaces.Unsubscribe(ctx, sid)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-nsChanges:
				if !ok {
					return
				}
				s.pruneSessions(ctx)
			}
		}
	}()
	return s
}

// pruneSessions will remove all sessions that belong to namespaces that are no longer managed.
func (s *state) pruneSessions(ctx context.Context) {
	nss := namespaces.Get(ctx)
	s.clients.Range(func(id tunnel.SessionID, cs *ClientSession) bool {
		if !slices.Contains(nss, cs.Namespace) {
			s.clients.Delete(id)
			s.removeClientSession(ctx, cs)
		}
		return true
	})
	var sids []tunnel.SessionID
	s.agents.Range(func(s tunnel.SessionID, c *AgentSession) bool {
		if !slices.Contains(nss, c.Namespace) {
			sids = append(sids, s)
		}
		return true
	})
	for _, sid := range sids {
		s.removeAgentSession(ctx, sid)
	}
}

func (s *state) SetSelf(self State) {
	s.self = self
}

// checkAgentsForIntercept (1) assumes that s.mu is already locked, and (2) checks the
// status of all agents that would be relevant to the given intercept spec, and returns whether the
// state of those agents would require transitioning to an error state.  If everything looks good,
// it returns the zero error code (InterceptDispositionType_UNSPECIFIED).
func (s *state) checkAgentsForIntercept(intercept *Intercept) (errCode rpc.InterceptDispositionType, errMsg string) {
	// Don't overwrite an existing error state
	switch intercept.Disposition {
	// non-error states ////////////////////////////////////////////////////
	case rpc.InterceptDispositionType_UNSPECIFIED:
		// Continue through; we can transition to an error state from here.
	case rpc.InterceptDispositionType_ACTIVE:
		// Continue through; we can transition to an error state from here.
	case rpc.InterceptDispositionType_WAITING:
		// Continue through; we can transition to an error state from here.
	// error states ////////////////////////////////////////////////////////
	case rpc.InterceptDispositionType_NO_CLIENT:
		// Don't overwrite this error state.
		return intercept.Disposition, intercept.Message
	case rpc.InterceptDispositionType_NO_AGENT:
		// Continue through; this is an error state that this function "owns".
	case rpc.InterceptDispositionType_NO_MECHANISM:
		// Continue through; this is an error state that this function "owns".
	case rpc.InterceptDispositionType_NO_PORTS:
		// Don't overwrite this error state.
		return intercept.Disposition, intercept.Message
	case rpc.InterceptDispositionType_AGENT_ERROR:
		// Continue through; the error states of this function take precedence.
	case rpc.InterceptDispositionType_BAD_ARGS:
		// Don't overwrite this error state.
		return intercept.Disposition, intercept.Message
	case rpc.InterceptDispositionType_REMOVED:
		// Don't overwrite this state.
		return intercept.Disposition, intercept.Message
	}

	// main ////////////////////////////////////////////////////////////////

	var agentList []*rpc.AgentInfo
	agentName := intercept.Spec.Agent
	ns := intercept.Spec.Namespace
	s.EachAgent(func(_ tunnel.SessionID, ai *AgentSession) bool {
		if ai.Name == agentName && ai.Namespace == ns {
			agentList = append(agentList, ai.AgentInfo)
		}
		return true
	})

	switch {
	case len(agentList) == 0:
		errCode = rpc.InterceptDispositionType_NO_AGENT
		errMsg = fmt.Sprintf("No agent found for %q", intercept.Spec.Agent)
	case !managerutil.AgentsAreCompatible(agentList):
		errCode = rpc.InterceptDispositionType_NO_AGENT
		errMsg = fmt.Sprintf("Agents for %q are not consistent", intercept.Spec.Agent)
	case !agentHasMechanism(agentList[0], intercept.Spec.Mechanism):
		errCode = rpc.InterceptDispositionType_NO_MECHANISM
		errMsg = fmt.Sprintf("Agents for %q do not have mechanism %q", intercept.Spec.Agent, intercept.Spec.Mechanism)
	default:
		errCode = rpc.InterceptDispositionType_UNSPECIFIED
		errMsg = ""
	}
	return errCode, errMsg
}

// Sessions: common ////////////////////////////////////////////////////////////////////////////////

// MarkSession marks a session as being present at the indicated time.  Returns true if everything goes OK,
// returns false if the given session ID does not exist.
func (s *state) MarkSession(req *rpc.RemainRequest, now time.Time) (ok bool) {
	id := tunnel.SessionID(req.Session.SessionId)
	if cs, ok := s.clients.Load(id); ok {
		cs.SetLastMarked(now)
		return true
	} else if as, ok := s.agents.Load(id); ok {
		as.SetLastMarked(now)
		return true
	}
	return false
}

// RemoveSession removes an AgentSession from the set of present session IDs.
func (s *state) RemoveSession(ctx context.Context, id tunnel.SessionID) {
	if cs, ok := s.clients.LoadAndDelete(id); ok {
		s.removeClientSession(ctx, cs)
	} else {
		s.removeAgentSession(ctx, id)
	}
}

// removeAgentSession removes an AgentSession from the set of present session IDs.
func (s *state) removeAgentSession(ctx context.Context, id tunnel.SessionID) {
	if as, loaded := s.agents.LoadAndDelete(id); loaded {
		dlog.Debugf(ctx, "AgentSession %s removed. Explicit removal", id)
		mutator.GetMap(s.backgroundCtx).Inactivate(types.UID(as.PodUid))
		s.consolidateAgentSessionIntercepts(ctx, as)
	}
}

// removeClientSession removes an AgentSession from the set of present session IDs.
func (s *state) removeClientSession(ctx context.Context, cs *ClientSession) {
	dlog.Debugf(ctx, "ClientSession %s removed. Explicit removal", cs.ID())

	// kill the session
	cs.Cancel()
	s.gcClientSessionIntercepts(ctx, cs)
	scm := cs.consumptionMetrics
	atomic.AddUint64(&s.tunnelIngressCounter, scm.FromClientBytes.GetValue())
	atomic.AddUint64(&s.tunnelEgressCounter, scm.ToClientBytes.GetValue())
	s.allClientSessionsFinalizerCall(cs)
}

func (s *state) consolidateAgentSessionIntercepts(ctx context.Context, agent *AgentSession) {
	dlog.Debugf(ctx, "Consolidating intercepts after removal of agent %s(%s)", agent.PodName, agent.PodIp)
	s.intercepts.Range(func(interceptID string, intercept *Intercept) bool {
		if intercept.Disposition == rpc.InterceptDispositionType_REMOVED || agent.PodIp != intercept.PodIp {
			// Not of interest. Continue iteration.
			return true
		}

		if errCode, errMsg := s.checkAgentsForIntercept(intercept); errCode != rpc.InterceptDispositionType_UNSPECIFIED {
			// No agents matching this intercept are available, so the intercept is now dormant or in error.
			dlog.Debugf(ctx, "Intercept %q no longer has available agents. Setting its disposition to %s", interceptID, errCode)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.PodIp = ""
				intercept.PodName = ""
				intercept.Disposition = errCode
				intercept.Message = errMsg
			})
		} else if agent.PodIp == intercept.PodIp {
			// The agent is about to die, but apparently more agents are present. Let some other agent pick it up then.
			dlog.Debugf(ctx, "Intercept %q lost its agent pod %s(%s). Setting its disposition to WAITING", interceptID, agent.PodName, agent.PodIp)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.PodIp = ""
				intercept.PodName = ""
				intercept.Disposition = rpc.InterceptDispositionType_WAITING
			})
		}
		return true
	})
}

func (s *state) gcClientSessionIntercepts(ctx context.Context, client *ClientSession) {
	// GC all intercepts for the client session (intercept.ClientSession.SessionId)
	s.intercepts.Range(func(interceptID string, intercept *Intercept) bool {
		if intercept.Disposition == rpc.InterceptDispositionType_REMOVED {
			return true
		}
		if tunnel.SessionID(intercept.ClientSession.SessionId) == client.ID() {
			// Client went away:
			// Delete it.
			wl := strings.SplitN(interceptID, ":", 2)[1]
			s.allInterceptsFinalizerCall(client, &wl)
			s.self.RemoveIntercept(ctx, interceptID)
		}
		return true
	})
}

// ExpireSessions prunes any sessions that haven't had a MarkSession heartbeat since
// respective given 'moment'.
func (s *state) ExpireSessions(ctx context.Context, clientMoment, agentMoment time.Time) {
	s.clients.Range(func(id tunnel.SessionID, client *ClientSession) bool {
		moment := clientMoment
		if client.LastMarked().Before(moment) {
			s.clients.Delete(id)
			s.removeClientSession(ctx, client)
		}
		return true
	})
	s.agents.Range(func(id tunnel.SessionID, agent *AgentSession) bool {
		moment := agentMoment
		if agent.LastMarked().Before(moment) {
			s.removeAgentSession(ctx, id)
		}
		return true
	})
}

// SessionDone returns a channel that is closed when the session with the given ID terminates.  If
// there is no such currently-live session, then an already-closed channel is returned.
func (s *state) SessionDone(id tunnel.SessionID) (<-chan struct{}, error) {
	if cs, ok := s.clients.Load(id); ok {
		return cs.Done(), nil
	}
	if as, ok := s.agents.Load(id); ok {
		return as.Done(), nil
	}
	return nil, status.Errorf(codes.NotFound, "session %q not found", id)
}

// Sessions: Clients ///////////////////////////////////////////////////////////////////////////////

func (s *state) AddClient(client *rpc.ClientInfo, now time.Time) tunnel.SessionID {
	// Use non-sequential things (i.e., UUIDs, not just a counter) as the session ID, because
	// the session ID also exists in external systems (the client, SystemA), so it's confusing
	// (to both humans and computers) if the manager restarts and those existing session IDs
	// suddenly refer to different sessions.
	sessionID := tunnel.SessionID(uuid.New().String())
	s.addClient(sessionID, client, now)
	return sessionID
}

// addClient is like AddClient, but takes a sessionID, for testing purposes.
func (s *state) addClient(id tunnel.SessionID, client *rpc.ClientInfo, now time.Time) {
	cs := newClientSessionState(s.backgroundCtx, id, client, now)
	if oldClient, hasConflict := s.clients.LoadOrStore(id, cs); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", id, oldClient, client))
	}
}

func (s *state) GetClient(id tunnel.SessionID) *ClientSession {
	ret, _ := s.clients.Load(id)
	return ret
}

func (s *state) EachClient(f func(tunnel.SessionID, *ClientSession) bool) {
	s.clients.Range(f)
}

func (s *state) CountAgents() int {
	return s.agents.Size()
}

func (s *state) CountClients() int {
	return s.clients.Size()
}

func (s *state) CountIntercepts() int {
	return s.intercepts.Size()
}

func (s *state) CountSessions() int {
	return s.CountAgents() + s.CountClients()
}

func (s *state) CountTunnels() int {
	return int(atomic.LoadInt32(&s.tunnelCounter))
}

func (s *state) CountTunnelIngress() uint64 {
	return atomic.LoadUint64(&s.tunnelIngressCounter)
}

func (s *state) CountTunnelEgress() uint64 {
	return atomic.LoadUint64(&s.tunnelEgressCounter)
}

// Sessions: Agents ////////////////////////////////////////////////////////////////////////////////

func (s *state) AddAgent(ctx context.Context, agent *rpc.AgentInfo, now time.Time) (tunnel.SessionID, error) {
	if mutator.GetMap(ctx).IsInactive(types.UID(agent.PodUid)) {
		return "", status.Error(codes.Aborted, "inactivated pod")
	}
	id := tunnel.SessionID(AgentSessionIDPrefix + agent.PodUid)
	as := newAgentSessionState(s.backgroundCtx, id, agent, now)
	if oldAgent, hasConflict := s.agents.LoadOrStore(id, as); hasConflict {
		return "", status.Error(codes.AlreadyExists, fmt.Sprintf("duplicate id %q, existing %+v, new %+v", id, oldAgent, agent))
	}

	s.intercepts.Range(func(interceptID string, intercept *Intercept) bool {
		if intercept.Disposition == rpc.InterceptDispositionType_REMOVED {
			return true
		}
		// Check whether each intercept needs to either (1) be moved in to a NO_AGENT state
		// because this agent made things inconsistent, or (2) be moved out of a NO_AGENT
		// state because it just gained an agent.
		if errCode, errMsg := s.checkAgentsForIntercept(intercept); errCode != 0 {
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.Disposition = errCode
				intercept.Message = errMsg
			})
		} else if intercept.Disposition == rpc.InterceptDispositionType_NO_AGENT {
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.Disposition = rpc.InterceptDispositionType_WAITING
				intercept.Message = ""
			})
		}
		return true
	})
	return id, nil
}

func (s *state) GetAgent(id tunnel.SessionID) *AgentSession {
	if ret, ok := s.agents.Load(id); ok {
		if !mutator.GetMap(s.backgroundCtx).IsInactive(types.UID(ret.PodUid)) {
			return ret
		}
	}
	return nil
}

func (s *state) EachAgent(f func(tunnel.SessionID, *AgentSession) bool) {
	m := mutator.GetMap(s.backgroundCtx)
	s.agents.Range(func(id tunnel.SessionID, ag *AgentSession) bool {
		if !m.IsInactive(types.UID(ag.PodUid)) {
			return f(id, ag)
		}
		return true
	})
}

func (s *state) LoadMatchingAgents(f func(tunnel.SessionID, *AgentSession) bool) map[tunnel.SessionID]*AgentSession {
	m := mutator.GetMap(s.backgroundCtx)
	return s.agents.LoadMatching(func(id tunnel.SessionID, ai *AgentSession) bool {
		return !m.IsInactive(types.UID(ai.PodUid)) && f(id, ai)
	})
}

func (s *state) HasAgent(name, namespace string) (ok bool) {
	s.EachAgent(func(_ tunnel.SessionID, ai *AgentSession) bool {
		if ai.Name == name && ai.Namespace == namespace {
			ok = true
			return false
		}
		return true
	})
	return ok
}

func (s *state) WatchAgents(
	ctx context.Context,
	filter func(tunnel.SessionID, *AgentSession) bool,
) <-chan map[tunnel.SessionID]*AgentSession {
	return s.agents.Subscribe(ctx.Done(), filter)
}

func (s *state) WatchWorkloads(ctx context.Context, ns string) (ch <-chan []workload.Event, err error) {
	ww, _ := s.workloadWatchers.Compute(ns, func(ww workload.Watcher, loaded bool) (workload.Watcher, bool) {
		if loaded {
			return ww, false
		}
		ww, err = workload.NewWatcher(s.backgroundCtx, ns, managerutil.GetEnv(ctx).EnabledWorkloadKinds)
		return ww, err != nil // delete if error.
	})
	if err != nil {
		return nil, err
	}
	return ww.Subscribe(ctx), nil
}

// Intercepts //////////////////////////////////////////////////////////////////////////////////////

// getAgentsInNamespace returns the session IDs the agents in the given namespace.
func (s *state) getAgentsInNamespace(namespace string) map[tunnel.SessionID]*AgentSession {
	return s.LoadMatchingAgents(func(_ tunnel.SessionID, ai *AgentSession) bool {
		return ai.Namespace == namespace
	})
}

// UpdateIntercept applies a given mutator function to the stored intercept with interceptID;
// storing and returning the result.  If the given intercept does not exist, then the mutator
// function is not run, and nil is returned.
//
// This does not lock; but instead uses CAS and may therefore call the mutator function multiple
// times.  So: it is safe to perform blocking operations in your mutator function, but you must take
// care that it is safe to call your mutator function multiple times.
func (s *state) UpdateIntercept(interceptID string, apply func(*Intercept)) *Intercept {
	for {
		cur, ok := s.intercepts.Load(interceptID)
		if !ok {
			// Doesn't exist (possibly was deleted while this loop was running).
			return nil
		}

		newInfo := cur.Clone()
		apply(newInfo)
		newInfo.ModifiedAt = timestamppb.Now()

		swapped := s.intercepts.CompareAndSwap(newInfo.Id, cur, newInfo)
		if swapped {
			// Success!
			return newInfo
		}
	}
}

func (s *state) RemoveIntercept(ctx context.Context, interceptID string) {
	if is, ok := s.intercepts.LoadAndDelete(interceptID); ok {
		is.terminate(s.backgroundCtx)
	}
}

func (s *state) UninstallAgents(ctx context.Context, ur *rpc.UninstallAgentsRequest) error {
	id := tunnel.SessionID(ur.GetSessionInfo().GetSessionId())
	clientInfo := s.GetClient(id)
	if clientInfo == nil {
		return status.Errorf(codes.NotFound, "Client session %q not found", id)
	}
	ns := clientInfo.GetNamespace()
	mm := mutator.GetMap(ctx)
	agents := ur.Agents
	if len(agents) == 0 {
		if err := mm.EvictAllPodsWithAgentConfig(ctx, ns); err != nil {
			return status.Errorf(codes.Internal, "unable to delete pods with agent: %v", err)
		}
		return nil
	}

	wls := make([]k8sapi.Workload, len(agents))
	for i, agent := range agents {
		wl, err := k8sapi.GetWorkload(ctx, agent, ns, "")
		if err != nil {
			return status.Errorf(codes.NotFound, "Workload %s.%s not found", agent, ns)
		}
		wls[i] = wl
	}

	for _, wl := range wls {
		mm.Delete(wl.GetName(), ns)
		if err := mm.EvictPodsWithAgentConfig(ctx, wl); err != nil {
			return status.Errorf(codes.Internal, "unable to delete agent for workload %s.%s: %v", wl.GetName(), ns, err)
		}
	}
	return nil
}

func (s *state) GetIntercept(interceptID string) (*Intercept, bool) {
	return s.intercepts.Load(interceptID)
}

func (s *state) WatchIntercepts(
	ctx context.Context,
	filter func(sessionID string, intercept *Intercept) bool,
) <-chan map[string]*Intercept {
	return s.intercepts.Subscribe(ctx.Done(), filter)
}

func (s *state) Tunnel(ctx context.Context, stream tunnel.Stream) error {
	id := stream.SessionID()
	if cs, ok := s.clients.Load(id); ok {
		return s.clientTunnel(ctx, cs, stream)
	}
	if as, ok := s.agents.Load(id); ok {
		return s.agentTunnel(ctx, as, stream)
	}
	return status.Errorf(codes.NotFound, "Session %q not found", id)
}

func (s *state) agentTunnel(ctx context.Context, agent *AgentSession, stream tunnel.Stream) error {
	var scm *SessionConsumptionMetrics

	// If it's an agent, find the associated ClientSession.
	if clientSessionID := agent.AwaitingBidiMapOwnerSessionID(stream); clientSessionID != "" {
		cs, ok := s.clients.Load(clientSessionID) // get awaiting state
		if ok {                                   // if found
			scm = cs.ConsumptionMetrics()
		}
	}

	if bidiPipe, err := agent.OnConnect(ctx, stream, &s.tunnelCounter, scm); err != nil {
		return err
	} else if bidiPipe != nil {
		// A peer awaited this stream. Wait for the bidiPipe to finish
		<-bidiPipe.Done()
		return nil
	}

	// A traffic-agent must always extend the tunnel to the client that it is currently intercepted
	// by, and hence, start by sending the sessionID of that client on the tunnel.

	// Obtain the desired client session
	m, err := stream.Receive(ctx)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to read first message from agent tunnel %q: %v", agent.PodName, err)
	}
	if m.Code() != tunnel.Session {
		return status.Errorf(codes.FailedPrecondition, "unable to read ClientSession from agent %q", agent.PodName)
	}
	if peerSession, ok := s.clients.Load(tunnel.GetSession(m)); ok {
		endPoint, err := peerSession.EstablishBidiPipe(ctx, stream)
		if err == nil {
			<-endPoint.Done()
		}
		return err
	}
	return nil
}

func (s *state) clientTunnel(ctx context.Context, client *ClientSession, stream tunnel.Stream) error {
	scm := client.ConsumptionMetrics()
	if bidiPipe, err := client.OnConnect(ctx, stream, &s.tunnelCounter, scm); err != nil {
		return err
	} else if bidiPipe != nil {
		// A peer awaited this stream. Wait for the bidiPipe to finish
		<-bidiPipe.Done()
		return nil
	}

	// The session is either the telepresence client or a traffic-agent.
	//
	// A client will want to extend the tunnel to a dialer in an intercepted traffic-agent or, if no
	// intercept is active, to a dialer in that namespace.
	if peerSession := s.getAgentForDial(ctx, client, stream.ID().DestinationAddr()); peerSession != nil {
		endPoint, err := peerSession.EstablishBidiPipe(ctx, stream)
		if err == nil {
			<-endPoint.Done()
		}
		return err
	}

	// No peerSession exists, so use the traffic-manager itself for the dial.
	endPoint := tunnel.NewDialer(stream, func() {}, scm.FromClientBytes, scm.ToClientBytes)
	endPoint.Start(ctx)
	<-endPoint.Done()
	return nil
}

func (s *state) getAgentForDial(ctx context.Context, client *ClientSession, podIP netip.Addr) *AgentSession {
	// An agent with a podIO matching the given podIP has precedence
	agents := s.LoadMatchingAgents(func(key tunnel.SessionID, ai *AgentSession) bool {
		if aip, err := netip.ParseAddr(ai.PodIp); err == nil {
			return podIP == aip
		}
		return false
	})
	for _, agent := range agents {
		dlog.Debugf(ctx, "selecting agent for dial based on podIP %s", podIP)
		return agent
	}

	env := managerutil.GetEnv(ctx)
	if env.ManagerNamespace == client.Namespace {
		// Traffic manager will do just fine
		dlog.Debugf(ctx, "selecting traffic-manager for dial, because it's in namespace %q", client.Namespace)
		return nil
	}

	// Any agent that is currently intercepted by the client has precedence.
	for _, agent := range s.getAgentsInterceptedByClient(client.ID()) {
		dlog.Debugf(ctx, "selecting intercepted agent %q for dial", agent.PodName)
		return agent
	}

	// Any agent from the same namespace will do.
	for _, agent := range s.getAgentsInNamespace(client.Namespace) {
		dlog.Debugf(ctx, "selecting agent %q for dial based on namespace %q", agent.PodName, client.Namespace)
		return agent
	}

	// Best effort is to use the traffic-manager.
	// TODO: Add a pod that can dial from the correct namespace
	dlog.Debugf(ctx, "selecting traffic-manager for dial, even though it's not in namespace %q", client.Namespace)
	return nil
}

func (s *state) WatchDial(id tunnel.SessionID) <-chan *rpc.DialRequest {
	if cs := s.GetClient(id); cs != nil {
		return cs.Dials()
	}
	if as := s.GetAgent(id); as != nil {
		return as.Dials()
	}
	return nil
}

// SetTempLogLevel sets the temporary log-level for the traffic-manager and all agents and,
// if a duration is given, it also starts a timer that will reset the log-level once it
// fires.
func (s *state) SetTempLogLevel(ctx context.Context, logLevelRequest *rpc.LogLevelRequest) {
	duration := time.Duration(0)
	if gd := logLevelRequest.Duration; gd != nil {
		duration = gd.AsDuration()
	}
	s.timedLogLevel.Set(ctx, logLevelRequest.LogLevel, duration)
	s.llSubs.notify(ctx, logLevelRequest)
}

// InitialTempLogLevel returns the temporary log-level if it exists, along with the remaining
// duration for it, which might be zero, in which case the log-level is valid until a new
// level is requested.
func (s *state) InitialTempLogLevel() *rpc.LogLevelRequest {
	level, duration := s.timedLogLevel.Get()
	if level == "" {
		return nil
	}
	return &rpc.LogLevelRequest{
		LogLevel: level,
		Duration: durationpb.New(duration),
	}
}

// WaitForTempLogLevel waits for a new temporary log-level request. It returns the values
// of the last request that was made.
func (s *state) WaitForTempLogLevel(stream rpc.Manager_WatchLogLevelServer) error {
	return s.llSubs.subscriberLoop(stream.Context(), stream)
}

func (s *state) SetPrometheusMetrics(
	connectCounterVec *prometheus.CounterVec,
	connectStatusGaugeVec *prometheus.GaugeVec,
	interceptCounterVec *prometheus.CounterVec,
	interceptStatusGaugeVec *prometheus.GaugeVec,
) {
	s.connectCounter = connectCounterVec
	s.connectActiveStatusGauge = connectStatusGaugeVec
	s.interceptCounter = interceptCounterVec
	s.interceptActiveStatusGauge = interceptStatusGaugeVec
}

func (s *state) GetConnectCounter() *prometheus.CounterVec {
	return s.connectCounter
}

func (s *state) GetConnectActiveStatus() *prometheus.GaugeVec {
	return s.connectActiveStatusGauge
}

func (s *state) GetInterceptCounter() *prometheus.CounterVec {
	return s.interceptCounter
}

func (s *state) GetInterceptActiveStatus() *prometheus.GaugeVec {
	return s.interceptActiveStatusGauge
}

func (s *state) SetAllClientSessionsFinalizer(finalizer allClientSessionsFinalizer) {
	s.allClientSessionsFinalizer = finalizer
}

func (s *state) allClientSessionsFinalizerCall(client *ClientSession) {
	if s.allClientSessionsFinalizer != nil {
		s.allClientSessionsFinalizer(client)
	}
}

func (s *state) SetAllInterceptsFinalizer(finalizer allInterceptsFinalizer) {
	s.allInterceptsFinalizer = finalizer
}

func (s *state) allInterceptsFinalizerCall(client *ClientSession, workload *string) {
	if s.allInterceptsFinalizer != nil {
		s.allInterceptsFinalizer(client, workload)
	}
}
