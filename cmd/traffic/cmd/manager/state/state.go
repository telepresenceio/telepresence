package state

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/go-multierror"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/watchable"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type State interface {
	AddAgent(*rpc.AgentInfo, time.Time) string
	AddClient(*rpc.ClientInfo, time.Time) string
	AddIntercept(string, string, *rpc.ClientInfo, *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error)
	AddInterceptFinalizer(string, InterceptFinalizer) error
	AgentsLookupDNS(context.Context, string, *rpc.DNSRequest) (dnsproxy.RRs, int, error)
	CountAgents() int
	CountClients() int
	CountIntercepts() int
	CountSessions() int
	CountTunnels() int
	ExpireSessions(context.Context, time.Time, time.Time)
	GetAgent(string) *rpc.AgentInfo
	GetAllClients() map[string]*rpc.ClientInfo
	GetClient(string) *rpc.ClientInfo
	GetSession(string) SessionState
	GetSessionConsumptionMetrics(string) *SessionConsumptionMetrics
	GetAllSessionConsumptionMetrics() map[string]*SessionConsumptionMetrics
	GetIntercept(string) (*rpc.InterceptInfo, bool)
	MarkSession(*rpc.RemainRequest, time.Time) bool
	NewInterceptInfo(string, *rpc.SessionInfo, *rpc.CreateInterceptRequest) *rpc.InterceptInfo
	PostLookupDNSResponse(context.Context, *rpc.DNSAgentResponse)
	PrepareIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.PreparedIntercept, error)
	RemoveIntercept(context.Context, string) (bool, error)
	RemoveSession(context.Context, string) error
	SessionDone(string) (<-chan struct{}, error)
	SetTempLogLevel(context.Context, *rpc.LogLevelRequest)
	Tunnel(context.Context, tunnel.Stream) error
	UpdateIntercept(string, func(*rpc.InterceptInfo)) *rpc.InterceptInfo
	UpdateClient(sessionID string, apply func(*rpc.ClientInfo)) *rpc.ClientInfo
	RefreshSessionConsumptionMetrics(sessionID string)
	ValidateAgentImage(string, bool) error
	WaitForTempLogLevel(rpc.Manager_WatchLogLevelServer) error
	WatchAgents(context.Context, func(sessionID string, agent *rpc.AgentInfo) bool) <-chan watchable.Snapshot[*rpc.AgentInfo]
	WatchDial(sessionID string) <-chan *rpc.DialRequest
	WatchIntercepts(context.Context, func(sessionID string, intercept *rpc.InterceptInfo) bool) <-chan watchable.Snapshot[*rpc.InterceptInfo]
	WatchLookupDNS(string) <-chan *rpc.DNSRequest
}

type cleanupWaiter func() error

type interceptFinalizerCall struct {
	state *interceptState
	info  *rpc.InterceptInfo
	errCh chan error
}

// state is the total state of the Traffic Manager.  A zero state is invalid; you must call
// NewState.
type state struct {
	// backgroundCtx is the context passed into the state by its owner. It's used for things that
	// need to exceed the context of a request into the state object, e.g. session contexts.
	backgroundCtx context.Context

	interceptFinalizerCh chan *interceptFinalizerCall

	mu sync.RWMutex
	// Things protected by 'mu': While the watchable.WhateverMaps have their own locking to
	// protect against memory corruption and ensure serialization for watches, we need to do our
	// own locking here to ensure consistency between the various maps:
	//
	//  1. `agents` needs to stay in-sync with `sessions`
	//  2. `clients` needs to stay in-sync with `sessions`
	//  3. `port` needs to be updated in-sync with `intercepts`
	//  4. `agentsByName` needs stay in-sync with `agents`
	//  5. `intercepts` needs to be pruned in-sync with `clients` (based on
	//     `intercept.ClientSession.SessionId`)
	//  6. `intercepts` needs to be pruned in-sync with `agents` (based on
	//     `agent.Name == intercept.Spec.Agent`)
	//  7. `cfgMapLocks` access must be concurrency protected
	//  8. `cachedAgentImage` access must be concurrency protected
	//  9. `interceptState` must be concurrency protected and updated/deleted in sync with intercepts
	intercepts      watchable.Map[*rpc.InterceptInfo]    // info for intercepts, keyed by intercept id
	agents          watchable.Map[*rpc.AgentInfo]        // info for agent sessions, keyed by session id
	clients         watchable.Map[*rpc.ClientInfo]       // info for client sessions, keyed by session id
	sessions        map[string]SessionState              // info for all sessions, keyed by session id
	agentsByName    map[string]map[string]*rpc.AgentInfo // indexed copy of `agents`
	interceptStates map[string]*interceptState
	timedLogLevel   log.TimedLevel
	llSubs          *loglevelSubscribers
	cfgMapLocks     map[string]*sync.Mutex
	tunnelCounter   int32

	// Possibly extended version of the state. Use when calling interface methods.
	self State
}

