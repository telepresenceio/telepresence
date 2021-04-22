package state

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
)

const (
	loPort = 6000
	hiPort = 8000
)

type SessionState struct {
	Done       <-chan struct{}
	Cancel     context.CancelFunc
	LastMarked time.Time
}

// State is the total state of the Traffic Manager.  A zero State is invalid; you must call
// NewState.
type State struct {
	ctx context.Context

	mu sync.Mutex
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
	port         uint16
	intercepts   watchable.InterceptMap
	agents       watchable.AgentMap                   // info for agent sessions
	clients      watchable.ClientMap                  // info for client sessions
	sessions     map[string]*SessionState             // info for all sessions
	agentsByName map[string]map[string]*rpc.AgentInfo // indexed copy of `agents`
}

func NewState(ctx context.Context) *State {
	return &State{
		ctx:          ctx,
		port:         loPort - 1,
		sessions:     make(map[string]*SessionState),
		agentsByName: make(map[string]map[string]*rpc.AgentInfo),
	}
}

// Internal ////////////////////////////////////////////////////////////////////////////////////////

func (s *State) unlockedNextPort() uint16 {
	for attempts := 0; attempts < hiPort-loPort; attempts++ {
		// Bump the port number

		s.port++

		if s.port == hiPort {
			s.port = loPort
		}

		// Check whether the new port number is available

		used := false
		for _, intercept := range s.intercepts.LoadAll() {
			if intercept.ManagerPort == int32(s.port) {
				used = true
				break
			}
		}

		if !used {
			return s.port
		}
	}

	// Hmm. We've checked every possible port and they're all in use. This is
	// unlikely. Return 0 to indicate an error...
	return 0
}

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

	if len(agentSet) == 0 {
		errCode = rpc.InterceptDispositionType_NO_AGENT
		errMsg = fmt.Sprintf("No agent found for %q", intercept.Spec.Agent)
		return
	}

	agentList := make([]*rpc.AgentInfo, 0, len(agentSet))
	for _, agent := range agentSet {
		agentList = append(agentList, agent)
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

// Mark a session as being present at the indicated time.  Returns true if everything goes OK,
// returns false if the given session ID does not exist.
func (s *State) MarkSession(req *rpc.RemainRequest, now time.Time) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := req.Session.SessionId

	if sess, ok := s.sessions[sessionID]; ok {
		sess.LastMarked = now
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

// Remove a session from the set of present session IDs.
func (s *State) RemoveSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.unlockedRemoveSession(sessionID)
}

func (s *State) unlockedRemoveSession(sessionID string) {
	if sess, ok := s.sessions[sessionID]; ok {
		// kill the session
		sess.Cancel()

		// remove it from the agentsByName index (if nescessary)
		if agent, isAgent := s.agents.Load(sessionID); isAgent {
			delete(s.agentsByName[agent.Name], sessionID)
			if len(s.agentsByName[agent.Name]) == 0 {
				delete(s.agentsByName, agent.Name)
			}
		}

		// remove the session
		s.agents.Delete(sessionID)
		s.clients.Delete(sessionID)
		delete(s.sessions, sessionID)

		// GC any intercepts that relied on this session; prune any intercepts that
		//  1. Don't have a client session (intercept.ClientSession.SessionId)
		//  2. Don't have any agents (agent.Name == intercept.Spec.Agent)
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
			}
		}
	}
}

// ExpireSessions prunes any sessions that haven't had a MarkSession heartbeat since the given
// 'moment'.
func (s *State) ExpireSessions(moment time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, sess := range s.sessions {
		if sess.LastMarked.Before(moment) {
			s.unlockedRemoveSession(id)
		}
	}
}

// SessionDone returns a channel that is closed when the session with the given ID terminates.  If
// there is no such currently-live session, then an already-closed channel is returned.
func (s *State) SessionDone(id string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		ret := make(chan struct{})
		close(ret)
		return ret
	}
	return sess.Done
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
	s.sessions[sessionID] = &SessionState{
		Done:       ctx.Done(),
		Cancel:     cancel,
		LastMarked: now,
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

	if s.agentsByName[agent.Name] == nil {
		s.agentsByName[agent.Name] = make(map[string]*rpc.AgentInfo)
	}

	for interceptID, intercept := range s.intercepts.LoadAll() {
		// Check whether each intercept needs to either (1) be moved in to a NO_AGENT state
		// because this agent made things incosnsitent, or (2) be moved out of a NO_AGENT
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

	sessionID := uuid.New().String()
	if oldAgent, hasConflict := s.agents.LoadOrStore(sessionID, agent); hasConflict {
		panic(fmt.Errorf("duplicate id %q, existing %+v, new %+v", sessionID, oldAgent, agent))
	}
	s.agentsByName[agent.Name][sessionID] = agent
	ctx, cancel := context.WithCancel(s.ctx)
	s.sessions[sessionID] = &SessionState{
		Done:       ctx.Done(),
		Cancel:     cancel,
		LastMarked: now,
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

func (s *State) GetAgentsByName(name string) map[string]*rpc.AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	ret := make(map[string]*rpc.AgentInfo, len(s.agentsByName[name]))
	for k, v := range s.agentsByName[name] {
		ret[k] = proto.Clone(v).(*rpc.AgentInfo)
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

func (s *State) AddIntercept(sessionID, apiKey string, spec *rpc.InterceptSpec) (*rpc.InterceptInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cept := &rpc.InterceptInfo{
		Spec:        spec,
		ManagerPort: 0,
		Disposition: rpc.InterceptDispositionType_WAITING,
		Message:     "Waiting for Agent approval",
		Id:          sessionID + ":" + spec.Name,
		ClientSession: &rpc.SessionInfo{
			SessionId: sessionID,
		},
		ApiKey: apiKey,
	}

	// Wrap each potential-state-change in a
	//
	//     if cept.Disposition == rpc.InterceptDispositionType_WAITING { … }
	//
	// so that we don't need to worry about different state-changes stomping on eachother.

	if cept.Disposition == rpc.InterceptDispositionType_WAITING {
		cept.ManagerPort = int32(s.unlockedNextPort())
		if cept.ManagerPort == 0 {
			// Wow, there are no ports left!  That is... unlikely!
			cept.Disposition = rpc.InterceptDispositionType_NO_PORTS
			cept.Message = ""
		}
	}

	if cept.Disposition == rpc.InterceptDispositionType_WAITING {
		if errCode, errMsg := s.unlockedCheckAgentsForIntercept(cept); errCode != 0 {
			cept.Disposition = errCode
			cept.Message = errMsg
		}
	}

	if _, hasConflict := s.intercepts.LoadOrStore(cept.Id, cept); hasConflict {
		return nil, grpcStatus.Errorf(grpcCodes.AlreadyExists, "Intercept named %q already exists", spec.Name)
	}

	return cept, nil
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

		new := proto.Clone(cur).(*rpc.InterceptInfo)
		apply(new)

		swapped := s.intercepts.CompareAndSwap(new.Id, cur, new)
		if swapped {
			// Success!
			return new
		}
	}
}

func (s *State) RemoveIntercept(sessionID string, name string) bool {
	_, didDelete := s.intercepts.LoadAndDelete(sessionID + ":" + name)
	return didDelete
}

func (s *State) GetIntercept(interceptID string) *rpc.InterceptInfo {
	intercept, _ := s.intercepts.Load(interceptID)
	return intercept
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
