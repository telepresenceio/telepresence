package state

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/datawire/telepresence2/cmd/traffic/cmd/manager/internal/watchable"
	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
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

	counter int64

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

func (s *State) next() int64 {
	return atomic.AddInt64(&s.counter, 1)
}

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

// Sessions: common ////////////////////////////////////////////////////////////////////////////////

// Mark a session as being present at the indicated time.  Returns true if everything goes OK,
// returns false if the given session ID does not exist.
func (s *State) MarkSession(req *rpc.RemainRequest, now time.Time) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := req.Session.SessionId

	if sess, ok := s.sessions[sessionID]; ok {
		sess.LastMarked = now
		if req.BearerToken != "" {
			if client, ok := s.clients.Load(sessionID); ok {
				client.BearerToken = req.BearerToken
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
				// owner went away
				s.intercepts.Delete(interceptID)
			} else if len(s.agentsByName[intercept.Spec.Agent]) == 0 {
				// refcount went to 0
				s.intercepts.Delete(interceptID)
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
	sessionID := fmt.Sprintf("C%03d", s.next())
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
) <-chan map[string]*rpc.ClientInfo {
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

	agents := make([]*rpc.AgentInfo, 0, len(s.agentsByName[agent.Name]))
	for _, agent := range s.agentsByName[agent.Name] {
		agents = append(agents, agent)
	}
	agents = append(agents, agent)
	if len(agents) > 1 && !agentsAreCompatible(agents) {
		message := fmt.Sprintf("Agents for %q are not consistent", agent.Name)
		for interceptID, intercept := range s.intercepts.LoadAll() {
			if intercept.Spec.Agent == agent.Name {
				intercept.Disposition = rpc.InterceptDispositionType_NO_AGENT
				intercept.Message = message
				s.intercepts.Store(interceptID, intercept)
			}
		}
	}
	if len(agents) == 1 {
		s.agentsByName[agent.Name] = make(map[string]*rpc.AgentInfo)
	}

	sessionID := fmt.Sprintf("A%03d", s.next())
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
) <-chan map[string]*rpc.AgentInfo {
	if filter == nil {
		return s.agents.Subscribe(ctx)
	} else {
		return s.agents.SubscribeSubset(ctx, filter)
	}
}

// Intercepts //////////////////////////////////////////////////////////////////////////////////////

func (s *State) AddIntercept(sessionID string, spec *rpc.InterceptSpec) (*rpc.InterceptInfo, error) {
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
	}

	if len(s.agentsByName[cept.Spec.Agent]) == 0 {
		cept.Disposition = rpc.InterceptDispositionType_NO_AGENT
		cept.Message = fmt.Sprintf("No agent found for %q", spec.Agent)
	} else {
		agents := make([]*rpc.AgentInfo, 0, len(s.agentsByName[cept.Spec.Agent]))
		for _, agent := range s.agentsByName[cept.Spec.Agent] {
			agents = append(agents, agent)
		}
		if !agentsAreCompatible(agents) {
			cept.Disposition = rpc.InterceptDispositionType_NO_AGENT
			cept.Message = fmt.Sprintf("Agents for %q are not consistent", spec.Agent)
		} else if !agentHasMechanism(agents[0], spec.Mechanism) {
			cept.Disposition = rpc.InterceptDispositionType_NO_MECHANISM
			cept.Message = fmt.Sprintf("Agents for %q do not have mechanism %q", spec.Agent, spec.Mechanism)
		}
	}

	if _, hasConflict := s.intercepts.LoadOrStore(cept.Id, cept); hasConflict {
		return nil, grpcStatus.Errorf(grpcCodes.AlreadyExists, "Intercept named %q already exists", spec.Name)
	}

	return cept, nil
}

func (s *State) UpdateIntercept(intercept *rpc.InterceptInfo) {
	s.intercepts.Store(intercept.Id, intercept)
}

func (s *State) RemoveIntercept(sessionID string, name string) bool {
	_, didDelete := s.intercepts.LoadAndDelete(sessionID + ":" + name)
	return didDelete
}

func (s *State) WatchIntercepts(
	ctx context.Context,
	filter func(sessionID string, intercept *rpc.InterceptInfo) bool,
) <-chan map[string]*rpc.InterceptInfo {
	if filter == nil {
		return s.intercepts.Subscribe(ctx)
	} else {
		return s.intercepts.SubscribeSubset(ctx, filter)
	}
}

func (s *State) ReviewIntercept(sessionID string, ceptID string, disposition rpc.InterceptDispositionType, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents.Load(sessionID)
	if !ok {
		return false
	}

	intercept, ok := s.intercepts.Load(ceptID)
	if !ok {
		return false
	}

	// Sanity check: The reviewing agent must be an agent for the intercept.
	if intercept.Spec.Agent != agent.Name {
		return false
	}

	// Only update intercepts in the waiting state.  Agents race to review an intercept, but we
	// expect they will always compatible answers.
	if intercept.Disposition == rpc.InterceptDispositionType_WAITING {
		intercept.Disposition = disposition
		intercept.Message = message

		// An intercept going active needs to be allocated a free port
		if disposition == rpc.InterceptDispositionType_ACTIVE {
			intercept.ManagerPort = int32(s.unlockedNextPort())
			if intercept.ManagerPort == 0 {
				// Wow, there are no ports left!  That is... unlikely!
				intercept.Disposition = rpc.InterceptDispositionType_NO_PORTS
			}
		}

		// Save the result.
		s.intercepts.Store(ceptID, intercept)
	}

	return true
}
