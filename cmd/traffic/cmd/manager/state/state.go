package state

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// State is the total state of the Traffic Manager.  A zero State is invalid; you must call
// NewState.
type State struct {
	ctx context.Context

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
}

func NewState(ctx context.Context) *State {
	loglevel := os.Getenv("LOG_LEVEL")
	return &State{
		ctx:             ctx,
		sessions:        make(map[string]SessionState),
		agentsByName:    make(map[string]map[string]*rpc.AgentInfo),
		cfgMapLocks:     make(map[string]*sync.Mutex),
		interceptStates: make(map[string]*interceptState),
		timedLogLevel:   log.NewTimedLevel(loglevel, log.SetLevel),
		llSubs:          newLoglevelSubscribers(),
	}
}

// Internal ////////////////////////////////////////////////////////////////////////////////////////

// unlockedCheckAgentsForIntercept (1) assumes that s.mu is already locked, and (2) checks the
// status of all agents that would be relevant to the given intercept spec, and returns whether the
// state of those agents would require transitioning to an error state.  If everything looks good,
// it returns the zero error code (InterceptDispositionType_UNSPECIFIED).
func (s *State) unlockedCheckAgentsForIntercept(intercept *rpc.InterceptInfo) (errCode rpc.InterceptDispositionType, errMsg string) {
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
func (s *State) MarkSession(req *rpc.RemainRequest, now time.Time) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := req.Session.SessionId

	if sess, ok := s.sessions[sessionID]; ok {
		sess.SetLastMarked(now)
		if req.ApiKey != "" {
			if client, ok := s.clients.Load(sessionID); ok {
				client.ApiKey = req.ApiKey
				s.clients.Store(sessionID, client)
			}
		}
		return true
	}

	return false
}

// RemoveSession removes a session from the set of present session IDs.
func (s *State) RemoveSession(ctx context.Context, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dlog.Debugf(ctx, "Session %s removed. Explicit removal", sessionID)

	s.unlockedRemoveSession(sessionID)
}

func (s *State) gcSessionIntercepts(sessionID string) {
	agent, isAgent := s.agents.Load(sessionID)

	// GC any intercepts that relied on this session; prune any intercepts that
	//  1. Don't have a client session (intercept.ClientSession.SessionId)
	//  2. Don't have any agents (agent.Name == intercept.Spec.Agent)
	// Alternatively, if the intercept is still live but has been switched over to a different agent, send it back to WAITING state
	for interceptID, intercept := range s.intercepts.LoadAll() {
		if intercept.ClientSession.SessionId == sessionID {
			// Client went away:
			// Delete it.
			s.unlockedRemoveIntercept(interceptID)
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
}

func (s *State) unlockedRemoveSession(sessionID string) {
	if sess, ok := s.sessions[sessionID]; ok {
		// kill the session
		defer sess.Cancel()

		s.gcSessionIntercepts(sessionID)

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
}

// ExpireSessions prunes any sessions that haven't had a MarkSession heartbeat since
// respective given 'moment'.
func (s *State) ExpireSessions(ctx context.Context, clientMoment, agentMoment time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, sess := range s.sessions {
		if _, ok := sess.(*clientSessionState); ok {
			if sess.LastMarked().Before(clientMoment) {
				dlog.Debugf(ctx, "Client Session %s removed. It has expired", id)
				s.unlockedRemoveSession(id)
			}
		} else {
			if sess.LastMarked().Before(agentMoment) {
				dlog.Debugf(ctx, "Agent Session %s removed. It has expired", id)
				s.unlockedRemoveSession(id)
			}
		}
	}
}

// SessionDone returns a channel that is closed when the session with the given ID terminates.  If
// there is no such currently-live session, then an already-closed channel is returned.
func (s *State) SessionDone(id string) (<-chan struct{}, error) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", id)
	}
	return sess.Done(), nil
}

// Sessions: Clients ///////////////////////////////////////////////////////////////////////////////

func (s *State) AddClient(client *rpc.ClientInfo, now time.Time) string {
	// Use non-sequential things (i.e., UUIDs, not just a counter) as the session ID, because
	// the session ID also exists in external systems (the client, SystemA), so it's confusing
	// (to both humans and computers) if the manager restarts and those existing session IDs
	// suddenly refer to different sessions.
	sessionID := uuid.New().String()
	return s.addClient(sessionID, client, now)
}

// addClient is like AddClient, but takes a sessionID, for testing purposes.
func (s *State) addClient(sessionID string, client *rpc.ClientInfo, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if oldClient, hasConflict := s.clients.LoadOrStore(sessionID, client); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", sessionID, oldClient, client))
	}
	s.sessions[sessionID] = newClientSessionState(s.ctx, now)
	return sessionID
}

func (s *State) GetClient(sessionID string) *rpc.ClientInfo {
	ret, _ := s.clients.Load(sessionID)
	return ret
}

func (s *State) GetAllClients() map[string]*rpc.ClientInfo {
	return s.clients.LoadAll()
}

func (s *State) CountAllClients() int {
	return s.clients.CountAll()
}

func (s *State) WatchClients(
	ctx context.Context,
	filter func(sessionID string, client *rpc.ClientInfo) bool,
) <-chan watchable.Snapshot[*rpc.ClientInfo] {
	if filter == nil {
		return s.clients.Subscribe(ctx)
	} else {
		return s.clients.SubscribeSubset(ctx, filter)
	}
}

// Sessions: Agents ////////////////////////////////////////////////////////////////////////////////

