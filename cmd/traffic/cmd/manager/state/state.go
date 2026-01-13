package state

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	grpcErrors "github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
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
			clog.Errorf(ctx, "finalizer for intercept %s failed: %v", is.Id, err)
		}
	}
}

type (
	allClientSessionsFinalizer func(client *ClientSession)
	allInterceptsFinalizer     func(client *ClientSession, workload *string)
)

// State is the total state of the Traffic Manager. A zero state is invalid; you must call NewState.
type State struct {
	// backgroundCtx is the context passed into the state by its owner. It's used for things that
	// need to exceed the context of a request into the state object, e.g. session contexts.
	backgroundCtx context.Context

	allClientSessionsFinalizer allClientSessionsFinalizer
	allInterceptsFinalizer     allInterceptsFinalizer
	intercepts                 *cache.Map[string, *Intercept]               // info for intercepts, keyed by intercept id
	agents                     *cache.Map[tunnel.SessionID, *AgentSession]  // info for agent sessions, keyed by session id
	clients                    *xsync.Map[tunnel.SessionID, *ClientSession] // info for client sessions, keyed by session id
	timedLogLevel              log.TimedLevel
	llSubs                     *loglevelSubscribers
	workloadWatchers           *xsync.Map[string, Watcher] // workload watchers, created on demand and keyed by namespace
	tunnelCounter              int32
	tunnelIngressCounter       uint64
	tunnelEgressCounter        uint64
	connectCounter             *prometheus.CounterVec
	connectActiveStatusGauge   *prometheus.GaugeVec
	interceptCounter           *prometheus.CounterVec
	interceptActiveStatusGauge *prometheus.GaugeVec
}

func (s *State) ManagesNamespace(ctx context.Context, ns string) bool {
	return slices.Contains(namespaces.Get(ctx), ns)
}

func interceptEqual(a, b *Intercept) bool {
	return proto.Equal(a.InterceptInfo, b.InterceptInfo)
}

func agentsEqual(a, b *AgentSession) bool {
	return proto.Equal(a.AgentInfo, b.AgentInfo)
}

func NewState(ctx context.Context, g log.Group) *State {
	loglevel, err := clog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		loglevel = slog.LevelInfo
	}
	s := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, 5*time.Millisecond, xsync.WithGrowOnly()),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, 5*time.Millisecond, xsync.WithGrowOnly()),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](xsync.WithGrowOnly()),
		workloadWatchers: xsync.NewMap[string, Watcher](xsync.WithGrowOnly()),
		timedLogLevel:    log.NewTimedLevel(loglevel, clog.SetTreeLevel),
		llSubs:           newLoglevelSubscribers(),
	}
	g.Go("namespace-GC", s.pruneSessionGCLoop)
	g.Go("expired-GC", s.runSessionGCLoop)
	return s
}

const agentSessionTTL = 70 * time.Second

func (s *State) runSessionGCLoop(ctx context.Context) error {
	// Loop calling Expire
	const tickInterval = 5 * time.Second
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	lastTick := time.Now().UnixNano()
	clientTTL := managerutil.GetEnv(ctx).ClientConnectionTTL
	for {
		select {
		case now := <-ticker.C:
			// We cannot use time.Sub() because it uses the monotonic clock. We need the wall clock difference.
			diff := time.Duration(now.UnixNano() - lastTick - int64(tickInterval)) // Should normally be close to zero.
			lastTick = now.UnixNano()
			if diff > tickInterval {
				// It's been more than tickInterval*2 since the last tick, so the computer must have been sleeping. Let's adjust
				// all marks with the delay.
				clog.Debugf(ctx, "Computer slept %s, adjusting session marks", diff)
				s.clients.Range(func(id tunnel.SessionID, cs *ClientSession) bool {
					cs.adjustMark(diff)
					return true
				})
				s.agents.Range(func(id tunnel.SessionID, as *AgentSession) bool {
					as.adjustMark(diff)
					return true
				})
			}
			s.expireSessions(ctx, now.Add(-clientTTL), now.Add(-agentSessionTTL))

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *State) pruneSessionGCLoop(ctx context.Context) error {
	sid, nsChanges := namespaces.Subscribe(ctx)
	defer namespaces.Unsubscribe(ctx, sid)
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-nsChanges:
			if !ok {
				return nil
			}
			s.pruneSessions(ctx)
		}
	}
}