var NewStateFunc = NewState //nolint:gochecknoglobals // extension point

func NewState(ctx context.Context) State {
	loglevel := os.Getenv("LOG_LEVEL")
	s := &state{
		backgroundCtx:        ctx,
		sessions:             make(map[string]SessionState),
		agentsByName:         make(map[string]map[string]*rpc.AgentInfo),
		cfgMapLocks:          make(map[string]*sync.Mutex),
		interceptStates:      make(map[string]*interceptState),
		timedLogLevel:        log.NewTimedLevel(loglevel, log.SetLevel),
		llSubs:               newLoglevelSubscribers(),
		interceptFinalizerCh: make(chan *interceptFinalizerCall),
	}
	go s.runInterceptFinalizerQueue()
	s.self = s
	return s
}

func (s *state) SetSelf(self State) {
	s.self = self
}

// unlockedCheckAgentsForIntercept (1) assumes that s.mu is already locked, and (2) checks the
// status of all agents that would be relevant to the given intercept spec, and returns whether the
// state of those agents would require transitioning to an error state.  If everything looks good,
// it returns the zero error code (InterceptDispositionType_UNSPECIFIED).
func (s *state) unlockedCheckAgentsForIntercept(intercept *rpc.InterceptInfo) (errCode rpc.InterceptDispositionType, errMsg string) {
	// Don't overwrite an existing error state
	switch intercept.Disposition {
	// non-error states ////////////////////////////////////////////////////
	case rpc.InterceptDispositionType_UNSPECIFIED:
		// Continue through; we can trasition to an error state from here.
	case rpc.InterceptDispositionType_ACTIVE:
		// Continue through; we can trasition to an error state from here.
	case rpc.InterceptDispositionType_WAITING:
		// Continue through; we can trasition to an error state from here.
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
	}

	// main ////////////////////////////////////////////////////////////////

	agentSet := s.agentsByName[intercept.Spec.Agent]

	agentList := make([]*rpc.AgentInfo, 0)
	for _, agent := range agentSet {
		if agent.Namespace == intercept.Spec.Namespace {
			agentList = append(agentList, agent)
		}
	}

	if len(agentList) == 0 {
		errCode = rpc.InterceptDispositionType_NO_AGENT
		errMsg = fmt.Sprintf("No agent found for %q", intercept.Spec.Agent)
		return
	}

	if !managerutil.AgentsAreCompatible(agentList) {
		errCode = rpc.InterceptDispositionType_NO_AGENT
		errMsg = fmt.Sprintf("Agents for %q are not consistent", intercept.Spec.Agent)
		return
	}

	if !agentHasMechanism(agentList[0], intercept.Spec.Mechanism) {
		errCode = rpc.InterceptDispositionType_NO_MECHANISM
		errMsg = fmt.Sprintf("Agents for %q do not have mechanism %q", intercept.Spec.Agent, intercept.Spec.Mechanism)
		return
	}

	return rpc.InterceptDispositionType_UNSPECIFIED, ""
}

// Sessions: common ////////////////////////////////////////////////////////////////////////////////

