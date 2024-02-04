package state

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const AgentSessionIDPrefix = "agent:"

type SessionState interface {
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
	doneCh              <-chan struct{}
	cancel              context.CancelFunc
	lastMarked          int64
	awaitingBidiPipeMap *xsync.MapOf[tunnel.ConnID, awaitingBidiPipe]
	dials               chan *rpc.DialRequest
}

// EstablishBidiPipe registers the given stream as waiting for a matching stream to arrive in a call
// to Tunnel, sends a DialRequest to the owner of this sessionState, and then waits. When the call
// arrives, a BidiPipe connecting the two streams is returned.
func (ss *sessionState) EstablishBidiPipe(ctx context.Context, stream tunnel.Stream) (tunnel.Endpoint, error) {
	// Dispatch directly to agent and let the dial happen there
	bidiPipeCh := make(chan tunnel.Endpoint)
	id := stream.ID()
	ss.awaitingBidiPipeMap.Store(id, awaitingBidiPipe{ctx: ctx, stream: stream, bidiPipeCh: bidiPipeCh})

	// Send dial request to the client/agent
	dr := &rpc.DialRequest{
		ConnId:           []byte(id),
		RoundtripLatency: int64(stream.RoundtripLatency()),
		DialTimeout:      int64(stream.DialTimeout()),
	}
	propagator := otel.GetTextMapPropagator()
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	dr.TraceContext = carrier
	select {
	case <-ss.Done():
		return nil, status.Error(codes.Canceled, "session cancelled")
	case ss.dials <- dr:
	}

	// Wait for the client/agent to connect. Allow extra time for the call
	ctx, cancel := context.WithTimeout(ctx, stream.DialTimeout()+stream.RoundtripLatency())
	defer cancel()
	select {
	case <-ctx.Done():
		return nil, status.Error(codes.DeadlineExceeded, "timeout while establishing bidipipe")
	case <-ss.Done():
		return nil, status.Error(codes.Canceled, "session cancelled")
	case bidi := <-bidiPipeCh:
		return bidi, nil
	}
}

func (ss *sessionState) AwaitingBidiMapOwnerSessionID(stream tunnel.Stream) string {
	if abp, ok := ss.awaitingBidiPipeMap.Load(stream.ID()); ok {
		return abp.stream.SessionID()
	}
	return ""
}

// OnConnect checks if a stream is waiting for the given stream to arrive in order to create a BidiPipe.
// If that's the case, the BidiPipe is created, started, and returned by both this method and the EstablishBidiPipe
// method that registered the waiting stream. Otherwise, this method returns nil.
func (ss *sessionState) OnConnect(
	ctx context.Context,
	stream tunnel.Stream,
	counter *int32,
	consumptionMetrics *SessionConsumptionMetrics,
) (tunnel.Endpoint, error) {
	id := stream.ID()
	// abp is a session corresponding to an end user machine
	abp, ok := ss.awaitingBidiPipeMap.LoadAndDelete(id)
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
	case <-ss.Done():
		return nil, status.Error(codes.Canceled, "session cancelled")
	case abp.bidiPipeCh <- bidiPipe:
		return bidiPipe, nil
	}
}

func (ss *sessionState) Active() bool {
	return true
}

func (ss *sessionState) Cancel() {
	ss.cancel()
	close(ss.dials)
}

func (ss *sessionState) Dials() <-chan *rpc.DialRequest {
	return ss.dials
}

func (ss *sessionState) Done() <-chan struct{} {
	return ss.doneCh
}

func (ss *sessionState) LastMarked() time.Time {
	return time.Unix(0, atomic.LoadInt64(&ss.lastMarked))
}

func (ss *sessionState) SetLastMarked(lastMarked time.Time) {
	atomic.StoreInt64(&ss.lastMarked, lastMarked.UnixNano())
}

func newSessionState(ctx context.Context, now time.Time) sessionState {
	ctx, cancel := context.WithCancel(ctx)
	return sessionState{
		doneCh:              ctx.Done(),
		cancel:              cancel,
		lastMarked:          now.UnixNano(),
		dials:               make(chan *rpc.DialRequest),
		awaitingBidiPipeMap: xsync.NewMapOf[tunnel.ConnID, awaitingBidiPipe](),
	}
}

type clientSessionState struct {
	sessionState
	pool *tunnel.Pool

	consumptionMetrics *SessionConsumptionMetrics
}

func (css *clientSessionState) ConsumptionMetrics() *SessionConsumptionMetrics {
	return css.consumptionMetrics
}

func newClientSessionState(ctx context.Context, ts time.Time) *clientSessionState {
	return &clientSessionState{
		sessionState: newSessionState(ctx, ts),
		pool:         tunnel.NewPool(),

		consumptionMetrics: NewSessionConsumptionMetrics(),
	}
}

type agentSessionState struct {
	sessionState
	dnsRequests  chan *rpc.DNSRequest
	dnsResponses map[string]chan *rpc.DNSResponse
	active       atomic.Bool
}

func newAgentSessionState(ctx context.Context, ts time.Time) *agentSessionState {
	as := &agentSessionState{
		sessionState: newSessionState(ctx, ts),
		dnsRequests:  make(chan *rpc.DNSRequest),
		dnsResponses: make(map[string]chan *rpc.DNSResponse),
	}
	as.active.Store(true)
	return as
}

func (ss *agentSessionState) Active() bool {
	return ss.active.Load()
}

func (ss *agentSessionState) Cancel() {
	ss.active.Store(false)
	close(ss.dnsRequests)
	for k, lr := range ss.dnsResponses {
		delete(ss.dnsResponses, k)
		close(lr)
	}
	ss.sessionState.Cancel()
}
