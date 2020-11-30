package manager

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
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

const (
	loPort = 6000
	hiPort = 8000
)

// State is the total state of the Traffic Manager.
type State struct {
	mu      sync.Mutex
	counter int
	port    int32
	clock   Clock

	agentWatches     *Watches
	interceptWatches *Watches

	intercepts map[string]*interceptEntry

	sessions *Presence
}

func NewState(ctx context.Context, clock Clock) *State {
	s := &State{
		port:             loPort - 1,
		clock:            clock,
		agentWatches:     NewWatches(),
		interceptWatches: NewWatches(),
		intercepts:       make(map[string]*interceptEntry),
	}
	s.sessions = NewPresence(ctx, s.presenceRemove)

	return s
}

// Internal

func (s *State) next() int {
	s.counter++
	return s.counter
}

func (s *State) nextUnusedPort() int32 {
	for attempts := 0; attempts < hiPort-loPort; attempts++ {
		// Bump the port number

		s.port++

		if s.port == hiPort {
			s.port = loPort
		}

		// Check whether the new port number is available

		used := false
		for _, entry := range s.intercepts {
			if entry.intercept.ManagerPort == s.port {
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

func (s *State) presenceRemove(_ context.Context, sessionID string, item Entity) {
	s.agentWatches.Unsubscribe(sessionID)
	s.interceptWatches.Unsubscribe(sessionID)

	switch info := item.(type) {
	case *rpc.ClientInfo:
		// Removing a client must remove all of its intercept
		for ceptID, entry := range s.intercepts {
			if entry.clientSessionID == sessionID {
				delete(s.intercepts, ceptID)
				s.notifyForIntercept(entry)
			}
		}
	case *rpc.AgentInfo:
		s.agentWatches.NotifyAll()

		// Removing the last agent associated with an intercept invalidates the
		// intercept.
		anyMoreAgents := false
		s.sessions.ForEach(func(_ context.Context, id string, item Entity) {
			if agent, ok := item.(*rpc.AgentInfo); ok {
				if agent.Name == info.Name {
					anyMoreAgents = true
				}
			}
		})
		if !anyMoreAgents {
			message := fmt.Sprintf("No more agents for %q exist", info.Name)
			for _, entry := range s.intercepts {
				if entry.intercept.Spec.Agent == info.Name {
					entry.intercept.Disposition = rpc.InterceptDispositionType_NO_AGENT
					entry.intercept.Message = message
					s.notifyForIntercept(entry)
				}
			}
		}
	}
}

func (s *State) Remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.sessions.Remove(sessionID) // Calls presenceRemove above
}

func (s *State) Expire(age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions.Expire(s.clock.Now().Add(-age))
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
	agents := append(s.GetAgentsByName(agent.Name), agent)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Adding an agent can invalidate existing intercepts because the set of
	// agents may no longer be self-consistent, e.g., during a rolling update of
	// a deployment. Validate the relevant intercepts if the set of agents has
	// more than one agent.
	if len(agents) > 1 && !agentsAreCompatible(agents) {
		message := fmt.Sprintf("Agents for %q are not consistent", agent.Name)
		for _, entry := range s.intercepts {
			if entry.intercept.Spec.Agent == agent.Name {
				entry.intercept.Disposition = rpc.InterceptDispositionType_NO_AGENT
				entry.intercept.Message = message
				s.notifyForIntercept(entry)
			}
		}
	}

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
	if entry == nil {
		return []*rpc.InterceptInfo{}
	}

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
	agents := s.GetAgentsByName(spec.Agent)

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

	switch {
	case len(agents) == 0:
		cept.Disposition = rpc.InterceptDispositionType_NO_AGENT
		cept.Message = fmt.Sprintf("No agent found for %q", spec.Agent)
	case !agentsAreCompatible(agents):
		cept.Disposition = rpc.InterceptDispositionType_NO_AGENT
		cept.Message = fmt.Sprintf("Agents for %q are not consistent", spec.Agent)
	case !agentHasMechanism(agents[0], spec.Mechanism):
		cept.Disposition = rpc.InterceptDispositionType_NO_MECHANISM
		cept.Message = fmt.Sprintf("Agents for %q do not have mechanism %q", spec.Agent, spec.Mechanism)
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

func (s *State) ReviewIntercept(sessionID string, ceptID string, disposition rpc.InterceptDispositionType, message string) bool {
	agent := s.GetAgent(sessionID)

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.intercepts[ceptID]
	if !ok {
		return false
	}

	// Sanity check: The reviewing agent must be an agent for the intercept.
	if entry.intercept.Spec.Agent != agent.Name {
		return false
	}

	// Only update intercepts in the waiting state. Agents race to review an
	// intercept, but we expect they will always compatible answers.
	if entry.intercept.Disposition == rpc.InterceptDispositionType_WAITING {
		entry.intercept.Disposition = disposition
		entry.intercept.Message = message

		// An intercept going active needs to be allocated a free port
		if disposition == rpc.InterceptDispositionType_ACTIVE {
			entry.intercept.ManagerPort = s.nextUnusedPort()
			if entry.intercept.ManagerPort == 0 {
				// Wow, there are no ports left! That is ... unlikely!
				entry.intercept.Disposition = rpc.InterceptDispositionType_NO_PORTS
			}
		}

		// We've updated an intercept. Notify all interested parties.
		s.notifyForIntercept(entry)
	}

	return true
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