// MarkSession marks a session as being present at the indicated time.  Returns true if everything goes OK,
// returns false if the given session ID does not exist.
func (s *state) MarkSession(req *rpc.RemainRequest, now time.Time) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := req.Session.SessionId

	if sess, ok := s.sessions[sessionID]; ok {
		sess.SetLastMarked(now)
		return true
	}

	return false
}

func (s *state) GetSession(sessionID string) SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

// RemoveSession removes a session from the set of present session IDs.
func (s *state) RemoveSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	dlog.Debugf(ctx, "Session %s removed. Explicit removal", sessionID)
	wait := s.unlockedRemoveSession(sessionID)
	s.mu.Unlock()

	return wait()
}

func (s *state) gcSessionIntercepts(sessionID string) cleanupWaiter {
	agent, isAgent := s.agents.Load(sessionID)

	wait := func() error { return nil }

	// GC any intercepts that relied on this session; prune any intercepts that
	//  1. Don't have a client session (intercept.ClientSession.SessionId)
	//  2. Don't have any agents (agent.Name == intercept.Spec.Agent)
	// Alternatively, if the intercept is still live but has been switched over to a different agent, send it back to WAITING state
	for interceptID, intercept := range s.intercepts.LoadAll() {
		if intercept.ClientSession.SessionId == sessionID {
			// Client went away:
			// Delete it.
			_, iceptWait := s.unlockedRemoveIntercept(interceptID)
			newWait := func() error {
				err := wait()
				err = multierror.Append(err, iceptWait())
				return err
			}
			wait = newWait
		} else if errCode, errMsg := s.unlockedCheckAgentsForIntercept(intercept); errCode != 0 {
			// Refcount went to zero:
			// Tell the client, so that the client can tell us to delete it.
			intercept.Disposition = errCode
			intercept.Message = errMsg
			s.intercepts.Store(interceptID, intercept)
		} else if isAgent && agent.PodIp == intercept.PodIp {
			// The agent whose podIP was stored by the intercept is dead, but it's not the last agent
			// Send it back to waiting so that one of the other agents can pick it up and set their own podIP
			intercept.Disposition = rpc.InterceptDispositionType_WAITING
			s.intercepts.Store(interceptID, intercept)
		}
	}

	return wait
}

func (s *state) unlockedRemoveSession(sessionID string) cleanupWaiter {
	wait := func() error { return nil }
	if sess, ok := s.sessions[sessionID]; ok {
		// kill the session
		defer sess.Cancel()

		wait = s.gcSessionIntercepts(sessionID)

		agent, isAgent := s.agents.Load(sessionID)
		if isAgent {
			// remove it from the agentsByName index (if nescessary)

			delete(s.agentsByName[agent.Name], sessionID)
			if len(s.agentsByName[agent.Name]) == 0 {
				delete(s.agentsByName, agent.Name)
			}
			// remove the session
			s.agents.Delete(sessionID)
		} else {
			s.clients.Delete(sessionID)
		}
		delete(s.sessions, sessionID)
	}
	return wait
}

// ExpireSessions prunes any sessions that haven't had a MarkSession heartbeat since
// respective given 'moment'.
func (s *state) ExpireSessions(ctx context.Context, clientMoment, agentMoment time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reportErr := func(id string, wait cleanupWaiter) {
		// We don't really have a user to report this to anyway, so just wait wherever and report there.
		if err := wait(); err != nil {
			dlog.Errorf(ctx, "Error cleaning up client session %s: %v", id, err)
		}
	}

	for id, sess := range s.sessions {
		if _, ok := sess.(*clientSessionState); ok {
			if sess.LastMarked().Before(clientMoment) {
				dlog.Debugf(ctx, "Client Session %s removed. It has expired", id)
				wait := s.unlockedRemoveSession(id)
				go reportErr(id, wait)
			}
		} else {
			if sess.LastMarked().Before(agentMoment) {
				dlog.Debugf(ctx, "Agent Session %s removed. It has expired", id)
				wait := s.unlockedRemoveSession(id)
				go reportErr(id, wait)
			}
		}
	}
}

