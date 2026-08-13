package state

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	grpcErrors "github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tmconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type InterceptFinalizer func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error

type Intercept struct {
	*rpc.InterceptInfo
	finalizers   []InterceptFinalizer
	participants map[string]*interceptParticipant
}

func (is *Intercept) Clone() *Intercept {
	return &Intercept{
		InterceptInfo: proto.Clone(is.InterceptInfo).(*rpc.InterceptInfo),
		finalizers:    slices.Clone(is.finalizers),
		participants:  is.cloneParticipants(),
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
	leases                     *xsync.Map[leaseKey, struct{}]               // node-agent claims taken by EnsureAgent(node_agent=true), keyed by session+agent+namespace
	timedLogLevel              log.TimedLevel
	llSubs                     *loglevelSubscribers
	workloadWatchers           *xsync.Map[string, Watcher] // workload watchers, created on demand and keyed by namespace
	serviceInterceptWatchers   *xsync.Map[string, struct{}]

	// nodeAgentPodWatchers tracks the running per-workload node-agent pod-set
	// watchers (nodeAgentPodWatchLoop), one per name+namespace with at least
	// one claim, so that starting one is idempotent and its own goroutine can
	// remove its entry when it exits.
	nodeAgentPodWatchers *xsync.Map[nodeAgentWatchKey, struct{}]

	// nodeAgentWatchTimings overrides the pod-set watcher's intervals when
	// non-zero; the zero value selects the production constants (see
	// watchTimings).
	nodeAgentWatchTimings      nodeAgentWatchTimings
	tunnelCounter              int32
	tunnelIngressCounter       uint64
	tunnelEgressCounter        uint64
	connectCounter             *prometheus.CounterVec
	connectActiveStatusGauge   *prometheus.GaugeVec
	interceptCounter           *prometheus.CounterVec
	interceptActiveStatusGauge *prometheus.GaugeVec

	// lastAdminRun is the timestamp when the RunAdminCommands was last executed.
	lastAdminRun int64
}

func (s *State) ManagesNamespace(ctx context.Context, ns string) bool {
	return slices.Contains(namespaces.Get(ctx), ns)
}

func interceptEqual(a, b *Intercept) bool {
	return proto.Equal(a.InterceptInfo, b.InterceptInfo) && participantsEqual(a.participants, b.participants)
}

func agentsEqual(a, b *AgentSession) bool {
	return proto.Equal(a.AgentInfo, b.AgentInfo)
}

func NewState(ctx context.Context, g log.Group, adminCommandCh <-chan tmconfig.AdminCommandList) *State {
	loglevel, err := clog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		loglevel = slog.LevelInfo
	}
	s := &State{
		backgroundCtx:            ctx,
		intercepts:               cache.NewMap[string, *Intercept](interceptEqual, 5*time.Millisecond, xsync.WithGrowOnly()),
		agents:                   cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, 5*time.Millisecond, xsync.WithGrowOnly()),
		clients:                  xsync.NewMap[tunnel.SessionID, *ClientSession](xsync.WithGrowOnly()),
		leases:                   xsync.NewMap[leaseKey, struct{}](),
		workloadWatchers:         xsync.NewMap[string, Watcher](),
		nodeAgentPodWatchers:     xsync.NewMap[nodeAgentWatchKey, struct{}](),
		timedLogLevel:            log.NewTimedLevel(loglevel, clog.SetTreeLevel),
		llSubs:                   newLoglevelSubscribers(),
		serviceInterceptWatchers: xsync.NewMap[string, struct{}](),
	}
	g.Go("namespace-GC", s.pruneSessionGCLoop)
	g.Go("expired-GC", s.runSessionGCLoop)
	g.Go("node-agent-GC", s.reconcileNodeAgentJobsLoop)
	g.Go("admin-commands", func(ctx context.Context) error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case al := <-adminCommandCh:
				err := s.RunAdminCommands(al)
				if err != nil {
					clog.Errorf(ctx, "Error running admin commands: %v", err)
				}
			}
		}
	})
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
			s.expireSessions(now.Add(-clientTTL), now.Add(-agentSessionTTL))

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
			// Watchers first: closing them stops event delivery before the
			// session pruning below ends the streams that subscribe to them.
			s.pruneWorkloadWatchers(ctx)
			s.pruneSessions(ctx)
		}
	}
}

