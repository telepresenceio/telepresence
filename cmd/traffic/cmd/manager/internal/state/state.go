package state

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type SessionState interface {
	Cancel()
	Done() <-chan struct{}
	LastMarked() time.Time
	SetLastMarked(lastMarked time.Time)
	Dials() <-chan *rpc.DialRequest
	EstablishBidiPipe(context.Context, tunnel.Stream) (tunnel.Endpoint, error)
	OnConnect(context.Context, tunnel.Stream) (tunnel.Endpoint, error)
}

type awaitingBidiPipe struct {
	stream     tunnel.Stream
	bidiPipeCh chan tunnel.Endpoint
}

type sessionState struct {
	sync.Mutex
	done                <-chan struct{}
	cancel              context.CancelFunc
	lastMarked          time.Time
	awaitingBidiPipeMap map[tunnel.ConnID]*awaitingBidiPipe
	dials               chan *rpc.DialRequest
}

// EstablishBidiPipe registers the given stream as waiting for a matching stream to arrive in a call
// to Tunnel, sends a DialRequest to the owner of this sessionState, and then waits. When the call
// arrives, a BidiPipe connecting the two streams is returned.
func (ss *sessionState) EstablishBidiPipe(ctx context.Context, stream tunnel.Stream) (tunnel.Endpoint, error) {
	// Dispatch directly to agent and let the dial happen there
	bidiPipeCh := make(chan tunnel.Endpoint)
	id := stream.ID()
	abp := &awaitingBidiPipe{stream: stream, bidiPipeCh: bidiPipeCh}

	ss.Lock()
	if ss.awaitingBidiPipeMap == nil {
		ss.awaitingBidiPipeMap = map[tunnel.ConnID]*awaitingBidiPipe{id: abp}
	} else {
		ss.awaitingBidiPipeMap[id] = abp
	}
	ss.Unlock()

	// Send dial request to the client/agent
	select {
	case <-ss.done:
		return nil, status.Error(codes.Canceled, "session cancelled")
	case ss.dials <- &rpc.DialRequest{ConnId: []byte(id), RoundtripLatency: int64(stream.RoundtripLatency()), DialTimeout: int64(stream.DialTimeout())}:
	}

	// Wait for the client/agent to connect. Allow extra time for the call
	ctx, cancel := context.WithTimeout(ctx, stream.DialTimeout()+stream.RoundtripLatency())
	defer cancel()
	select {
	case <-ctx.Done():
		return nil, status.Error(codes.DeadlineExceeded, "timeout while establishing bidipipe")
	case <-ss.done:
		return nil, status.Error(codes.Canceled, "session cancelled")
	case bidi := <-bidiPipeCh:
		return bidi, nil
	}
}

// OnConnect checks if a stream is waiting for the given stream to arrive in order to create a BidiPipe.
// If that's the case, the BidiPipe is created, started, and returned by both this method and the EstablishBidiPipe
// method that registered the waiting stream. Otherwise, this method returns nil.
func (ss *sessionState) OnConnect(ctx context.Context, stream tunnel.Stream) (tunnel.Endpoint, error) {
	id := stream.ID()
	ss.Lock()
	abp, ok := ss.awaitingBidiPipeMap[id]
	if ok {
		delete(ss.awaitingBidiPipeMap, id)
	}
	ss.Unlock()

	if !ok {
		return nil, nil
	}
	dlog.Debugf(ctx, "   FWD %s, connect session %s with %s", id, abp.stream.SessionID(), stream.SessionID())
	bidiPipe := tunnel.NewBidiPipe(abp.stream, stream)
	bidiPipe.Start(ctx)

	defer close(abp.bidiPipeCh)
	select {
	case <-ss.done:
		return nil, status.Error(codes.Canceled, "session cancelled")
	case abp.bidiPipeCh <- bidiPipe:
		return bidiPipe, nil
	}
}

func (ss *sessionState) Cancel() {
	ss.cancel()
	close(ss.dials)
}