// SessionDone returns a channel that is closed when the session with the given ID terminates.  If
// there is no such currently-live session, then an already-closed channel is returned.
func (s *state) SessionDone(id string) (<-chan struct{}, error) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", id)
	}
	return sess.Done(), nil
}

// Sessions: Clients ///////////////////////////////////////////////////////////////////////////////

func (s *state) AddClient(client *rpc.ClientInfo, now time.Time) string {
	// Use non-sequential things (i.e., UUIDs, not just a counter) as the session ID, because
	// the session ID also exists in external systems (the client, SystemA), so it's confusing
	// (to both humans and computers) if the manager restarts and those existing session IDs
	// suddenly refer to different sessions.
	sessionID := uuid.New().String()
	return s.addClient(sessionID, client, now)
}

// addClient is like AddClient, but takes a sessionID, for testing purposes.
func (s *state) addClient(sessionID string, client *rpc.ClientInfo, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if oldClient, hasConflict := s.clients.LoadOrStore(sessionID, client); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", sessionID, oldClient, client))
	}

	s.sessions[sessionID] = newClientSessionState(s.backgroundCtx, now)
	return sessionID
}

func (s *state) GetClient(sessionID string) *rpc.ClientInfo {
	ret, _ := s.clients.Load(sessionID)
	return ret
}

func (s *state) GetAllClients() map[string]*rpc.ClientInfo {
	return s.clients.LoadAll()
}

func (s *state) CountAgents() int {
	return s.agents.CountAll()
}

func (s *state) CountClients() int {
	return s.clients.CountAll()
}

func (s *state) CountIntercepts() int {
	return s.intercepts.CountAll()
}

func (s *state) CountSessions() int {
	s.mu.RLock()
	count := len(s.sessions)
	s.mu.RUnlock()
	return count
}

func (s *state) CountTunnels() int {
	return int(atomic.LoadInt32(&s.tunnelCounter))
}

// Sessions: Agents ////////////////////////////////////////////////////////////////////////////////

func (s *state) AddAgent(agent *rpc.AgentInfo, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionID := AgentSessionIDPrefix + uuid.New().String()
	if oldAgent, hasConflict := s.agents.LoadOrStore(sessionID, agent); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", sessionID, oldAgent, agent))
	}

	if s.agentsByName[agent.Name] == nil {
		s.agentsByName[agent.Name] = make(map[string]*rpc.AgentInfo)
	}
	s.agentsByName[agent.Name][sessionID] = agent
	s.sessions[sessionID] = newAgentSessionState(s.backgroundCtx, now)

	for interceptID, intercept := range s.intercepts.LoadAll() {
		// Check whether each intercept needs to either (1) be moved in to a NO_AGENT state
		// because this agent made things inconsistent, or (2) be moved out of a NO_AGENT
		// state because it just gained an agent.
		if errCode, errMsg := s.unlockedCheckAgentsForIntercept(intercept); errCode != 0 {
			intercept.Disposition = errCode
			intercept.Message = errMsg
			s.intercepts.Store(interceptID, intercept)
		} else if intercept.Disposition == rpc.InterceptDispositionType_NO_AGENT {
			intercept.Disposition = rpc.InterceptDispositionType_WAITING
			intercept.Message = ""
			s.intercepts.Store(interceptID, intercept)
		}
	}
	return sessionID
}

func (s *state) GetAgent(sessionID string) *rpc.AgentInfo {
	ret, _ := s.agents.Load(sessionID)
	return ret
}

func (s *state) getAllAgents() map[string]*rpc.AgentInfo {
	return s.agents.LoadAll()
}

func (s *state) getAgentsByName(name, namespace string) map[string]*rpc.AgentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ret := make(map[string]*rpc.AgentInfo, len(s.agentsByName[name]))
	for k, v := range s.agentsByName[name] {
		if v.Namespace == namespace {
			ret[k] = proto.Clone(v).(*rpc.AgentInfo)
		}
	}

	return ret
}

func (s *state) WatchAgents(
	ctx context.Context,
	filter func(sessionID string, agent *rpc.AgentInfo) bool,
) <-chan watchable.Snapshot[*rpc.AgentInfo] {
	if filter == nil {
		return s.agents.Subscribe(ctx)
	} else {
		return s.agents.SubscribeSubset(ctx, filter)
	}
}

