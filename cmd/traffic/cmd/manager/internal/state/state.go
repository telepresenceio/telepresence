package state

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type SessionState interface {
	Cancel()
	Done() <-chan struct{}
	LastMarked() time.Time
	SetLastMarked(lastMarked time.Time)
}

type sessionState struct {
	done       <-chan struct{}
	cancel     context.CancelFunc
	lastMarked time.Time
}

func (ss *sessionState) Cancel() {
	ss.cancel()
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

type agentTunnel struct {
	name      string
	namespace string
	tunnel    rpc.Manager_AgentTunnelServer
}

type clientSessionState struct {
	sessionState
	name               string
	pool               *connpool.Pool
	ClientTunnelServer rpc.Manager_ClientTunnelServer
	agentTunnelsMu     sync.Mutex
	agentTunnels       map[string]*agentTunnel
}

func (cs *clientSessionState) addAgentTunnel(agentSessionID, name, namespace string, tunnel rpc.Manager_AgentTunnelServer) {
	cs.agentTunnelsMu.Lock()
	cs.agentTunnels[agentSessionID] = &agentTunnel{
		name:      name,
		namespace: namespace,
		tunnel:    tunnel,
	}
	cs.agentTunnelsMu.Unlock()
}

func (cs *clientSessionState) deleteAgentTunnel(agentSessionID string) {
	cs.agentTunnelsMu.Lock()
	delete(cs.agentTunnels, agentSessionID)
	cs.agentTunnelsMu.Unlock()
}

// getRandomAgentTunnel will return the tunnel of an intercepted agent provided all intercepted
// agents live in the same namespace. The method will return nil if the client currently has no
// intercepts or if it has several intercepts that span more than one namespace.
func (cs *clientSessionState) getRandomAgentTunnel() (tunnel *agentTunnel) {
	cs.agentTunnelsMu.Lock()
	defer cs.agentTunnelsMu.Unlock()
	prevNs := ""
	for _, agentTunnel := range cs.agentTunnels {
		tunnel = agentTunnel
		if prevNs == "" {
			prevNs = agentTunnel.namespace
		} else if prevNs != agentTunnel.name {
			return nil
		}
	}
	// return the first tunnel found. In case there are several, the map will
	// randomize which one
	return tunnel
}

// getInterceptedAgents returns the session ID of each agent currently intercepted
// by this client
func (cs *clientSessionState) getInterceptedAgents() []string {
	cs.agentTunnelsMu.Lock()
	agentSessionIDs := make([]string, len(cs.agentTunnels))
	i := 0
	for agentSession := range cs.agentTunnels {
		agentSessionIDs[i] = agentSession
		i++
	}
	cs.agentTunnelsMu.Unlock()
	return agentSessionIDs
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
	//  7. `interceptAPIKeys` need to be created and updated in-sync with `intercepts` (but not deleted
	//      in-sync with `intercepts`; that happens separately, in `RemoveInterceptAPIKey())
	intercepts       watchable.InterceptMap
	agents           watchable.AgentMap                   // info for agent sessions
	clients          watchable.ClientMap                  // info for client sessions
	sessions         map[string]SessionState              // info for all sessions
	interceptAPIKeys map[string]string                    // InterceptIDs mapped to the APIKey used to create them
	listeners        map[string]connpool.Handler          // listeners for all intercepts
	agentsByName     map[string]map[string]*rpc.AgentInfo // indexed copy of `agents`
}

func NewState(ctx context.Context) *State {
	return &State{
		ctx:              ctx,
		sessions:         make(map[string]SessionState),
		interceptAPIKeys: make(map[string]string),
		agentsByName:     make(map[string]map[string]*rpc.AgentInfo),
		listeners:        make(map[string]connpool.Handler),
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
		if sess.LastMarked().Before(moment) {
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
	return sess.Done()
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
		},
		name:         client.Name,
		pool:         connpool.NewPool(),
		agentTunnels: make(map[string]*agentTunnel),
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
	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *State) AddIntercept(sessionID, apiKey string, spec *rpc.InterceptSpec) (*rpc.InterceptInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	interceptID := fmt.Sprintf("%s:%s", sessionID, spec.Name)
	s.interceptAPIKeys[interceptID] = apiKey
	cept := &rpc.InterceptInfo{
		Spec:        spec,
		Disposition: rpc.InterceptDispositionType_WAITING,
		Message:     "Waiting for Agent approval",
		Id:          interceptID,
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

// getAgentsInterceptedByClient returns the session IDs for each agent that is currently
// intercepted by the client with the given client session ID.
func (s *State) getAgentsInterceptedByClient(clientSessionID string) ([]string, error) {
	s.mu.Lock()
	ss := s.sessions[clientSessionID]
	s.mu.Unlock()
	if cs, ok := ss.(*clientSessionState); ok {
		return cs.getInterceptedAgents(), nil
	}
	return nil, status.Errorf(codes.NotFound, "Client session %q not found", clientSessionID)
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

func (s *State) RemoveIntercept(interceptID string) bool {
	_, didDelete := s.intercepts.LoadAndDelete(interceptID)
	if didDelete {
		s.mu.Lock()
		l, ok := s.listeners[interceptID]
		if ok {
			delete(s.listeners, interceptID)
		}
		s.mu.Unlock()
		if ok {
			l.Close(s.ctx)
		}
	}
	return didDelete
}

// GetInterceptAPIKey returns the first non-empty apiKey associated with an intercept IDs.
// We use this fuction as a last resort if we need to garbage collect intercepts when
// there are no active sessions.
func (s *State) GetInterceptAPIKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if _, ok := s.interceptAPIKeys[interceptID]; !ok {
		return false
	}
	delete(s.interceptAPIKeys, interceptID)
	s.mu.Unlock()
	return true
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

func (s *State) ClientTunnel(ctx context.Context, server rpc.Manager_ClientTunnelServer) error {
	sessionID := managerutil.GetSessionID(ctx)
	s.mu.Lock()
	ss := s.sessions[sessionID]
	s.mu.Unlock()
	cs, ok := ss.(*clientSessionState)
	if !ok {
		return status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}
	dlog.Debug(ctx, "Established TCP tunnel")
	pool := cs.pool // must have one pool per client
	cs.ClientTunnelServer = server
	defer pool.CloseAll(ctx)
	closing := int32(0)
	msgCh, errCh := connpool.NewStream(server).ReadLoop(ctx, &closing)
	for {
		select {
		case <-ctx.Done():
			atomic.StoreInt32(&closing, 2)
			return nil
		case err := <-errCh:
			return err
		case msg := <-msgCh:
			if msg == nil {
				return nil
			}

			id := msg.ID()
			// Retrieve the connection that is tracked for the given id. Create a new one if necessary
			h, _, err := pool.Get(ctx, id, func(ctx context.Context, release func()) (connpool.Handler, error) {
				switch id.Protocol() {
				case ipproto.TCP, ipproto.UDP:
					if agentTunnel := cs.getRandomAgentTunnel(); agentTunnel != nil {
						// Dispatch directly to agent and let the dial happen there
						dlog.Debugf(ctx, "|| FRWD %s forwarding client connection to agent %s.%s", id, agentTunnel.name, agentTunnel.namespace)
						return newConnForward(release, agentTunnel.tunnel), nil
					}
					return connpool.NewDialer(id, cs.ClientTunnelServer, release), nil
				default:
					return nil, fmt.Errorf("unhadled L4 protocol: %d", id.Protocol())
				}
			})
			if err != nil {
				return fmt.Errorf("failed to get connection handler: %w", err)
			}
			h.HandleMessage(ctx, msg)
		}
	}
}

type connForward struct {
	release  func()
	toStream connpool.TunnelStream
}

func newConnForward(release func(), toStream connpool.TunnelStream) *connForward {
	return &connForward{release: release, toStream: toStream}
}

func (cf *connForward) Close(_ context.Context) {
	cf.release()
}

func (cf *connForward) HandleMessage(ctx context.Context, cm connpool.Message) {
	dlog.Debugf(ctx, ">> FRWD %s to agent", cm.ID())
	if err := cf.toStream.Send(cm.TunnelMessage()); err != nil {
		dlog.Errorf(ctx, "!! FRWD %s to agent, send failed: %v", cm.ID(), err)
	}
}

func (cf *connForward) Start(_ context.Context) {
}

func (s *State) AgentTunnel(ctx context.Context, clientSessionInfo *rpc.SessionInfo, server rpc.Manager_AgentTunnelServer) error {
	agentSessionID := managerutil.GetSessionID(ctx)
	as, cs, err := func() (*agentSessionState, *clientSessionState, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		ss := s.sessions[agentSessionID]
		as, ok := ss.(*agentSessionState)
		if !ok {
			return nil, nil, status.Errorf(codes.NotFound, "agent session %q not found", agentSessionID)
		}
		clientSessionID := clientSessionInfo.GetSessionId()
		ss = s.sessions[clientSessionID]
		cs, ok := ss.(*clientSessionState)
		if !ok {
			return nil, nil, status.Errorf(codes.NotFound, "client session %q not found", clientSessionID)
		}
		return as, cs, nil
	}()
	if err != nil {
		return err
	}
	dlog.Debugf(ctx, "Established TCP tunnel from agent %s to client %s", as.agent.Name, cs.name)

	// During intercept, all requests that are made to this pool, are forwarded to the intercepted
	// agent(s)
	cs.addAgentTunnel(agentSessionID, as.agent.Name, as.agent.Namespace, server)
	defer cs.deleteAgentTunnel(agentSessionID)

	pool := cs.pool
	stream := connpool.NewStream(server)
	closing := int32(0)
	msgCh, errCh := stream.ReadLoop(ctx, &closing)
	for {
		select {
		case <-ctx.Done():
			atomic.StoreInt32(&closing, 2)
			return nil
		case err := <-errCh:
			return err
		case msg := <-msgCh:
			if msg == nil {
				return nil
			}
			id := msg.ID()
			conn, found, err := pool.Get(ctx, id, func(ctx context.Context, release func()) (connpool.Handler, error) {
				return newConnForward(release, server), nil
			})
			if found {
				if _, ok := conn.(*connForward); !ok {
					// lingering, non intercepted outbound connection to agent. Close it and create a new forward
					conn.Close(ctx)
					_, _, err = pool.Get(ctx, id, func(ctx context.Context, release func()) (connpool.Handler, error) {
						return newConnForward(release, server), nil
					})
				}
			}
			if err != nil {
				dlog.Error(ctx, err)
				return status.Error(codes.Internal, err.Error())
			}
			dlog.Debugf(ctx, ">> FRWD %s to client", id)
			if err = cs.ClientTunnelServer.Send(msg.TunnelMessage()); err != nil {
				dlog.Errorf(ctx, "Send to client failed: %v", err)
				return err
			}
		}
	}
}

// AgentsLookup will send the given request to all agents currently intercepted by the client identified with
// the clientSessionID, it will then wait for results to arrive, collect those results, and return them as a
// unique and sorted slice.
func (s *State) AgentsLookup(ctx context.Context, clientSessionID string, request *rpc.LookupHostRequest) (iputil.IPs, error) {
	iceptAgentIDs, err := s.getAgentsInterceptedByClient(clientSessionID)
	if err != nil {
		return nil, err
	}

	ips := iputil.IPs{}
	iceptCount := len(iceptAgentIDs)
	if iceptCount == 0 {
		return ips, nil
	}

	rsMu := sync.Mutex{} // prevent concurrent updates of the ips slice
	agentTimeout, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	responseCount := 0
	defer cancel()

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
				responseCount++
				rc := responseCount
				for _, ip := range rs.Ips {
					ips = append(ips, ip)
				}
				rsMu.Unlock()
				if rc == iceptCount {
					// all agents have responded
					return
				}
			}
		}(agentSessionID)
	}
	wg.Wait() // wait for timeout or that all agents have responded
	return ips.UniqueSorted(), nil
}

// PostLookupResponse receives lookup responses from an agent and places them in the channel
// that corresponds to the lookup request
func (s *State) PostLookupResponse(response *rpc.LookupHostAgentResponse) {
	responseID := response.Request.Session.SessionId + ":" + response.Request.Host
	var rch chan<- *rpc.LookupHostResponse
	s.mu.Lock()
	if as, ok := s.sessions[response.Session.SessionId].(*agentSessionState); ok {
		rch = as.lookupResponses[responseID]
	}
	s.mu.Unlock()
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
	s.mu.Lock()
	ss, ok := s.sessions[agentSessionID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return ss.(*agentSessionState).lookups
}