func (ss *sessionState) Dials() <-chan *rpc.DialRequest {
	return ss.dials
}

func (ss *sessionState) Done() <-chan struct{} {
	return ss.done
}

func (ss *sessionState) LastMarked() time.Time {
	return ss.lastMarked
}

func (ss *sessionState) SetLastMarked(lastMarked time.Time) {
	ss.lastMarked = lastMarked
}

type clientSessionState struct {
	sessionState
	name string
	pool *tunnel.Pool
}

type agentSessionState struct {
	sessionState
	agent           *rpc.AgentInfo
	lookups         chan *rpc.LookupHostRequest
	lookupResponses map[string]chan *rpc.LookupHostResponse
}

func (ss *agentSessionState) Cancel() {
	close(ss.lookups)
	for _, lr := range ss.lookupResponses {
		close(lr)
	}
	ss.sessionState.Cancel()
}

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
	//  7. `interceptAPIKeys` need to be created and updated in-sync with `intercepts` (but not deleted
	//      in-sync with `intercepts`; that happens separately, in `RemoveInterceptAPIKey())
	//  8. `cfgMapLocks` access must be concurrency protected
	//  9. `cachedAgentImage` access must be concurrency protected
	intercepts       watchable.InterceptMap
	agents           watchable.AgentMap                   // info for agent sessions
	clients          watchable.ClientMap                  // info for client sessions
	sessions         map[string]SessionState              // info for all sessions
	interceptAPIKeys map[string]string                    // InterceptIDs mapped to the APIKey used to create them
	agentsByName     map[string]map[string]*rpc.AgentInfo // indexed copy of `agents`
	timedLogLevel    log.TimedLevel
	llSubs           *loglevelSubscribers
	cfgMapLocks      map[string]*sync.Mutex
	cachedAgentImage string
}