// Intercepts //////////////////////////////////////////////////////////////////////////////////////

func (s *state) AddIntercept(sessionID, clusterID string, client *rpc.ClientInfo, cir *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID].(*clientSessionState)
	if sess == nil || !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", sessionID)
	}

	spec := cir.InterceptSpec
	interceptID := fmt.Sprintf("%s:%s", sessionID, spec.Name)
	installID := client.GetInstallId()
	clientSession := rpc.SessionInfo{
		SessionId: sessionID,
		ClusterId: clusterID,
		InstallId: &installID,
	}

	cept := s.self.NewInterceptInfo(interceptID, &clientSession, cir)

	// Wrap each potential-state-change in a
	//
	//     if cept.Disposition == rpc.InterceptDispositionType_WAITING { … }
	//
	// so that we don't need to worry about different state-changes stomping on eachother.
	if cept.Disposition == rpc.InterceptDispositionType_WAITING {
		if errCode, errMsg := s.unlockedCheckAgentsForIntercept(cept); errCode != 0 {
			cept.Disposition = errCode
			cept.Message = errMsg
		}
	}

	if _, hasConflict := s.intercepts.LoadOrStore(cept.Id, cept); hasConflict {
		return nil, status.Errorf(codes.AlreadyExists, "Intercept named %q already exists", spec.Name)
	}

	state := newInterceptState(cept.Id)
	s.interceptStates[interceptID] = state

	return cept, nil
}

func (s *state) NewInterceptInfo(interceptID string, session *rpc.SessionInfo, ciReq *rpc.CreateInterceptRequest) *rpc.InterceptInfo {
	return &rpc.InterceptInfo{
		Spec:          ciReq.InterceptSpec,
		Disposition:   rpc.InterceptDispositionType_WAITING,
		Message:       "Waiting for Agent approval",
		Id:            interceptID,
		ClientSession: session,
	}
}

func (s *state) AddInterceptFinalizer(interceptID string, finalizer InterceptFinalizer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	is, ok := s.interceptStates[interceptID]
	if !ok {
		return status.Errorf(codes.NotFound, "no such intercept %s", interceptID)
	}
	is.addFinalizer(finalizer)
	return nil
}

// getAgentsInterceptedByClient returns the session IDs for each agent that are currently
// intercepted by the client with the given client session ID.
func (s *state) getAgentsInterceptedByClient(clientSessionID string) map[string]*rpc.AgentInfo {
	intercepts := s.intercepts.LoadAllMatching(func(_ string, ii *rpc.InterceptInfo) bool {
		return ii.ClientSession.SessionId == clientSessionID
	})
	if len(intercepts) == 0 {
		return nil
	}
	return s.agents.LoadAllMatching(func(_ string, ai *rpc.AgentInfo) bool {
		for _, ii := range intercepts {
			if ai.Name == ii.Spec.Agent && ai.Namespace == ii.Spec.Namespace {
				return true
			}
		}
		return false
	})
}

// getAgentsInNamespace returns the session IDs the agents in the given namespace.
func (s *state) getAgentsInNamespace(namespace string) map[string]*rpc.AgentInfo {
	return s.agents.LoadAllMatching(func(_ string, ii *rpc.AgentInfo) bool {
		return ii.Namespace == namespace
	})
}

// UpdateIntercept applies a given mutator function to the stored intercept with interceptID;
// storing and returning the result.  If the given intercept does not exist, then the mutator
// function is not run, and nil is returned.
//
// This does not lock; but instead uses CAS and may therefore call the mutator function multiple
// times.  So: it is safe to perform blocking operations in your mutator function, but you must take
// care that it is safe to call your mutator function multiple times.
func (s *state) UpdateIntercept(interceptID string, apply func(*rpc.InterceptInfo)) *rpc.InterceptInfo {
	for {
		cur, ok := s.intercepts.Load(interceptID)
		if !ok || cur == nil {
			// Doesn't exist (possibly was deleted while this loop was running).
			return nil
		}

		newInfo := proto.Clone(cur).(*rpc.InterceptInfo)
		apply(newInfo)

		swapped := s.intercepts.CompareAndSwap(newInfo.Id, cur, newInfo)
		if swapped {
			// Success!
			return newInfo
		}
	}
}

