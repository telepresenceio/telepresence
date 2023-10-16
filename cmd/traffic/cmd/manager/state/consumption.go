package state

import (
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// SessionConsumptionMetricsStaleTTL is the duration after which we consider the metrics to be staled, meaning
// that they should not be updated anymore since the user doesn't really use Telepresence at the moment.
const SessionConsumptionMetricsStaleTTL = 60 * time.Minute

func NewSessionConsumptionMetrics() *SessionConsumptionMetrics {
	return &SessionConsumptionMetrics{
		ConnectDuration: 0,
		FromClientBytes: tunnel.NewCounterProbe("FromClientBytes"),
		ToClientBytes:   tunnel.NewCounterProbe("ToClientBytes"),

		LastUpdate: time.Now(),
	}
}

type SessionConsumptionMetrics struct {
	ConnectDuration uint32
	LastUpdate      time.Time

	// data from client to the traffic manager.
	FromClientBytes *tunnel.CounterProbe
	// data from the traffic manager to the client.
	ToClientBytes *tunnel.CounterProbe
}

func (s *state) GetSessionConsumptionMetrics(sessionID string) *SessionConsumptionMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.sessions {
		if css, ok := s.sessions[i].(*clientSessionState); i == sessionID && ok {
			return css.ConsumptionMetrics()
		}
	}
	return nil
}

func (s *state) GetAllSessionConsumptionMetrics() map[string]*SessionConsumptionMetrics {
	allSCM := make(map[string]*SessionConsumptionMetrics)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for sessionID := range s.sessions {
		if css, ok := s.sessions[sessionID].(*clientSessionState); ok {
			allSCM[sessionID] = css.ConsumptionMetrics()
		}
	}
	return allSCM
}

// RefreshSessionConsumptionMetrics refreshes the metrics associated to a specific session.
func (s *state) RefreshSessionConsumptionMetrics(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session := s.sessions[sessionID]
	if _, isClientSession := session.(*clientSessionState); !isClientSession {
		return
	}

	var scm *SessionConsumptionMetrics
	if css, ok := s.sessions[sessionID].(*clientSessionState); ok {
		scm = css.ConsumptionMetrics()
	} else {
		return
	}

	// If last update is more than SessionConsumptionMetricsStaleTTL old, probably that the reporting was interrupted.
	wasInterrupted := time.Now().After(scm.LastUpdate.Add(SessionConsumptionMetricsStaleTTL))
	if !wasInterrupted { // If it wasn't stale, we want to count duration since last metric update.
		scm.ConnectDuration += uint32(time.Since(scm.LastUpdate).Seconds())
	}

	scm.LastUpdate = time.Now()
}