func NewState(ctx context.Context) *State {
	loglevel := os.Getenv("LOG_LEVEL")
	return &State{
		ctx:              ctx,
		sessions:         make(map[string]SessionState),
		interceptAPIKeys: make(map[string]string),
		agentsByName:     make(map[string]map[string]*rpc.AgentInfo),
		cfgMapLocks:      make(map[string]*sync.Mutex),
		timedLogLevel:    log.NewTimedLevel(loglevel, log.SetLevel),
		llSubs:           newLoglevelSubscribers(),
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

func (s *State) unlockedRemoveSession(sessionID string) {
	if sess, ok := s.sessions[sessionID]; ok {
		// kill the session
		sess.Cancel()

		// remove it from the agentsByName index (if nescessary)
		agent, isAgent := s.agents.Load(sessionID)
		if isAgent {
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

		// GC any intercepts that relied on this session; prune any intercepts that
		//  1. Don't have a client session (intercept.ClientSession.SessionId)
		//  2. Don't have any agents (agent.Name == intercept.Spec.Agent)
		// Alternatively, if the intercept is still live but has been switched over to a different agent, send it back to WAITING state
		for interceptID, intercept := range s.intercepts.LoadAll() {
			if intercept.ClientSession.SessionId == sessionID {
				// Client went away:
				// Delete it.
				s.intercepts.Delete(interceptID)
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

// addClient is like AddClient, but takes a sessionID, for testing purposes
func (s *State) addClient(sessionID string, client *rpc.ClientInfo, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if oldClient, hasConflict := s.clients.LoadOrStore(sessionID, client); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", sessionID, oldClient, client))
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.sessions[sessionID] = &clientSessionState{
		sessionState: sessionState{
			done:       ctx.Done(),
			cancel:     cancel,
			lastMarked: now,
			dials:      make(chan *rpc.DialRequest),
		},
		name: client.Name,
		pool: tunnel.NewPool(),
	}
	return sessionID
}

func (s *State) GetClient(sessionID string) *rpc.ClientInfo {
	ret, _ := s.clients.Load(sessionID)
	return ret
}

func (s *State) GetAllClients() map[string]*rpc.ClientInfo {
	return s.clients.LoadAll()
}

func (s *State) WatchClients(
	ctx context.Context,
	filter func(sessionID string, client *rpc.ClientInfo) bool,
) <-chan watchable.ClientMapSnapshot {
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

	ctx, cancel := context.WithCancel(s.ctx)
	s.sessions[sessionID] = &agentSessionState{
		sessionState: sessionState{
			done:       ctx.Done(),
			cancel:     cancel,
			lastMarked: now,
			dials:      make(chan *rpc.DialRequest),
		},
		lookups:         make(chan *rpc.LookupHostRequest),
		lookupResponses: make(map[string]chan *rpc.LookupHostResponse),
		agent:           agent,
	}

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
) <-chan watchable.AgentMapSnapshot {
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

	interceptID := fmt.Sprintf("%s:%s", sessionID, spec.Name)
	s.interceptAPIKeys[interceptID] = apiKey
	clonedClient, ok := proto.Clone(client).(*rpc.ClientInfo)
	if !ok {
		return nil, fmt.Errorf("unexpected error trying to create intercept: failed to clone ClientInfo proto")
	}
	cept := &rpc.InterceptInfo{
		Spec:        spec,
		Disposition: rpc.InterceptDispositionType_WAITING,
		Message:     "Waiting for Agent approval",
		Id:          interceptID,
		ClientSession: &rpc.SessionInfo{
			SessionId: sessionID,
			ClusterId: clusterID,
			Session:   &rpc.SessionInfo_Client{Client: clonedClient},
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

	return cept, nil
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
	_, didDelete := s.intercepts.LoadAndDelete(interceptID)
	return didDelete
}

// GetInterceptAPIKey returns the first non-empty apiKey associated with an intercept IDs.
// We use this fuction as a last resort if we need to garbage collect intercepts when
// there are no active sessions.
func (s *State) GetInterceptAPIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range s.interceptAPIKeys {
		if key != "" {
			return key
		}
	}
	return ""
}

// RemoveInterceptAPIKey removes the associated APIKey for an Intercept ID
// Only call on an intercept that has been deleted.
func (s *State) RemoveInterceptAPIKey(interceptID string) bool {
	// If the APIKey isn't present, then we return false since we didn't remove
	// anything since no APIKey was associated with that intercept.
	s.mu.Lock()
	_, ok := s.interceptAPIKeys[interceptID]
	if ok {
		delete(s.interceptAPIKeys, interceptID)
	}
	s.mu.Unlock()
	return ok
}

func (s *State) GetIntercept(interceptID string) (*rpc.InterceptInfo, bool) {
	return s.intercepts.Load(interceptID)
}

func (s *State) WatchIntercepts(
	ctx context.Context,
	filter func(sessionID string, intercept *rpc.InterceptInfo) bool,
) <-chan watchable.InterceptMapSnapshot {
	if filter == nil {
		return s.intercepts.Subscribe(ctx)
	} else {
		return s.intercepts.SubscribeSubset(ctx, filter)
	}
}

func (s *State) Tunnel(ctx context.Context, stream tunnel.Stream) error {
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
		// traffic-agent, so obtain the desired client session
		m, err := stream.Receive(ctx)
		if err != nil {
			return status.Errorf(codes.FailedPrecondition, "failed to read first message from agent tunnel %q: %v", sessionID, err)
		}
		if m.Code() != tunnel.Session {
			return status.Errorf(codes.FailedPrecondition, "unable to read ClientSession from agent %q", sessionID)
		}
		peerID := tunnel.GetSession(m)
		s.mu.RLock()
		peerSession = s.sessions[peerID]
		s.mu.RUnlock()
	} else {
		peerSession = s.getRandomAgentSession(sessionID)
	}

	var endPoint tunnel.Endpoint
	if peerSession != nil {
		var err error
		if endPoint, err = peerSession.EstablishBidiPipe(ctx, stream); err != nil {
			return err
		}
	} else {
		endPoint = tunnel.NewDialer(stream)
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

func (s *State) WatchDial(sessionID string) <-chan *rpc.DialRequest {
	s.mu.RLock()
	ss, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return ss.Dials()
}

// AgentsLookup will send the given request to all agents currently intercepted by the client identified with
// the clientSessionID, it will then wait for results to arrive, collect those results, and return them as a
// unique and sorted slice together with a count of how many agents that replied.
func (s *State) AgentsLookup(ctx context.Context, clientSessionID string, request *rpc.LookupHostRequest) (iputil.IPs, int, error) {
	iceptAgentIDs := s.getAgentsInterceptedByClient(clientSessionID)
	ips := iputil.IPs{}
	iceptCount := len(iceptAgentIDs)
	if iceptCount == 0 {
		return ips, 0, nil
	}

	rsMu := sync.Mutex{} // prevent concurrent updates of the ips slice
	agentTimeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	count := 0
	wg := sync.WaitGroup{}
	wg.Add(iceptCount)
	for _, agentSessionID := range iceptAgentIDs {
		go func(agentSessionID string) {
			defer func() {
				s.endHostLookup(agentSessionID, request)
				wg.Done()
			}()

			rsCh := s.startHostLookup(agentSessionID, request)
			if rsCh == nil {
				return
			}
			select {
			case <-agentTimeout.Done():
				return
			case rs := <-rsCh:
				if rs == nil {
					// Channel closed
					return
				}
				rsMu.Lock()
				count++
				for _, ip := range rs.Ips {
					ips = append(ips, ip)
				}
				rsMu.Unlock()
			}
		}(agentSessionID)
	}
	wg.Wait() // wait for timeout or that all agents have responded
	return ips.UniqueSorted(), count, nil
}

// PostLookupResponse receives lookup responses from an agent and places them in the channel
// that corresponds to the lookup request
func (s *State) PostLookupResponse(response *rpc.LookupHostAgentResponse) {
	responseID := response.Request.Session.SessionId + ":" + response.Request.Host
	var rch chan<- *rpc.LookupHostResponse
	s.mu.RLock()
	if as, ok := s.sessions[response.Session.SessionId].(*agentSessionState); ok {
		rch = as.lookupResponses[responseID]
	}
	s.mu.RUnlock()
	if rch != nil {
		rch <- response.Response
	}
}

func (s *State) startHostLookup(agentSessionID string, request *rpc.LookupHostRequest) <-chan *rpc.LookupHostResponse {
	responseID := request.Session.SessionId + ":" + request.Host
	var (
		rch chan *rpc.LookupHostResponse
		as  *agentSessionState
		ok  bool
	)
	s.mu.Lock()
	if as, ok = s.sessions[agentSessionID].(*agentSessionState); ok {
		if rch, ok = as.lookupResponses[responseID]; !ok {
			rch = make(chan *rpc.LookupHostResponse)
			as.lookupResponses[responseID] = rch
		}
	}
	s.mu.Unlock()
	if as != nil {
		// the as.lookups channel may be closed at this point, so guard for panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					close(rch)
				}
			}()
			as.lookups <- request
		}()
	}
	return rch
}

func (s *State) endHostLookup(agentSessionID string, request *rpc.LookupHostRequest) {
	responseID := request.Session.SessionId + ":" + request.Host
	s.mu.Lock()
	if as, ok := s.sessions[agentSessionID].(*agentSessionState); ok {
		if rch, ok := as.lookupResponses[responseID]; ok {
			delete(as.lookupResponses, responseID)
			close(rch)
		}
	}
	s.mu.Unlock()
}

func (s *State) WatchLookupHost(agentSessionID string) <-chan *rpc.LookupHostRequest {
	s.mu.RLock()
	ss, ok := s.sessions[agentSessionID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return ss.(*agentSessionState).lookups
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