// UpdateClient applies a given mutator function to the stored client with sessionID;
// storing and returning the result.  If the given client does not exist, then the mutator
// function is not run, and nil is returned.
func (s *state) UpdateClient(sessionID string, apply func(*rpc.ClientInfo)) *rpc.ClientInfo {
	for {
		cur, ok := s.clients.Load(sessionID)
		if !ok || cur == nil {
			// Doesn't exist (possibly was deleted while this loop was running).
			return nil
		}

		newInfo := proto.Clone(cur).(*rpc.ClientInfo)
		apply(newInfo)

		swapped := s.clients.CompareAndSwap(sessionID, cur, newInfo)
		if swapped {
			return newInfo
		}
	}
}

func (s *state) RemoveIntercept(ctx context.Context, interceptID string) (bool, error) {
	s.mu.Lock()
	removed, wait := s.unlockedRemoveIntercept(interceptID)
	s.mu.Unlock()
	return removed, wait()
}

func (s *state) unlockedRemoveIntercept(interceptID string) (bool, cleanupWaiter) {
	intercept, didDelete := s.intercepts.LoadAndDelete(interceptID)
	wait := func() error { return nil }
	if state, ok := s.interceptStates[interceptID]; ok && didDelete {
		delete(s.interceptStates, interceptID)
		call := &interceptFinalizerCall{
			state: state,
			info:  intercept,
			errCh: make(chan error),
		}
		s.interceptFinalizerCh <- call
		wait = func() error {
			return <-call.errCh
		}
	}

	return didDelete, wait
}

func (s *state) runInterceptFinalizerQueue() {
	for {
		select {
		case call := <-s.interceptFinalizerCh:
			call.errCh <- call.state.terminate(s.backgroundCtx, call.info)
		case <-s.backgroundCtx.Done():
			return
		}
	}
}

func (s *state) GetIntercept(interceptID string) (*rpc.InterceptInfo, bool) {
	return s.intercepts.Load(interceptID)
}

func (s *state) WatchIntercepts(
	ctx context.Context,
	filter func(sessionID string, intercept *rpc.InterceptInfo) bool,
) <-chan watchable.Snapshot[*rpc.InterceptInfo] {
	if filter == nil {
		return s.intercepts.Subscribe(ctx)
	} else {
		return s.intercepts.SubscribeSubset(ctx, filter)
	}
}