func (s *State) AddAgent(agent *rpc.AgentInfo, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionID := uuid.New().String()
	if oldAgent, hasConflict := s.agents.LoadOrStore(sessionID, agent); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", sessionID, oldAgent, agent))
	}

	if s.agentsByName[agent.Name] == nil {
		s.agentsByName[agent.Name] = make(map[string]*rpc.AgentInfo)
	}
	s.agentsByName[agent.Name][sessionID] = agent
	s.sessions[sessionID] = newAgentSessionState(s.ctx, now)

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

func (s *State) GetAgent(sessionID string) *rpc.AgentInfo {
	ret, _ := s.agents.Load(sessionID)
	return ret
}

func (s *State) GetAllAgents() map[string]*rpc.AgentInfo {
	return s.agents.LoadAll()
}

func (s *State) GetAgentsByName(name, namespace string) map[string]*rpc.AgentInfo {
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

func (s *State) WatchAgents(
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

func (s *State) AddIntercept(sessionID, clusterID, apiKey string, client *rpc.ClientInfo, spec *rpc.InterceptSpec) (*rpc.InterceptInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID].(*clientSessionState)
	if sess == nil || !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", sessionID)
	}

	interceptID := fmt.Sprintf("%s:%s", sessionID, spec.Name)
	installId := client.GetInstallId()
	cept := &rpc.InterceptInfo{
		Spec:        spec,
		Disposition: rpc.InterceptDispositionType_WAITING,
		Message:     "Waiting for Agent approval",
		Id:          interceptID,
		ClientSession: &rpc.SessionInfo{
			SessionId: sessionID,
			ClusterId: clusterID,
			InstallId: &installId,
		},
		ApiKey: apiKey,
	}

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

func (s *State) AddInterceptFinalizer(interceptID string, finalizer InterceptFinalizer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.interceptStates[interceptID]
	if !ok {
		return status.Errorf(codes.NotFound, "no such intercept %s", interceptID)
	}
	state.addFinalizer(finalizer)
	return nil
}

// getAgentsInterceptedByClient returns the session IDs for each agent that are currently
// intercepted by the client with the given client session ID.
func (s *State) getAgentsInterceptedByClient(clientSessionID string) []string {
	intercepts := s.intercepts.LoadAllMatching(func(_ string, ii *rpc.InterceptInfo) bool {
		return ii.ClientSession.SessionId == clientSessionID
	})
	if len(intercepts) == 0 {
		return nil
	}
	agents := s.agents.LoadAllMatching(func(_ string, ai *rpc.AgentInfo) bool {
		for _, ii := range intercepts {
			if ai.Name == ii.Spec.Agent && ai.Namespace == ii.Spec.Namespace {
				return true
			}
		}
		return false
	})
	if len(agents) == 0 {
		return nil
	}

	agentIDs := make([]string, len(agents)) // At least one agent per intercept
	i := 0
	for id := range agents {
		agentIDs[i] = id
		i++
	}
	return agentIDs
}

// UpdateIntercept applies a given mutator function to the stored intercept with interceptID;
// storing and returning the result.  If the given intercept does not exist, then the mutator
// function is not run, and nil is returned.
//
// This does not lock; but instead uses CAS and may therefore call the mutator function multiple
// times.  So: it is safe to perform blocking operations in your mutator function, but you must take
// care that it is safe to call your mutator function multiple times.
func (s *State) UpdateIntercept(interceptID string, apply func(*rpc.InterceptInfo)) *rpc.InterceptInfo {
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

func (s *State) RemoveIntercept(interceptID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unlockedRemoveIntercept(interceptID)
}

func (s *State) unlockedRemoveIntercept(interceptID string) bool {
	intercept, didDelete := s.intercepts.LoadAndDelete(interceptID)
	if state, ok := s.interceptStates[interceptID]; ok && didDelete {
		delete(s.interceptStates, interceptID)
		state.terminate(s.ctx, intercept)
	}

	return didDelete
}

func (s *State) GetIntercept(interceptID string) (*rpc.InterceptInfo, bool) {
	return s.intercepts.Load(interceptID)
}

func (s *State) WatchIntercepts(
	ctx context.Context,
	filter func(sessionID string, intercept *rpc.InterceptInfo) bool,
) <-chan watchable.Snapshot[*rpc.InterceptInfo] {
	if filter == nil {
		return s.intercepts.Subscribe(ctx)
	} else {
		return s.intercepts.SubscribeSubset(ctx, filter)
	}
}

func (s *State) Tunnel(ctx context.Context, stream tunnel.Stream) error {
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

	bidiPipe, err := ss.OnConnect(ctx, stream)
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
	// intercept is active, to a dialer here in the traffic-agent.
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
		peerSession = s.getRandomAgentSession(sessionID)
	}

	var endPoint tunnel.Endpoint
	if peerSession != nil {
		var err error
		if endPoint, err = peerSession.EstablishBidiPipe(ctx, stream); err != nil {
			return err
		}
	} else {
		endPoint = tunnel.NewDialer(stream, func() {})
		endPoint.Start(ctx)
	}
	<-endPoint.Done()
	return nil
}

func (s *State) getRandomAgentSession(clientSessionID string) (agent SessionState) {
	if agentIDs := s.getAgentsInterceptedByClient(clientSessionID); len(agentIDs) > 0 {
		s.mu.RLock()
		agent = s.sessions[agentIDs[0]]
		s.mu.RUnlock()
	}
	return
}

func (s *State) WatchDial(sessionID string) chan *rpc.DialRequest {
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
func (s *State) SetTempLogLevel(ctx context.Context, logLevelRequest *rpc.LogLevelRequest) {
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
func (s *State) InitialTempLogLevel() *rpc.LogLevelRequest {
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
func (s *State) WaitForTempLogLevel(stream rpc.Manager_WatchLogLevelServer) error {
	return s.llSubs.subscriberLoop(stream.Context(), stream)
}