// pruneSessions will remove all sessions that belong to namespaces that are no longer managed.
func (s *State) pruneSessions(ctx context.Context) {
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

// checkAgentsForIntercept (1) assumes that s.mu is already locked, and (2) checks the
// status of all agents that would be relevant to the given intercept spec, and returns whether the
// state of those agents would require transitioning to an error state.  If everything looks good,
// it returns the zero error code (InterceptDispositionType_UNSPECIFIED).
func (s *State) checkAgentsForIntercept(intercept *Intercept) (errCode rpc.InterceptDispositionType, errMsg string) {
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
func (s *State) MarkSession(req *rpc.RemainRequest, now time.Time) (ok bool) {
	id := tunnel.SessionID(req.Session.SessionId)
	if cs, ok := s.clients.Load(id); ok {
		cs.mark(now)
		return true
	} else if as, ok := s.agents.Load(id); ok {
		as.mark(now)
		return true
	}
	return false
}

// RemoveSession removes an AgentSession from the set of present session IDs.
func (s *State) RemoveSession(ctx context.Context, id tunnel.SessionID) {
	if cs, ok := s.clients.LoadAndDelete(id); ok {
		s.removeClientSession(ctx, cs)
	} else {
		s.removeAgentSession(ctx, id)
	}
}

// removeAgentSession removes an AgentSession from the set of present session IDs.
func (s *State) removeAgentSession(ctx context.Context, id tunnel.SessionID) {
	if as, loaded := s.agents.LoadAndDelete(id); loaded {
		clog.Debugf(ctx, "AgentSession %s removed. Explicit removal", id)
		mutator.GetMap(s.backgroundCtx).Inactivate(types.UID(as.PodUid))
		s.consolidateAgentSessionIntercepts(ctx, as)
	}
}

// removeClientSession removes an AgentSession from the set of present session IDs.
func (s *State) removeClientSession(ctx context.Context, cs *ClientSession) {
	clog.Debugf(ctx, "ClientSession %s removed. Explicit removal", cs.sessionID())

	// kill the session
	cs.cancel()
	s.gcClientSessionIntercepts(ctx, cs)
	scm := cs.consumptionMetrics
	atomic.AddUint64(&s.tunnelIngressCounter, scm.FromClientBytes.GetValue())
	atomic.AddUint64(&s.tunnelEgressCounter, scm.ToClientBytes.GetValue())
	s.allClientSessionsFinalizerCall(cs)
}

func (s *State) consolidateAgentSessionIntercepts(ctx context.Context, agent *AgentSession) {
	clog.Debugf(ctx, "Consolidating intercepts after removal of agent %s(%s)", agent.PodName, agent.PodIp)
	s.intercepts.Range(func(interceptID string, intercept *Intercept) bool {
		if intercept.Disposition == rpc.InterceptDispositionType_REMOVED || agent.PodIp != intercept.PodIp {
			// Not of interest. Continue iteration.
			return true
		}

		if errCode, errMsg := s.checkAgentsForIntercept(intercept); errCode != rpc.InterceptDispositionType_UNSPECIFIED {
			// No agents matching this intercept are available, so the intercept is now dormant or in error.
			clog.Debugf(ctx, "Intercept %q no longer has available agents. Setting its disposition to %s", interceptID, errCode)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.PodIp = ""
				intercept.PodName = ""
				intercept.Disposition = errCode
				intercept.Message = errMsg
			})
		} else if agent.PodIp == intercept.PodIp {
			// The agent is about to die, but apparently more agents are present. Let some other agent pick it up then.
			clog.Debugf(ctx, "Intercept %q lost its agent pod %s(%s). Setting its disposition to WAITING", interceptID, agent.PodName, agent.PodIp)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.PodIp = ""
				intercept.PodName = ""
				intercept.Disposition = rpc.InterceptDispositionType_WAITING
			})
		}
		return true
	})
}

