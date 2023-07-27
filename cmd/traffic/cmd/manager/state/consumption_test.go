package state

import (
	"time"

	"github.com/stretchr/testify/assert"
)

func (s *suiteState) TestRefreshSessionConsumptionMetrics() {
	// given
	now := time.Now()
	session1 := &clientSessionState{}
	session1.SetLastMarked(now)
	session3 := &clientSessionState{}
	session3.SetLastMarked(now.Add(-24 * time.Hour * 30))
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
		LastUpdate:      session3.lastMarked,
	}

	// when
	s.state.RefreshSessionConsumptionMetrics("session-1")
	s.state.RefreshSessionConsumptionMetrics("session-2") // should not fail.
	s.state.RefreshSessionConsumptionMetrics("session-3") // should not refresh a stale metric.

	// then
	assert.Len(s.T(), s.state.GetAllSessionConsumptionMetrics(), 3)
	assert.True(s.T(), (s.state.sessions["session-1"].ConsumptionMetrics().ConnectDuration) > 42)
	assert.Equal(s.T(), 36, int(s.state.sessions["session-3"].ConsumptionMetrics().ConnectDuration))
}
