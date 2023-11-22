package state

import (
	"time"

	"github.com/stretchr/testify/assert"
)

func (s *suiteState) TestRefreshSessionConsumptionMetrics() {
	// given
	now := time.Now()
	session1 := &clientSessionState{}
	session3 := &clientSessionState{}
	s.state.sessions.Store("session-1", session1)
	s.state.sessions.Store("session-2", &agentSessionState{})
	s.state.sessions.Store("session-3", session3)
	session1.consumptionMetrics = &SessionConsumptionMetrics{
		connectDuration: int64(42 * time.Second),
		lastUpdate:      now.Add(-time.Minute).UnixNano(),
	}
	// staled metric
	session3.consumptionMetrics = &SessionConsumptionMetrics{
		connectDuration: int64(36 * time.Second),
		lastUpdate:      now.Add(-SessionConsumptionMetricsStaleTTL - time.Minute).UnixNano(),
	}

	// when
	s.state.RefreshSessionConsumptionMetrics("session-1")
	s.state.RefreshSessionConsumptionMetrics("session-2") // should not fail even if it's an agent session.
	s.state.RefreshSessionConsumptionMetrics("session-3") // should not refresh a stale metric.
	s.state.RefreshSessionConsumptionMetrics("session-4") // doesn't exist but shouldn't fail.

	// then
	ccs1, _ := s.state.sessions.Load("session-1")
	ccs3, _ := s.state.sessions.Load("session-3")

	assert.Len(s.T(), s.state.GetAllSessionConsumptionMetrics(), 2)
	assert.True(s.T(), ccs1.(*clientSessionState).ConsumptionMetrics().ConnectDuration() > 42*time.Second)
	assert.Equal(s.T(), 36*time.Second, ccs3.(*clientSessionState).ConsumptionMetrics().ConnectDuration())
}
