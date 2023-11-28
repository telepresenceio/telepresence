package state

import (
	"sync/atomic"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// SessionConsumptionMetricsStaleTTL is the duration after which we consider the metrics to be staled, meaning
// that they should not be updated anymore since the user doesn't really use Telepresence at the moment.
const SessionConsumptionMetricsStaleTTL = 60 * time.Minute

func NewSessionConsumptionMetrics() *SessionConsumptionMetrics {
	return &SessionConsumptionMetrics{
		connectDuration: 0,
		FromClientBytes: tunnel.NewCounterProbe("FromClientBytes"),
		ToClientBytes:   tunnel.NewCounterProbe("ToClientBytes"),
		lastUpdate:      time.Now().UnixNano(),
	}
}

type SessionConsumptionMetrics struct {
	connectDuration int64
	lastUpdate      int64

	// data from client to the traffic manager.
	FromClientBytes *tunnel.CounterProbe
	// data from the traffic manager to the client.
	ToClientBytes *tunnel.CounterProbe
}

func (m *SessionConsumptionMetrics) ConnectDuration() time.Duration {
	return time.Duration(atomic.LoadInt64(&m.connectDuration))
}

func (m *SessionConsumptionMetrics) SetConnectDuration(d time.Duration) {
	atomic.StoreInt64(&m.connectDuration, int64(d))
}

func (m *SessionConsumptionMetrics) LastUpdate() time.Time {
	return time.Unix(0, atomic.LoadInt64(&m.lastUpdate))
}

func (m *SessionConsumptionMetrics) SetLastUpdate(t time.Time) {
	atomic.StoreInt64(&m.lastUpdate, t.UnixNano())
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
	scm := css.ConsumptionMetrics()

	// If last update is more than SessionConsumptionMetricsStaleTTL old, probably that the reporting was interrupted.
	lu := scm.LastUpdate()
	now := time.Now()
	wasInterrupted := now.After(lu.Add(SessionConsumptionMetricsStaleTTL))
	if !wasInterrupted { // If it wasn't stale, we want to count duration since last metric update.
		scm.SetConnectDuration(now.Sub(lu))
	}
	scm.SetLastUpdate(now)
}
