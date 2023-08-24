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
	s.state.sessions["session-1"] = session1
	s.state.sessions["session-2"] = &agentSessionState{}
	s.state.sessions["session-3"] = session3
	session1.consumptionMetrics = &SessionConsumptionMetrics{
		ConnectDuration: 42,
		LastUpdate:      now.Add(-time.Minute),
	}
	// staled metric
	session3.consumptionMetrics = &SessionConsumptionMetrics{
		ConnectDuration: 36,
		LastUpdate:      now.Add(-SessionConsumptionMetricsStaleTTL - time.Minute),
	}

	// when
	s.state.RefreshSessionConsumptionMetrics("session-1")
	s.state.RefreshSessionConsumptionMetrics("session-2") // should not fail even if it's an agent session.
	s.state.RefreshSessionConsumptionMetrics("session-3") // should not refresh a stale metric.
	s.state.RefreshSessionConsumptionMetrics("session-4") // doesn't exist but shouldn't fail.

	// then
	ccs1 := s.state.sessions["session-1"].(*clientSessionState)
	ccs3 := s.state.sessions["session-3"].(*clientSessionState)

	assert.Len(s.T(), s.state.GetAllSessionConsumptionMetrics(), 2)
	assert.True(s.T(), (ccs1.ConsumptionMetrics().ConnectDuration) > 42)
	assert.Equal(s.T(), 36, int(ccs3.ConsumptionMetrics().ConnectDuration))
}
