package state

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const AgentSessionIDPrefix = "agent:"

type SessionState interface {
	ID() tunnel.SessionID
	Active() bool
	Cancel()
	AwaitingBidiMapOwnerSessionID(stream tunnel.Stream) string
	Done() <-chan struct{}
	LastMarked() time.Time
	SetLastMarked(lastMarked time.Time)
	Dials() <-chan *rpc.DialRequest
	EstablishBidiPipe(context.Context, tunnel.Stream) (tunnel.Endpoint, error)
	OnConnect(context.Context, tunnel.Stream, *int32, *SessionConsumptionMetrics) (tunnel.Endpoint, error)
}

type awaitingBidiPipe struct {
	ctx        context.Context
	stream     tunnel.Stream
	bidiPipeCh chan tunnel.Endpoint
}

type sessionState struct {
	id                  tunnel.SessionID
	doneCh              <-chan struct{}
	cancel              context.CancelFunc
	lastMarked          int64
	awaitingBidiPipeMap *xsync.Map[tunnel.ConnID, awaitingBidiPipe]
	dials               chan *rpc.DialRequest
}

func (s *sessionState) ID() tunnel.SessionID {
	return s.id
}

// EstablishBidiPipe registers the given stream as waiting for a matching stream to arrive in a call
// to Tunnel, sends a DialRequest to the owner of this sessionState, and then waits. When the call
// arrives, a BidiPipe connecting the two streams is returned.
func (s *sessionState) EstablishBidiPipe(ctx context.Context, stream tunnel.Stream) (tunnel.Endpoint, error) {
	// Dispatch directly to agent and let the dial happen there
	bidiPipeCh := make(chan tunnel.Endpoint)
	id := stream.ID()
	s.awaitingBidiPipeMap.Store(id, awaitingBidiPipe{ctx: ctx, stream: stream, bidiPipeCh: bidiPipeCh})

	// Send dial request to the client/agent
	dr := &rpc.DialRequest{
		ConnId:           []byte(id),
		RoundtripLatency: int64(stream.RoundtripLatency()),
		DialTimeout:      int64(stream.DialTimeout()),
	}
	select {
	case <-s.Done():
		return nil, status.Error(codes.Canceled, "session cancelled")
	case s.dials <- dr:
	}

	// Wait for the client/agent to connect. Allow extra time for the call
	ctx, cancel := context.WithTimeout(ctx, stream.DialTimeout()+stream.RoundtripLatency())
	defer cancel()
	select {
	case <-ctx.Done():
		return nil, status.Error(codes.DeadlineExceeded, "timeout while establishing bidipipe")
	case <-s.Done():
		return nil, status.Error(codes.Canceled, "session cancelled")
	case bidi := <-bidiPipeCh:
		return bidi, nil
	}
}

func (s *sessionState) AwaitingBidiMapOwnerSessionID(stream tunnel.Stream) tunnel.SessionID {
	if abp, ok := s.awaitingBidiPipeMap.Load(stream.ID()); ok {
		return abp.stream.SessionID()
	}
	return ""
}

// OnConnect checks if a stream is waiting for the given stream to arrive in order to create a BidiPipe.
// If that's the case, the BidiPipe is created, started, and returned by both this method and the EstablishBidiPipe
// method that registered the waiting stream. Otherwise, this method returns nil.
func (s *sessionState) OnConnect(
	ctx context.Context,
	stream tunnel.Stream,
	counter *int32,
	consumptionMetrics *SessionConsumptionMetrics,
) (tunnel.Endpoint, error) {
	id := stream.ID()
	// abp is a session corresponding to an end user machine
	abp, ok := s.awaitingBidiPipeMap.LoadAndDelete(id)
	if !ok {
		return nil, nil
	}
	name := fmt.Sprintf("%s: session %s -> %s", id, abp.stream.SessionID(), stream.SessionID())
	tunnelProbes := &tunnel.BidiPipeProbes{}
	if consumptionMetrics != nil {
		tunnelProbes.BytesProbeA = consumptionMetrics.FromClientBytes
		tunnelProbes.BytesProbeB = consumptionMetrics.ToClientBytes
	}

	bidiPipe := tunnel.NewBidiPipe(abp.stream, stream, name, counter, tunnelProbes)
	bidiPipe.Start(abp.ctx)

	defer close(abp.bidiPipeCh)
	select {
	case <-s.Done():
		return nil, status.Error(codes.Canceled, "session cancelled")
	case abp.bidiPipeCh <- bidiPipe:
		return bidiPipe, nil
	}
}

func (s *sessionState) Cancel() {
	s.cancel()
	close(s.dials)
}

func (s *sessionState) Dials() <-chan *rpc.DialRequest {
	return s.dials
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
		id:                  id,
		doneCh:              ctx.Done(),
		cancel:              cancel,
		lastMarked:          now.UnixNano(),
		dials:               make(chan *rpc.DialRequest),
		awaitingBidiPipeMap: xsync.NewMap[tunnel.ConnID, awaitingBidiPipe](),
	}
}

type ClientSession struct {
	*rpc.ClientInfo
	sessionState
	pool *tunnel.Pool

	consumptionMetrics *SessionConsumptionMetrics
}

func (cs *ClientSession) ConsumptionMetrics() *SessionConsumptionMetrics {
	return cs.consumptionMetrics
}

func newClientSessionState(ctx context.Context, id tunnel.SessionID, ci *rpc.ClientInfo, ts time.Time) *ClientSession {
	return &ClientSession{
		ClientInfo:         ci,
		sessionState:       newSessionState(ctx, id, ts),
		pool:               tunnel.NewPool(),
		consumptionMetrics: NewSessionConsumptionMetrics(),
	}
}

type AgentSession struct {
	*rpc.AgentInfo
	sessionState
	dnsRequests  chan *rpc.DNSRequest
	dnsResponses *xsync.Map[string, chan *rpc.DNSResponse]
}

func newAgentSessionState(ctx context.Context, id tunnel.SessionID, ai *rpc.AgentInfo, ts time.Time) *AgentSession {
	as := &AgentSession{
		AgentInfo:    ai,
		sessionState: newSessionState(ctx, id, ts),
		dnsRequests:  make(chan *rpc.DNSRequest),
		dnsResponses: xsync.NewMap[string, chan *rpc.DNSResponse](),
	}
	return as
}

func (as *AgentSession) Cancel() {
	close(as.dnsRequests)
	as.dnsResponses.Range(func(k string, lr chan *rpc.DNSResponse) bool {
		as.dnsResponses.Delete(k)
		close(lr)
		return true
	})
	as.sessionState.Cancel()
}
