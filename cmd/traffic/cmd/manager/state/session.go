package state

import (
	"context"
	"sync/atomic"
	"time"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const AgentSessionIDPrefix = "agent:"

type sessionState struct {
	id        tunnel.SessionID
	doneCh    <-chan struct{}
	cancel    context.CancelFunc
	timestamp int64
}

func (s *sessionState) sessionID() tunnel.SessionID {
	return s.id
}

func (s *sessionState) done() <-chan struct{} {
	return s.doneCh
}

func (s *sessionState) lastMarked() time.Time {
	return time.Unix(0, atomic.LoadInt64(&s.timestamp))
}

func (s *sessionState) Mark(lastMarked time.Time) bool {
	oldMark := atomic.LoadInt64(&s.timestamp)
	newMark := lastMarked.UnixNano()
	return oldMark < newMark && atomic.CompareAndSwapInt64(&s.timestamp, oldMark, newMark)
}

func (s *sessionState) adjustMark(diff time.Duration) {
	mark := atomic.LoadInt64(&s.timestamp)
	atomic.CompareAndSwapInt64(&s.timestamp, mark, mark+int64(diff))
}

func newSessionState(ctx context.Context, id tunnel.SessionID, now time.Time) sessionState {
	ctx, cancel := context.WithCancel(ctx)
	return sessionState{
		id:        id,
		doneCh:    ctx.Done(),
		cancel:    cancel,
		timestamp: now.UnixNano(),
	}
}

type ClientSession struct {
	*rpc.ClientInfo
	sessionState
	consumptionMetrics *SessionConsumptionMetrics
}

func (cs *ClientSession) ConsumptionMetrics() *SessionConsumptionMetrics {
	return cs.consumptionMetrics
}

func newClientSessionState(ctx context.Context, id tunnel.SessionID, ci *rpc.ClientInfo, ts time.Time) *ClientSession {
	return &ClientSession{
		ClientInfo:         ci,
		sessionState:       newSessionState(ctx, id, ts),
		consumptionMetrics: NewSessionConsumptionMetrics(),
	}
}

type AgentSession struct {
	*rpc.AgentInfo
	sessionState
}

func newAgentSessionState(ctx context.Context, id tunnel.SessionID, ai *rpc.AgentInfo, ts time.Time) *AgentSession {
	as := &AgentSession{
		AgentInfo:    ai,
		sessionState: newSessionState(ctx, id, ts),
	}
	return as
}
