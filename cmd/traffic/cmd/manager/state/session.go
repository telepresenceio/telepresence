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
	id         tunnel.SessionID
	doneCh     <-chan struct{}
	cancel     context.CancelFunc
	lastMarked int64
}

func (s *sessionState) ID() tunnel.SessionID {
	return s.id
}

func (s *sessionState) Cancel() {
	s.cancel()
}

func (s *sessionState) Done() <-chan struct{} {
	return s.doneCh
}

func (s *sessionState) LastMarked() time.Time {
	return time.Unix(0, atomic.LoadInt64(&s.lastMarked))
}

func (s *sessionState) SetLastMarked(lastMarked time.Time) {
	atomic.StoreInt64(&s.lastMarked, lastMarked.UnixNano())
}

func newSessionState(ctx context.Context, id tunnel.SessionID, now time.Time) sessionState {
	ctx, cancel := context.WithCancel(ctx)
	return sessionState{
		id:         id,
		doneCh:     ctx.Done(),
		cancel:     cancel,
		lastMarked: now.UnixNano(),
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
