package state

import (
	"context"
	"time"
)

// SessionConsumptionMetricsStaleTTL is the duration after which we consider the metrics to be staled, meaning
// that they should not be updated anymore since the user doesn't really use Telepresence at the moment.
const SessionConsumptionMetricsStaleTTL = 15 * time.Minute

func NewSessionConsumptionMetrics() *SessionConsumptionMetrics {
	return &SessionConsumptionMetrics{
		ConnectDuration:     0,
		FromClientBytesChan: make(chan uint64),
		ToClientBytesChan:   make(chan uint64),

		LastUpdate: time.Now(),
	}
}

type SessionConsumptionMetrics struct {
	ConnectDuration uint32
	LastUpdate      time.Time

	// data from client to the traffic manager.
	fromClientBytes uint64
	// data from the traffic manager to the client.
	toClientBytes uint64

	FromClientBytesChan chan uint64
	ToClientBytesChan   chan uint64
}

func (sc *SessionConsumptionMetrics) RunCollect(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			sc.closeChannels()
			return
		case b, ok := <-sc.FromClientBytesChan:
			if !ok {
				return
			}
			sc.fromClientBytes += b
		case b, ok := <-sc.ToClientBytesChan:
			if !ok {
				return
			}
			sc.toClientBytes += b
		}
	}
}

func (sc *SessionConsumptionMetrics) closeChannels() {
	close(sc.FromClientBytesChan)
	close(sc.ToClientBytesChan)
}

func (s *state) GetSessionConsumptionMetrics(sessionID string) *SessionConsumptionMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.sessions {
		if i == sessionID {
			return s.sessions[i].ConsumptionMetrics()
		}
	}
	return nil
}

func (s *state) GetAllSessionConsumptionMetrics() map[string]*SessionConsumptionMetrics {
	allSCM := make(map[string]*SessionConsumptionMetrics)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for sessionID := range s.sessions {
		allSCM[sessionID] = s.sessions[sessionID].ConsumptionMetrics()
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

	lastMarked := session.LastMarked()
	consumption := s.sessions[sessionID].ConsumptionMetrics()

	// If the last mark is older than the SessionConsumptionMetricsStaleTTL, it indicates that the duration
	// metric should no longer be updated, as the user's machine may be in standby.
	isStale := time.Now().After(lastMarked.Add(SessionConsumptionMetricsStaleTTL))
	if !isStale {
		consumption.ConnectDuration += uint32(time.Since(consumption.LastUpdate).Seconds())
	}

	consumption.LastUpdate = time.Now()
}