func (s *state) Tunnel(ctx context.Context, stream tunnel.Stream) error {
	ctx, span := otel.Tracer("").Start(ctx, "state.Tunnel")
	defer span.End()
	stream.ID().SpanRecord(span)

	sessionID := stream.SessionID()
	s.mu.RLock()
	ss, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return status.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}

	var scm *SessionConsumptionMetrics
	switch sst := ss.(type) {
	case *agentSessionState:
		// If it's an agent, find the associated clientSessionState.
		if clientSessionID := sst.AwaitingBidiMapOwnerSessionID(stream); clientSessionID != "" {
			s.mu.RLock()
			as := s.sessions[clientSessionID] // get awaiting state
			s.mu.RUnlock()
			if as != nil { // if found
				if css, isClient := as.(*clientSessionState); isClient {
					scm = css.ConsumptionMetrics()
				}
			}
		}
	case *clientSessionState:
		scm = sst.ConsumptionMetrics()
	default:
	}

	bidiPipe, err := ss.OnConnect(ctx, stream, &s.tunnelCounter, scm)
	if err != nil {
		return err
	}

	if bidiPipe != nil {
		span.SetAttributes(attribute.Bool("peer-awaited", true))
		// A peer awaited this stream. Wait for the bidiPipe to finish
		<-bidiPipe.Done()
		return nil
	}

	// The session is either the telepresence client or a traffic-agent.
	//
	// A client will want to extend the tunnel to a dialer in an intercepted traffic-agent or, if no
	// intercept is active, to a dialer in that namespace.
	//
	// A traffic-agent must always extend the tunnel to the client that it is currently intercepted
	// by, and hence, start by sending the sessionID of that client on the tunnel.
	var peerSession SessionState
	if _, ok := ss.(*agentSessionState); ok {
		span.SetAttributes(attribute.String("session-type", "traffic-agent"))
		// traffic-agent, so obtain the desired client session
		m, err := stream.Receive(ctx)
		if err != nil {
			return status.Errorf(codes.FailedPrecondition, "failed to read first message from agent tunnel %q: %v", sessionID, err)
		}
		if m.Code() != tunnel.Session {
			return status.Errorf(codes.FailedPrecondition, "unable to read ClientSession from agent %q", sessionID)
		}
		peerID := tunnel.GetSession(m)
		span.SetAttributes(attribute.String("peer-id", peerID))
		s.mu.RLock()
		peerSession = s.sessions[peerID]
		s.mu.RUnlock()
	} else {
		span.SetAttributes(attribute.String("session-type", "userd"))
		peerSession, err = s.getAgentForDial(ctx, sessionID, stream.ID().Destination())
		if err != nil {
			return err
		}
	}

	var endPoint tunnel.Endpoint
	if peerSession != nil {
		var err error
		if endPoint, err = peerSession.EstablishBidiPipe(ctx, stream); err != nil {
			return err
		}
	} else {
		if css, isClient := ss.(*clientSessionState); isClient {
			scm = css.ConsumptionMetrics()
		}
		endPoint = tunnel.NewDialer(stream, func() {}, scm.FromClientBytes, scm.ToClientBytes)
		endPoint.Start(ctx)
	}
	<-endPoint.Done()
	return nil
}

func (s *state) getAgentForDial(ctx context.Context, clientSessionID string, podIP net.IP) (SessionState, error) {
	agentKey, err := s.getAgentIdForDial(ctx, clientSessionID, podIP)
	if err != nil || agentKey == "" {
		return nil, err
	}
	s.mu.RLock()
	agent := s.sessions[agentKey]
	s.mu.RUnlock()
	return agent, nil
}

func (s *state) getAgentIdForDial(ctx context.Context, clientSessionID string, podIP net.IP) (string, error) {
	// An agent with a podIO matching the given podIP has precedence
	agents := s.agents.LoadAllMatching(func(key string, ai *rpc.AgentInfo) bool {
		return podIP.Equal(iputil.Parse(ai.PodIp))
	})
	for agentID := range agents {
		dlog.Debugf(ctx, "selecting agent for dial based on podIP %s", podIP)
		return agentID, nil
	}

	client, ok := s.clients.Load(clientSessionID)
	if !ok {
		return "", status.Errorf(codes.NotFound, "session %q not found", clientSessionID)
	}
	env := managerutil.GetEnv(ctx)
	if env.ManagerNamespace == client.Namespace {
		// Traffic manager will do just fine
		dlog.Debugf(ctx, "selecting traffic-manager for dial, because it's in namespace %q", client.Namespace)
		return "", nil
	}

	// Any agent that is currently intercepted by the client has precedence.
	for agentID := range s.getAgentsInterceptedByClient(clientSessionID) {
		dlog.Debugf(ctx, "selecting intercepted agent %q for dial", agentID)
		return agentID, nil
	}

	// Any agent from the same namespace will do.
	for agentID := range s.getAgentsInNamespace(client.Namespace) {
		dlog.Debugf(ctx, "selecting agent %q for dial based on namespace %q", agentID, client.Namespace)
		return agentID, nil
	}

	// Best effort is to use the traffic-manager.
	// TODO: Add a pod that can dial from the correct namespace
	dlog.Debugf(ctx, "selecting traffic-manager for dial, even though it's not in namespace %q", client.Namespace)
	return "", nil
}

func (s *state) WatchDial(sessionID string) <-chan *rpc.DialRequest {
	s.mu.RLock()
	ss, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return ss.Dials()
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
