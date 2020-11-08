package manager

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/datawire/telepresence2/pkg/rpc"
)

// Clock is the mechanism used by the Manager state to get the current time.
type Clock interface {
	Now() time.Time
}

type interceptEntry struct {
	clientSessionID string
	index           int
	intercept       *rpc.InterceptInfo
}

// State is the total state of the Traffic Manager.
type State struct {
	mu      sync.Mutex
	counter int
	clock   Clock

	agentWatches     *Watches
	interceptWatches *Watches

	intercepts map[string]*interceptEntry

	sessions *Presence
}

func NewState(ctx context.Context, clock Clock) *State {
	return &State{
		clock:            clock,
		agentWatches:     NewWatches(),
		interceptWatches: NewWatches(),
		intercepts:       make(map[string]*interceptEntry),
		sessions:         NewPresence(ctx, nil),
	}
}

// Internal

func (s *State) next() int {
	s.counter++
	return s.counter
}

/*
// reconcile updates the state of the Manager based on the absence or
// presence/status of clients and agents in the system. After removing clients
// and agents that are no longer present, Reconcile updates the disposition of
// affected intercepts. It returns the set of intercepts that have changed so
// that intercept watch events can be fired for the associated clients and
// agents. It also returns a boolean to indicate whether the set of Agents has
// changed so that agent watch events can be fired for all clients.
func (s *State) reconcile() ([]*rpc.InterceptInfo, bool) {
	// Remove clients that are no longer present. Removing clients can trigger
	// an intercept watch event below.
	for sessionID := range s.clients {
		if !s.presence.IsPresent(sessionID) {
			delete(s.clients, sessionID)
		}
	}

	// Remove agents that are no longer present. Removing agents can trigger an
	// agent watch event here and can trigger an intercept watch event below.
	agentsUpdated := false
	agentsByName := make(map[string][]*rpc.AgentInfo)
	for sessionID, agent := range s.agents {
		if s.presence.IsPresent(sessionID) {
			agentsByName[agent.Name] = append(agentsByName[agent.Name], agent)
			continue
		}

		delete(s.agents, sessionID)
		agentsUpdated = true
	}

	// Reconcile Intercepts
	interceptsUpdated := make([]*rpc.InterceptInfo, 0, 10)
	for key, iEntry := range s.intercepts {
		// Make sure the client is present. Otherwise remove the intercept and
		// update the associated agents.
		if _, ok := s.clients[iEntry.clientSessionID]; !ok {
			iEntry.intercept.Disposition = rpc.InterceptInfo_NO_CLIENT
			interceptsUpdated = append(interceptsUpdated, iEntry.intercept)
			delete(s.intercepts, key)
			continue
		}

		// Make sure at least one agent is present and the agents are compatible
		// with one another (avoid e.g., misconfigured agents or agents going
		// through a rolling update). Otherwise mark the intercept as failed and
		// update the associated client. Let the client remove the intercept.
		agents := agentsByName[iEntry.intercept.Spec.Agent]
		if len(agents) == 0 || !agentsAreCompatible(agents) {
			iEntry.intercept.Disposition = rpc.InterceptInfo_NO_AGENT
			interceptsUpdated = append(interceptsUpdated, iEntry.intercept)
			continue
		}

		// Make sure the agents offer the specified intercept mechanism.
		// Otherwise mark the intercept as failed and update the associated
		// client. Let the client figure out what to do about it.
		mechanismMatched := false
		for _, mechanism := range agents[0].Mechanisms {
			if mechanism.Name == iEntry.intercept.Spec.Mechanism {
				mechanismMatched = true
				break
			}
		}
		if !mechanismMatched {
			iEntry.intercept.Disposition = rpc.InterceptInfo_NO_MECHANISM
			interceptsUpdated = append(interceptsUpdated, iEntry.intercept)
			continue
		}

		// This intercept looks good. Let's make sure it's marked as active.
		// Update the associated client if we change the disposition here.
		if iEntry.intercept.Disposition != rpc.InterceptInfo_ACTIVE {
			iEntry.intercept.Disposition = rpc.InterceptInfo_ACTIVE
			interceptsUpdated = append(interceptsUpdated, iEntry.intercept)
			continue // for symmetry
		}
	}

	return interceptsUpdated, agentsUpdated
}
*/

// Presence

func (s *State) Has(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sessions.IsPresent(sessionID)
}

func (s *State) Get(sessionID string) *PresenceEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sessions.Get(sessionID)
}

func (s *State) Mark(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sessions.Mark(sessionID, s.clock.Now()) == nil
}

func (s *State) Remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.agentWatches.Unsubscribe(sessionID)
	s.interceptWatches.Unsubscribe(sessionID)
	_ = s.sessions.Remove(sessionID)
}