func (s *State) gcClientSessionIntercepts(ctx context.Context, client *ClientSession) {
	// GC all intercepts for the client session (intercept.ClientSession.SessionId)
	s.intercepts.Range(func(interceptID string, intercept *Intercept) bool {
		if intercept.Disposition == rpc.InterceptDispositionType_REMOVED {
			return true
		}
		if tunnel.SessionID(intercept.ClientSession.SessionId) == client.sessionID() {
			// Client went away:
			// Delete it.
			wl := strings.SplitN(interceptID, ":", 2)[1]
			s.allInterceptsFinalizerCall(client, &wl)
			s.RemoveIntercept(ctx, interceptID)
		}
		return true
	})
}

// expireSessions prunes any sessions that haven't had a MarkSession heartbeat since
// respective given 'moment'.
func (s *State) expireSessions(ctx context.Context, clientMoment, agentMoment time.Time) {
	s.clients.Range(func(id tunnel.SessionID, client *ClientSession) bool {
		moment := clientMoment
		if client.lastMarked().Before(moment) {
			s.clients.Delete(id)
			s.removeClientSession(ctx, client)
		}
		return true
	})
	s.agents.Range(func(id tunnel.SessionID, agent *AgentSession) bool {
		moment := agentMoment
		if agent.lastMarked().Before(moment) {
			s.removeAgentSession(ctx, id)
		}
		return true
	})
}

// SessionDone returns a channel that is closed when the session with the given ID terminates.  If
// there is no such currently-live session, then an already-closed channel is returned.
func (s *State) SessionDone(id tunnel.SessionID) (<-chan struct{}, error) {
	if cs, ok := s.clients.Load(id); ok {
		return cs.done(), nil
	}
	if as, ok := s.agents.Load(id); ok {
		return as.done(), nil
	}
	return nil, grpcErrors.Errorf(codes.NotFound, "session %q not found", id)
}

// Sessions: Clients ///////////////////////////////////////////////////////////////////////////////

func (s *State) AddClient(client *rpc.ClientInfo, now time.Time) tunnel.SessionID {
	// Use non-sequential things (i.e., UUIDs, not just a counter) as the session ID, because
	// the session ID also exists in external systems (the client, SystemA), so it's confusing
	// (to both humans and computers) if the manager restarts and those existing session IDs
	// suddenly refer to different sessions.
	sessionID := tunnel.SessionID(uuid.New().String())
	s.addClient(sessionID, client, now)
	return sessionID
}

func (s *State) RestoreClient(sessionID tunnel.SessionID, client *rpc.ClientInfo, now time.Time) {
	s.addClient(sessionID, client, now)
}

func (s *State) RestoreAgents(agents []*rpc.AgentInfo, now time.Time) {
	for _, newAgent := range agents {
		id := tunnel.SessionID(AgentSessionIDPrefix + newAgent.PodUid)
		s.agents.LoadOrCompute(id, func() *AgentSession {
			return newAgentSessionState(s.backgroundCtx, id, newAgent, now)
		})
	}
}