// pruneWorkloadWatchers closes and removes the workload watchers of
// namespaces that are no longer managed: a closed watcher's informer event
// handlers are removed, so it stops consuming events for a namespace nothing
// watches anymore, and the next WatchWorkloads for a re-managed namespace
// creates a fresh one.
func (s *State) pruneWorkloadWatchers(ctx context.Context) {
	nss := namespaces.Get(ctx)
	if nss == nil {
		// Cluster-global scope: every namespace is managed.
		return
	}
	s.workloadWatchers.Range(func(ns string, ww Watcher) bool {
		if ns == "" {
			// The cluster-wide watcher lives as long as the process.
			return true
		}
		if !slices.Contains(nss, ns) {
			s.workloadWatchers.Delete(ns)
			ww.Close()
		}
		return true
	})
}

// pruneSessions will remove all sessions that belong to namespaces that are no longer managed.
func (s *State) pruneSessions(ctx context.Context) {
	nss := namespaces.Get(ctx)
	s.clients.Range(func(id tunnel.SessionID, cs *ClientSession) bool {
		if !slices.Contains(nss, cs.Namespace) {
			s.clients.Delete(id)
			s.removeClientSession(cs)
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
		s.removeAgentSession(sid)
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

	serviceScoped := serviceScopedIntercept(intercept.Spec)
	if serviceScoped && len(intercept.participants) == 0 {
		return rpc.InterceptDispositionType_NO_AGENT,
			fmt.Sprintf("No agent found that claims service %q port %d", intercept.Spec.ServiceName, intercept.Spec.ServicePort)
	}

	groups := s.agentsByParticipant(intercept)
	keys := intercept.participantKeys()
	if !serviceScoped {
		keys = []string{participantKey(intercept.Spec.Namespace, intercept.Spec.WorkloadKind, intercept.Spec.Agent)}
	}
	for _, key := range keys {
		agentList := groups[key]
		participantName := intercept.Spec.Agent
		if participant := intercept.participants[key]; participant != nil {
			participantName = participant.name
		}
		switch {
		case len(agentList) == 0:
			return rpc.InterceptDispositionType_NO_AGENT, fmt.Sprintf("No agent found for %q", participantName)
		case serviceScoped && !allAgentsMatchIntercept(agentList, intercept.Spec):
			return rpc.InterceptDispositionType_NO_AGENT,
				fmt.Sprintf("Not every agent for %q advertises the selected Service target", participantName)
		case !managerutil.AgentsAreCompatible(agentList):
			return rpc.InterceptDispositionType_NO_AGENT, fmt.Sprintf("Agents for %q are not consistent", participantName)
		case !agentHasMechanism(agentList[0], intercept.Spec.Mechanism):
			return rpc.InterceptDispositionType_NO_MECHANISM,
				fmt.Sprintf("Agents for %q do not have mechanism %q", participantName, intercept.Spec.Mechanism)
		}
	}
	return rpc.InterceptDispositionType_UNSPECIFIED, ""
}

// Sessions: common ////////////////////////////////////////////////////////////////////////////////

// RemoveSession removes an AgentSession from the set of present session IDs.
func (s *State) RemoveSession(ctx context.Context, id tunnel.SessionID) {
	if cs, ok := s.clients.LoadAndDelete(id); ok {
		s.removeClientSession(cs)
	} else {
		s.removeAgentSession(id)
	}
}

// removeAgentSession removes an AgentSession from the set of present session IDs.
func (s *State) removeAgentSession(id tunnel.SessionID) {
	if as, loaded := s.agents.LoadAndDelete(id); loaded {
		clog.Debugf(s.backgroundCtx, "AgentSession %s removed. Explicit removal", id)
		mutator.GetMap(s.backgroundCtx).Inactivate(types.UID(as.PodUid))
		s.consolidateAgentSessionIntercepts(as)
	}
}

// removeClientSession removes an AgentSession from the set of present session IDs.
func (s *State) removeClientSession(cs *ClientSession) {
	clog.Debugf(s.backgroundCtx, "ClientSession %s removed. Explicit removal", cs.sessionID())

	// kill the session
	cs.cancel()
	s.gcClientSessionIntercepts(cs)
	s.gcClientSessionLeases(cs)
	scm := cs.consumptionMetrics
	atomic.AddUint64(&s.tunnelIngressCounter, scm.FromClientBytes.GetValue())
	atomic.AddUint64(&s.tunnelEgressCounter, scm.ToClientBytes.GetValue())
	s.allClientSessionsFinalizerCall(cs)
}

func (s *State) consolidateAgentSessionIntercepts(agent *AgentSession) {
	clog.Debugf(s.backgroundCtx, "Consolidating intercepts after removal of agent %s(%s)", agent.PodName, agent.PodIp)
	s.intercepts.Range(func(interceptID string, intercept *Intercept) bool {
		serviceScoped := serviceScopedIntercept(intercept.Spec)
		if intercept.Disposition == rpc.InterceptDispositionType_REMOVED ||
			(!serviceScoped &&
				(!AgentMatchesIntercept(agent.AgentInfo, intercept.Spec) || agent.PodIp != intercept.PodIp)) {
			// Not of interest. Continue iteration.
			return true
		}
		participantKey := agentParticipantKey(agent.AgentInfo)
		if serviceScoped {
			intercept = s.reconcileServiceParticipants(interceptID, intercept)
			if intercept == nil {
				return true
			}
			if _, ok := intercept.participants[participantKey]; !ok {
				return true
			}
		}

		removedParticipantReview := false
		if serviceScoped {
			if participant := intercept.participants[participantKey]; participant != nil {
				removedParticipantReview = participant.podName == agent.PodName
			}
		}

		errCode, errMsg := s.checkAgentsForIntercept(intercept)
		switch {
		case errCode != rpc.InterceptDispositionType_UNSPECIFIED:
			// No agents matching this intercept are available, so the intercept is now dormant or in error.
			clog.Debugf(s.backgroundCtx, "Intercept %q no longer has available agents. Setting its disposition to %s", interceptID, errCode)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				if serviceScoped {
					if participant := intercept.participants[participantKey]; participant != nil && participant.podName == agent.PodName {
						participant.review = nil
						participant.podName = ""
					}
				} else {
					intercept.PodIp = ""
					intercept.PodName = ""
				}
				intercept.Disposition = errCode
				intercept.Message = errMsg
			})
		case serviceScoped && removedParticipantReview:
			// A different pod in the same workload can keep serving traffic,
			// but it must review the intercept before that workload is an
			// active participant again. Preserve the primary pod recorded on
			// the logical intercept while clearing only this workload's review.
			clog.Debugf(s.backgroundCtx, "Intercept %q lost reviewing pod %s(%s). Setting its disposition to WAITING", interceptID, agent.PodName, agent.PodIp)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				if participant := intercept.participants[participantKey]; participant != nil && participant.podName == agent.PodName {
					participant.review = nil
					participant.podName = ""
				}
				intercept.Disposition = rpc.InterceptDispositionType_WAITING
				intercept.Message = fmt.Sprintf("Waiting for Agent approval from workloads: %s", strings.Join(intercept.pendingParticipants(), ", "))
			})
		case serviceScoped && intercept.Disposition == rpc.InterceptDispositionType_NO_AGENT:
			// A non-claiming replica can hold a participant in NO_AGENT while
			// it is present. Once it leaves, let the remaining agents review the
			// intercept again if every current participant is healthy.
			clog.Debugf(s.backgroundCtx, "Intercept %q has healthy participant agents again. Setting its disposition to WAITING", interceptID)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.Disposition = rpc.InterceptDispositionType_WAITING
				intercept.Message = ""
			})
		case !serviceScoped:
			// The agent is about to die, but apparently more agents are present. Let some other agent pick it up then.
			clog.Debugf(s.backgroundCtx, "Intercept %q lost its agent pod %s(%s). Setting its disposition to WAITING", interceptID, agent.PodName, agent.PodIp)
			s.UpdateIntercept(interceptID, func(intercept *Intercept) {
				intercept.PodIp = ""
				intercept.PodName = ""
				intercept.Disposition = rpc.InterceptDispositionType_WAITING
			})
		}
		return true
	})
}