func (s *State) Expire(moment time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions.Expire(moment)
}

// Clients

func (s *State) AddClient(client *rpc.ClientInfo) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionID := fmt.Sprintf("C%03d", s.next())
	s.sessions.Add(sessionID, client, s.clock.Now())

	return sessionID
}

func (s *State) HasClient(sessionID string) bool {
	return s.GetClient(sessionID) != nil
}

func (s *State) GetClient(sessionID string) *rpc.ClientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry := s.sessions.Get(sessionID); entry != nil {
		if res, ok := entry.Item().(*rpc.ClientInfo); ok {
			return res
		}
	}

	return nil
}

// Agents

func (s *State) AddAgent(agent *rpc.AgentInfo) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionID := fmt.Sprintf("A%03d", s.next())
	s.sessions.Add(sessionID, agent, s.clock.Now())
	s.agentWatches.NotifyAll()

	return sessionID
}

func (s *State) HasAgent(sessionID string) bool {
	return s.GetAgent(sessionID) != nil
}

func (s *State) GetAgent(sessionID string) *rpc.AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry := s.sessions.Get(sessionID); entry != nil {
		if res, ok := entry.Item().(*rpc.AgentInfo); ok {
			return res
		}
	}

	return nil
}

func (s *State) GetAgents() []*rpc.AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	agents := []*rpc.AgentInfo{}
	s.sessions.ForEach(func(_ context.Context, id string, item Entity) {
		if agent, ok := item.(*rpc.AgentInfo); ok {
			agents = append(agents, agent)
		}
	})
	return agents
}

func (s *State) GetAgentsByName(name string) []*rpc.AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	agents := []*rpc.AgentInfo{}
	s.sessions.ForEach(func(_ context.Context, id string, item Entity) {
		if agent, ok := item.(*rpc.AgentInfo); ok {
			if agent.Name == name {
				agents = append(agents, agent)
			}
		}
	})
	return agents
}

// Intercepts

func (s *State) GetIntercepts(sessionID string) []*rpc.InterceptInfo {
	entry := s.Get(sessionID)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Choose intercepts based on session type: agent or client
	var filter func(*interceptEntry) bool

	switch item := entry.Item().(type) {
	case *rpc.ClientInfo:
		filter = func(entry *interceptEntry) bool {
			return entry.clientSessionID == sessionID
		}
	case *rpc.AgentInfo:
		filter = func(entry *interceptEntry) bool {
			return entry.intercept.Spec.Agent == item.Name
		}
	default:
		return []*rpc.InterceptInfo{}
	}

	// Select the relevant subset of intercepts
	entries := []*interceptEntry{}
	for _, entry := range s.intercepts {
		if filter(entry) {
			entries = append(entries, entry)
		}
	}

	// Always return intercepts in the same order
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].index < entries[j].index
	})

	cepts := make([]*rpc.InterceptInfo, len(entries))
	for i := 0; i < len(entries); i++ {
		cepts[i] = entries[i].intercept
	}

	return cepts
}

func (s *State) AddIntercept(sessionID string, spec *rpc.InterceptSpec) *rpc.InterceptInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	ceptIndex := s.next()
	ceptID := fmt.Sprintf("I%03d", ceptIndex)
	cept := &rpc.InterceptInfo{
		Spec:        spec,
		ManagerPort: 0,
		Disposition: rpc.InterceptDispositionType_WAITING,
		Message:     "Waiting for Agent approval",
		Id:          ceptID,
	}
	entry := &interceptEntry{
		clientSessionID: sessionID,
		index:           ceptIndex,
		intercept:       cept,
	}
	s.intercepts[ceptID] = entry
	s.notifyForIntercept(entry)

	return cept
}

func (s *State) RemoveIntercept(sessionID string, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for ceptID, entry := range s.intercepts {
		correctSession := entry.clientSessionID == sessionID
		correctName := entry.intercept.Spec.Name == name
		if correctSession && correctName {
			delete(s.intercepts, ceptID)
			s.notifyForIntercept(entry)
			return true
		}
	}

	return false
}

// Watches

func (s *State) WatchAgents(sessionID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.agentWatches.Subscribe(sessionID)
}

func (s *State) WatchIntercepts(sessionID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.interceptWatches.Subscribe(sessionID)
}

func (s *State) notifyForIntercept(entry *interceptEntry) {
	s.interceptWatches.Notify(entry.clientSessionID)
	s.sessions.ForEach(func(_ context.Context, id string, item Entity) {
		if agent, ok := item.(*rpc.AgentInfo); ok {
			if agent.Name == entry.intercept.Spec.Agent {
				s.interceptWatches.Notify(id)
			}
		}
	})
}
