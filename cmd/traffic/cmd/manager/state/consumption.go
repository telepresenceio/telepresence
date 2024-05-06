package state

import (
	"sync/atomic"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const (
	ClientRemainTick = 5 * int64(time.Second)

	// ConnectionStaleTimeout is the duration after which we consider the connection dormant
	// and not currently used.
	// The Remain call from the client arrives every 5 seconds, so if 15 seconds pass without such a call,
	// then the connection has been interrupted (the user might have closed the lid on the laptop).
	ConnectionStaleTimeout = ClientRemainTick * 6
)

func NewSessionConsumptionMetrics() *SessionConsumptionMetrics {
	m := &SessionConsumptionMetrics{
		FromClientBytes: tunnel.NewCounterProbe("FromClientBytes"),
		ToClientBytes:   tunnel.NewCounterProbe("ToClientBytes"),
	}
	m.lastUpdate.Store(time.Now().UnixNano())
	return m
}

type SessionConsumptionMetrics struct {
	connectDuration atomic.Int64
	lastUpdate      atomic.Int64

	// data from client to the traffic manager.
	FromClientBytes *tunnel.CounterProbe
	// data from the traffic manager to the client.
	ToClientBytes *tunnel.CounterProbe
}

func (m *SessionConsumptionMetrics) ConnectDuration() time.Duration {
	return time.Duration(m.connectDuration.Load())
}

func (m *SessionConsumptionMetrics) AddTimeSpent() {
	now := time.Now().UnixNano()
	timeSpent := now - m.lastUpdate.Swap(now)
	if timeSpent > ConnectionStaleTimeout {
		// The Connection was idle for a long time, and is now back, but we don't count the idle time.
		// Instead, we just use the time between two remain calls.
		timeSpent = ClientRemainTick
	}
	m.connectDuration.Add(timeSpent)
}

func (m *SessionConsumptionMetrics) LastUpdate() time.Time {
	return time.Unix(0, m.lastUpdate.Load())
}

func (m *SessionConsumptionMetrics) SetLastUpdate(t time.Time) {
	m.lastUpdate.Store(t.UnixNano())
}

func (s *state) GetSessionConsumptionMetrics(sessionID string) *SessionConsumptionMetrics {
	if css, ok := s.GetSession(sessionID).(*clientSessionState); ok {
		return css.ConsumptionMetrics()
	}
	return nil
}

func (s *state) GetAllSessionConsumptionMetrics() map[string]*SessionConsumptionMetrics {
	allSCM := make(map[string]*SessionConsumptionMetrics)
	s.sessions.Range(func(sessionID string, sess SessionState) bool {
		if css, ok := sess.(*clientSessionState); ok {
			allSCM[sessionID] = css.ConsumptionMetrics()
		}
		return true
	})
	return allSCM
}

func (s *state) AddSessionConsumptionMetrics(metrics *manager.TunnelMetrics) {
	cs, ok := s.GetSession(metrics.ClientSessionId).(*clientSessionState)
	if ok {
		cm := cs.consumptionMetrics
		cm.FromClientBytes.Increment(metrics.IngressBytes)
		cm.ToClientBytes.Increment(metrics.EgressBytes)
	}
}

// RefreshSessionConsumptionMetrics refreshes the metrics associated to a specific session.
func (s *state) RefreshSessionConsumptionMetrics(sessionID string) {
	css, ok := s.GetSession(sessionID).(*clientSessionState)
	if !ok {
		return
	}
	css.ConsumptionMetrics().AddTimeSpent()
}