func (s *State) gcClientSessionIntercepts(client *ClientSession) {
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
			s.RemoveIntercept(interceptID)
		}
		return true
	})
}

// gcClientSessionLeases drops every node-agent lease held by client and, for
// each (name, namespace) that is no longer wanted by anything else, reaps
// its node-agent Job. By the time this runs, client has already been
// removed from s.clients (RemoveSession deletes it before calling
// removeClientSession), so nodeAgentWanted never sees client's own leases as
// live.
func (s *State) gcClientSessionLeases(client *ClientSession) {
	sid := client.sessionID()
	var released []leaseKey
	s.leases.Range(func(k leaseKey, _ struct{}) bool {
		if k.sessionID == sid {
			released = append(released, k)
		}
		return true
	})
	for _, k := range released {
		s.leases.Delete(k)
		if !s.nodeAgentWanted(k.name, k.namespace) {
			if err := reapNodeAgentJobs(s.backgroundCtx, k.name, k.namespace); err != nil {
				clog.Errorf(s.backgroundCtx, "failed to reap node-agent job for %s.%s after session %s ended: %v", k.name, k.namespace, sid, err)
			}
		}
	}
}

// expireSessions prunes any sessions that haven't had a MarkSession heartbeat since
// respective given 'moment'.
func (s *State) expireSessions(clientMoment, agentMoment time.Time) {
	s.clients.Range(func(id tunnel.SessionID, client *ClientSession) bool {
		moment := clientMoment
		if client.lastMarked().Before(moment) {
			s.clients.Delete(id)
			s.removeClientSession(client)
		}
		return true
	})
	s.agents.Range(func(id tunnel.SessionID, agent *AgentSession) bool {
		moment := agentMoment
		if agent.lastMarked().Before(moment) {
			s.removeAgentSession(id)
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

func (s *State) AddClient(client *rpc.ClientInfo, principal *auth.Principal, now time.Time) tunnel.SessionID {
	// Use non-sequential things (i.e., UUIDs, not just a counter) as the session ID, because
	// the session ID also exists in external systems (the client, SystemA), so it's confusing
	// (to both humans and computers) if the manager restarts and those existing session IDs
	// suddenly refer to different sessions.
	sessionID := tunnel.SessionID(uuid.New().String())
	s.addClient(sessionID, client, principal, now)
	return sessionID
}

func (s *State) RestoreClient(sessionID tunnel.SessionID, client *rpc.ClientInfo, principal *auth.Principal, now time.Time) {
	s.addClient(sessionID, client, principal, now)
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
	nodeAgentWatches := make(map[nodeAgentWatchKey]struct{})
	serviceInterceptWatches := make(map[string]struct{})
	for _, intercept := range intercepts {
		s.intercepts.LoadOrCompute(intercept.Id, func() *Intercept {
			spec := intercept.Spec
			is := &Intercept{InterceptInfo: intercept}
			s.initializeParticipants(is)
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
				if spec.NodeAgent {
					is.addFinalizer(s.nodeAgentReapFinalizer())
					nodeAgentWatches[nodeAgentWatchKey{name: spec.Agent, namespace: spec.Namespace}] = struct{}{}
				}
				if serviceScopedIntercept(spec) {
					serviceInterceptWatches[intercept.Id] = struct{}{}
				}
			}
			return is
		})
	}
	// The watches are started only after the intercepts are stored: a watcher
	// checks nodeAgentWanted as soon as it starts, and exits unless it can
	// already observe the claim it was started for.
	for key := range nodeAgentWatches {
		s.startNodeAgentPodWatch(key.name, key.namespace)
	}
	for interceptID := range serviceInterceptWatches {
		s.startServiceInterceptWatch(interceptID)
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
func (s *State) addClient(id tunnel.SessionID, client *rpc.ClientInfo, principal *auth.Principal, now time.Time) {
	cs := newClientSessionState(s.backgroundCtx, id, client, now)
	if principal != nil {
		cs.SetPrincipal(principal)
	}
	if oldClient, hasConflict := s.clients.LoadOrStore(id, cs); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", id, oldClient, client))
	}
}

func (s *State) GetClient(id tunnel.SessionID) *ClientSession {
	ret, _ := s.clients.Load(id)
	return ret
}

// ClientOwnershipError returns an error when client is bound to a verified
// principal and the caller's principal (carried by ctx) doesn't match it (see
// auth.Principal.SameAs). Returns nil when the session is unowned (an older
// client) or the caller is the bound identity. A caller whose token couldn't
// be verified for infrastructure reasons gets Unavailable instead of
// PermissionDenied, since ownership could not be established either way.
func ClientOwnershipError(ctx context.Context, sessionID tunnel.SessionID, client *ClientSession) error {
	bound := client.Principal()
	if bound == nil {
		return nil
	}
	if p := auth.PrincipalFrom(ctx); p != nil && bound.SameAs(p) {
		return nil
	}
	if auth.AuthUnavailable(ctx) {
		return grpcErrors.Errorf(codes.Unavailable, "cannot verify session ownership: authentication unavailable")
	}
	return grpcErrors.Errorf(codes.PermissionDenied, "client session %q is bound to another identity", sessionID)
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

// IsInterceptedBy reports whether this agent pod is serving an ACTIVE
// intercept owned by the given client. Every agent pod that matches an
// intercept needs a dial watcher, regardless of mechanism or filters.
// Service-scoped intercepts extend matching to agents that advertise the
// selected Service target and still belong to the current participant set.
func (s *State) IsInterceptedBy(agent *AgentSession, client tunnel.SessionID) (found bool) {
	if agent == nil {
		return false
	}
	clientSessionID := string(client)
	s.intercepts.Range(func(id string, ii *Intercept) bool {
		if ii.ClientSession.SessionId != clientSessionID || ii.Disposition != rpc.InterceptDispositionType_ACTIVE {
			return true
		}
		if !AgentMatchesInterceptInfo(agent.AgentInfo, ii) {
			return true
		}
		found = true
		return false
	})
	return found
}

// Sessions: Agents ////////////////////////////////////////////////////////////////////////////////

func (s *State) AddAgent(ctx context.Context, agent *rpc.AgentInfo, principal *auth.Principal, now time.Time) (tunnel.SessionID, error) {
	if mutator.GetMap(ctx).IsInactive(types.UID(agent.PodUid)) {
		return "", status.Error(codes.Aborted, "inactivated pod")
	}
	return s.RestoreAgent(ctx, tunnel.SessionID(AgentSessionIDPrefix+agent.PodUid), agent, principal, now)
}

func (s *State) RestoreAgent(ctx context.Context, id tunnel.SessionID, agent *rpc.AgentInfo, principal *auth.Principal, now time.Time) (tunnel.SessionID, error) {
	as := newAgentSessionState(s.backgroundCtx, id, agent, now)
	if principal != nil {
		as.SetPrincipal(principal)
	}
	if _, exists := s.agents.LoadOrStore(id, as); exists {
		return id, nil
	}

	s.intercepts.Range(func(interceptID string, intercept *Intercept) bool {
		if intercept.Disposition == rpc.InterceptDispositionType_REMOVED {
			return true
		}
		matches := AgentMatchesIntercept(agent, intercept.Spec)
		if serviceScopedIntercept(intercept.Spec) {
			participantKey := agentParticipantKey(agent)
			wasParticipant := intercept.participants[participantKey] != nil
			intercept = s.reconcileServiceParticipants(interceptID, intercept)
			if intercept == nil {
				return true
			}
			if !matches {
				if !wasParticipant {
					return true
				}
			} else {
				// All pods in one workload share a participant key. Avoid entering
				// UpdateIntercept once the workload is already represented; otherwise a
				// burst of pods needlessly contends on the same intercept record.
				if _, exists := intercept.participants[participantKey]; !exists {
					if _, selectionKnown := s.selectedServiceParticipants(intercept); selectionKnown {
						// The Service selector is authoritative when its
						// informer state is available. An old agent can keep
						// advertising a target after its workload was
						// pruned, but must not rejoin the intercept.
						return true
					}
					intercept = s.UpdateIntercept(interceptID, func(intercept *Intercept) {
						if _, exists := intercept.participants[participantKey]; exists {
							return
						}
						intercept.addParticipant(agent)
						if intercept.Disposition == rpc.InterceptDispositionType_ACTIVE {
							intercept.Disposition = rpc.InterceptDispositionType_WAITING
							intercept.Message = fmt.Sprintf("Waiting for Agent approval from workloads: %s",
								strings.Join(intercept.pendingParticipants(), ", "))
						}
					})
					if intercept == nil {
						return true
					}
				}
			}
		} else if !matches {
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
	// Cluster-wide scope: informers are already cluster-wide (informer.GetFactory
	// returns the "" factory for every namespace), so one watcher and one handler
	// set serves every subscription; each subscription filters to its own
	// namespace at delivery time (see watcher.go).
	key := ns
	if namespaces.Get(ctx) == nil {
		key = ""
	}
	ww, _ := s.workloadWatchers.LoadOrCompute(key, func() (ww Watcher, rm bool) {
		ww, err = NewWatcher(s.backgroundCtx, key, managerutil.GetEnv(ctx).EnabledWorkloadKinds)
		return ww, err != nil // delete if error.
	})
	if err != nil {
		return nil, err
	}
	return ww.Subscribe(ctx, ns), nil
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

// ApplyAgentReview applies an agent's intercept review. Service-scoped
// intercepts wait for one approval from each participating workload before
// they become active; workload-scoped intercepts retain the historical
// first-review-wins behavior.
func (s *State) ApplyAgentReview(ctx context.Context, interceptID string, agent *AgentSession, review *rpc.ReviewInterceptRequest) *Intercept {
	return s.UpdateIntercept(interceptID, func(intercept *Intercept) {
		if !AgentMatchesInterceptInfo(agent.AgentInfo, intercept) {
			return
		}
		if mutator.GetMap(ctx).IsInactive(types.UID(agent.PodUid)) {
			clog.Debugf(ctx, "Pod %s(%s) is blacklisted", agent.PodName, agent.PodIp)
			return
		}

		// Agents race to review an intercept, so only reviews for waiting
		// intercepts can change its state.
		if intercept.Disposition != rpc.InterceptDispositionType_NO_AGENT &&
			intercept.Disposition != rpc.InterceptDispositionType_WAITING {
			return
		}
		if serviceScopedIntercept(intercept.Spec) {
			intercept.applyServiceReview(agent.AgentInfo, review)
			return
		}

		applyReview(intercept, review)
		intercept.PodName = agent.PodName
	})
}

func (s *State) RemoveIntercept(interceptID string) {
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
		if err := ClientOwnershipError(ctx, id, cs); err != nil {
			return err
		}
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
func (s *State) WaitForTempLogLevel(stream grpc.ServerStreamingServer[rpc.LogLevelRequest]) error {
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
		if ii.Disposition != rpc.InterceptDispositionType_ACTIVE {
			return false
		}
		if ii.Spec.Agent == workloadKey.Name && ii.Spec.Namespace == workloadKey.Namespace {
			return true
		}
		for _, workload := range ii.ServiceWorkloads {
			if workload != nil &&
				workload.WorkloadName == workloadKey.Name &&
				workload.Namespace == workloadKey.Namespace &&
				workload.WorkloadKind == string(workloadKey.Kind) {
				return true
			}
		}
		return false
	})
	return len(intercepts)
}