func (s *State) RestoreIntercepts(ctx context.Context, intercepts []*rpc.InterceptInfo, now time.Time) {
	var addedChildren []*Intercept
	for _, intercept := range intercepts {
		s.intercepts.LoadOrCompute(intercept.Id, func() *Intercept {
			spec := intercept.Spec
			is := &Intercept{InterceptInfo: intercept}
			if IsChildIntercept(spec) {
				// Finalizer must be added to the parent intercept, but the parent might be added after
				// the child intercept is added, so it'll have to wait.
				addedChildren = append(addedChildren, is)
			} else {
				wl, err := agentmap.GetWorkload(ctx, spec.Agent, spec.Namespace, k8sapi.Kind(spec.WorkloadKind))
				if err == nil {
					is.addFinalizer(func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
						return s.restoreAppContainer(ctx, interceptInfo, wl)
					})
				}
			}
			return is
		})
	}
	for _, intercept := range addedChildren {
		parent, ok := s.GetParentIntercept(tunnel.SessionID(intercept.ClientSession.SessionId), intercept.Spec)
		if ok {
			parent.addFinalizer(func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
				s.intercepts.Delete(intercept.Id)
				return nil
			})
		}
	}
}

// addClient is like AddClient but takes a sessionID, for testing purposes.
func (s *State) addClient(id tunnel.SessionID, client *rpc.ClientInfo, now time.Time) {
	cs := newClientSessionState(s.backgroundCtx, id, client, now)
	if oldClient, hasConflict := s.clients.LoadOrStore(id, cs); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", id, oldClient, client))
	}
}

func (s *State) GetClient(id tunnel.SessionID) *ClientSession {
	ret, _ := s.clients.Load(id)
	return ret
}

func (s *State) EachClient(f func(tunnel.SessionID, *ClientSession) bool) {
	s.clients.Range(f)
}

func (s *State) CountAgents() int {
	return s.agents.Size()
}

func (s *State) CountClients() int {
	return s.clients.Size()
}

func (s *State) CountIntercepts() int {
	return s.intercepts.Size()
}

func (s *State) CountSessions() int {
	return s.CountAgents() + s.CountClients()
}

func (s *State) CountTunnels() int {
	return int(atomic.LoadInt32(&s.tunnelCounter))
}

func (s *State) CountTunnelIngress() uint64 {
	return atomic.LoadUint64(&s.tunnelIngressCounter)
}

func (s *State) CountTunnelEgress() uint64 {
	return atomic.LoadUint64(&s.tunnelEgressCounter)
}

func (s *State) IsInterceptedBy(agentPodIP netip.Addr, client tunnel.SessionID) (found bool) {
	clientSessionID := string(client)
	podIPStr := agentPodIP.String()
	s.intercepts.Range(func(id string, ii *Intercept) bool {
		if ii.PodIp == podIPStr && ii.ClientSession.SessionId == clientSessionID {
			found = true
			return false
		}
		return true
	})
	return found
}

// Sessions: Agents ////////////////////////////////////////////////////////////////////////////////

func (s *State) AddAgent(ctx context.Context, agent *rpc.AgentInfo, now time.Time) (tunnel.SessionID, error) {
	if mutator.GetMap(ctx).IsInactive(types.UID(agent.PodUid)) {
		return "", status.Error(codes.Aborted, "inactivated pod")
	}
	return s.RestoreAgent(ctx, tunnel.SessionID(AgentSessionIDPrefix+agent.PodUid), agent, now)
}

