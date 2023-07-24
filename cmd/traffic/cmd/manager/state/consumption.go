package state

import (
	"time"
)

// SessionConsumptionMetricsStaleTTL is the duration after which we consider the metrics to be staled, meaning
// that they should not be updated anymore since the user doesn't really use Telepresence at the moment.
const SessionConsumptionMetricsStaleTTL = 1 * time.Minute // TODO: Increase.

type SessionConsumptionMetrics struct {
	Duration   uint32
	LastUpdate time.Time
}

func (s *state) unlockedAddSessionConsumption(sessionID string) {
	s.sessionConsumptionMetrics[sessionID] = &SessionConsumptionMetrics{
		Duration:   0,
		LastUpdate: time.Now(),
	}
}

func (s *state) unlockedRemoveSessionConsumption(sessionID string) {
	delete(s.sessionConsumptionMetrics, sessionID)
}

func (s *state) GetSessionConsumptionMetrics(sessionID string) *SessionConsumptionMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionConsumptionMetrics[sessionID]
}

func (c *state) GetAllSessionConsumptionMetrics() map[string]*SessionConsumptionMetrics {
	scmCopy := make(map[string]*SessionConsumptionMetrics)
	c.mu.RLock()
	defer c.mu.RUnlock()
	for sessionID, val := range c.sessionConsumptionMetrics {
		valCopy := *val
		scmCopy[sessionID] = &valCopy
	}
	return scmCopy
}

// RefreshSessionConsumptionMetrics refreshes the metrics associated to a specific session.
func (s *state) RefreshSessionConsumptionMetrics(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session := s.sessions[sessionID]
	if _, isClientSession := session.(*clientSessionState); !isClientSession {
		return
	}

	lastMarked := session.LastMarked()
	consumption := s.sessionConsumptionMetrics[sessionID]

	// if last mark is more than SessionConsumptionMetricsStaleTTL old, it means the duration metric should stop being
	// updated since the user machine is maybe in standby.
	isStale := time.Now().After(lastMarked.Add(SessionConsumptionMetricsStaleTTL))
	if !isStale {
		consumption.Duration += uint32(time.Since(consumption.LastUpdate).Seconds())
	}

	consumption.LastUpdate = time.Now()
}