func (s *State) RestoreAgent(ctx context.Context, id tunnel.SessionID, agent *rpc.AgentInfo, now time.Time) (tunnel.SessionID, error) {
	as := newAgentSessionState(s.backgroundCtx, id, agent, now)
	if _, exists := s.agents.LoadOrStore(id, as); exists {
		return "", nil
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

func (s *State) GetAgent(id tunnel.SessionID) *AgentSession {
	if ret, ok := s.agents.Load(id); ok {
		if !mutator.GetMap(s.backgroundCtx).IsInactive(types.UID(ret.PodUid)) {
			return ret
		}
	}
	return nil
}

func (s *State) EachAgent(f func(tunnel.SessionID, *AgentSession) bool) {
	m := mutator.GetMap(s.backgroundCtx)
	s.agents.Range(func(id tunnel.SessionID, ag *AgentSession) bool {
		if !m.IsInactive(types.UID(ag.PodUid)) {
			return f(id, ag)
		}
		return true
	})
}

func (s *State) LoadMatchingAgents(f func(tunnel.SessionID, *AgentSession) bool) map[tunnel.SessionID]*AgentSession {
	m := mutator.GetMap(s.backgroundCtx)
	return s.agents.LoadMatching(func(id tunnel.SessionID, ai *AgentSession) bool {
		return !m.IsInactive(types.UID(ai.PodUid)) && f(id, ai)
	})
}

func (s *State) HasAgent(name, namespace string) (ok bool) {
	s.EachAgent(func(_ tunnel.SessionID, ai *AgentSession) bool {
		if ai.Name == name && ai.Namespace == namespace {
			ok = true
			return false
		}
		return true
	})
	return ok
}

func (s *State) WatchAgents(
	ctx context.Context,
	filter func(tunnel.SessionID, *AgentSession) bool,
) <-chan cache.Delta[tunnel.SessionID, *AgentSession] {
	return s.agents.Subscribe(ctx.Done(), filter)
}

func (s *State) WatchWorkloads(ctx context.Context, ns string) (ch <-chan []Event, err error) {
	ww, _ := s.workloadWatchers.LoadOrCompute(ns, func() (ww Watcher, rm bool) {
		ww, err = NewWatcher(s.backgroundCtx, ns, managerutil.GetEnv(ctx).EnabledWorkloadKinds)
		return ww, err != nil // delete if error.
	})
	if err != nil {
		return nil, err
	}
	return ww.Subscribe(ctx), nil
}

// UpdateIntercept applies a given mutator function to the stored intercept with interceptID;
// storing and returning the result.  If the given intercept does not exist, then the mutator
// function is not run, and nil is returned.
//
// This does not lock; but instead uses CAS and may therefore call the mutator function multiple
// times.  So: it is safe to perform blocking operations in your mutator function, but you must take
// care that it is safe to call your mutator function multiple times.
func (s *State) UpdateIntercept(interceptID string, apply func(*Intercept)) *Intercept {
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

func (s *State) RemoveIntercept(ctx context.Context, interceptID string) {
	if is, ok := s.intercepts.LoadAndDelete(interceptID); ok {
		is.terminate(s.backgroundCtx)
	}
}

func (s *State) UninstallAgents(ctx context.Context, ur *rpc.UninstallAgentsRequest) error {
	id := tunnel.SessionID(ur.GetSessionInfo().GetSessionId())
	clientInfo := s.GetClient(id)
	if clientInfo == nil {
		return grpcErrors.Errorf(codes.NotFound, "Client session %q not found", id)
	}
	ns := clientInfo.GetNamespace()
	mm := mutator.GetMap(ctx)
	agents := ur.Agents
	if len(agents) == 0 {
		if err := mm.EvictAllPodsWithAgentConfig(ctx, ns); err != nil {
			return grpcErrors.Errorf(codes.Internal, "unable to delete pods with agent: %v", err)
		}
		return nil
	}

	wls := make([]k8sapi.Workload, len(agents))
	for i, agent := range agents {
		wl, err := k8sapi.GetWorkload(ctx, agent, ns, "")
		if err != nil {
			return grpcErrors.Errorf(codes.NotFound, "Workload %s.%s not found", agent, ns)
		}
		wls[i] = wl
	}

	for _, wl := range wls {
		mm.Delete(wl.GetName(), ns)
		if err := mm.EvictPodsWithAgentConfig(ctx, wl); err != nil {
			return grpcErrors.Errorf(codes.Internal, "unable to delete agent for workload %s.%s: %v", wl.GetName(), ns, err)
		}
	}
	return nil
}

func (s *State) GetIntercept(interceptID string) (*Intercept, bool) {
	return s.intercepts.Load(interceptID)
}

func (s *State) WatchIntercepts(
	ctx context.Context,
	filter func(sessionID string, intercept *Intercept) bool,
) <-chan cache.Delta[string, *Intercept] {
	return s.intercepts.Subscribe(ctx.Done(), filter)
}

func (s *State) Tunnel(ctx context.Context, stream tunnel.Stream) error {
	id := stream.SessionID()
	if cs, ok := s.clients.Load(id); ok {
		return s.clientTunnel(ctx, cs, stream)
	}
	return grpcErrors.Errorf(codes.NotFound, "Session %q not found", id)
}

func (s *State) clientTunnel(ctx context.Context, client *ClientSession, stream tunnel.Stream) error {
	scm := client.ConsumptionMetrics()
	endPoint := tunnel.NewDialer(stream, func() {}, scm.FromClientBytes, scm.ToClientBytes)
	endPoint.Start(ctx)
	<-endPoint.Done()
	return nil
}

// SetTempLogLevel sets the temporary log-level for the traffic-manager and all agents and,
// if a duration is given, it also starts a timer that will reset the log-level once it
// fires.
func (s *State) SetTempLogLevel(ctx context.Context, logLevelRequest *rpc.LogLevelRequest) error {
	lvl, err := clog.ParseLevel(logLevelRequest.LogLevel)
	if err != nil {
		return err
	}
	duration := time.Duration(0)
	if gd := logLevelRequest.Duration; gd != nil {
		duration = gd.AsDuration()
	}
	s.timedLogLevel.Set(ctx, lvl, duration)
	s.llSubs.notify(ctx, logLevelRequest)
	return nil
}

// InitialTempLogLevel returns the temporary log-level if it exists, along with the remaining
// duration for it, which might be zero, in which case the log-level is valid until a new
// level is requested.
func (s *State) InitialTempLogLevel() *rpc.LogLevelRequest {
	level, duration := s.timedLogLevel.Get()
	if level == log.UnsetLevel {
		return nil
	}
	return &rpc.LogLevelRequest{
		LogLevel: level.String(),
		Duration: durationpb.New(duration),
	}
}

// WaitForTempLogLevel waits for a new temporary log-level request. It returns the values
// of the last request that was made.
func (s *State) WaitForTempLogLevel(stream rpc.Manager_WatchLogLevelServer) error {
	return s.llSubs.subscriberLoop(stream.Context(), stream)
}

func (s *State) SetPrometheusMetrics(
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

func (s *State) GetConnectCounter() *prometheus.CounterVec {
	return s.connectCounter
}

func (s *State) GetConnectActiveStatus() *prometheus.GaugeVec {
	return s.connectActiveStatusGauge
}

func (s *State) GetInterceptCounter() *prometheus.CounterVec {
	return s.interceptCounter
}

func (s *State) GetInterceptActiveStatus() *prometheus.GaugeVec {
	return s.interceptActiveStatusGauge
}

func (s *State) SetAllClientSessionsFinalizer(finalizer allClientSessionsFinalizer) {
	s.allClientSessionsFinalizer = finalizer
}

func (s *State) allClientSessionsFinalizerCall(client *ClientSession) {
	if s.allClientSessionsFinalizer != nil {
		s.allClientSessionsFinalizer(client)
	}
}

func (s *State) SetAllInterceptsFinalizer(finalizer allInterceptsFinalizer) {
	s.allInterceptsFinalizer = finalizer
}

func (s *State) allInterceptsFinalizerCall(client *ClientSession, workload *string) {
	if s.allInterceptsFinalizer != nil {
		s.allInterceptsFinalizer(client, workload)
	}
}

func (s *State) CountActiveInterceptsForWorkload(workloadKey *mutator.WorkloadKey) int {
	intercepts := s.intercepts.LoadMatching(func(_ string, ii *Intercept) bool {
		return ii.Disposition == rpc.InterceptDispositionType_ACTIVE &&
			ii.Spec.Agent == workloadKey.Name &&
			ii.Spec.Namespace == workloadKey.Namespace
	})
	return len(intercepts)
}
