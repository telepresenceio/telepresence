package state

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type SessionState interface {
	Cancel()
	Done() <-chan struct{}
	LastMarked() time.Time
	SetLastMarked(lastMarked time.Time)
	Dials() <-chan *rpc.DialRequest
	EstablishBidiPipe(context.Context, tunnel.Stream) (tunnel.Endpoint, error)
	OnConnect(context.Context, tunnel.Stream) (tunnel.Endpoint, error)
}

type awaitingBidiPipe struct {
	stream     tunnel.Stream
	bidiPipeCh chan tunnel.Endpoint
}

type sessionState struct {
	sync.Mutex
	doneCh              <-chan struct{}
	cancel              context.CancelFunc
	lastMarked          time.Time
	awaitingBidiPipeMap map[tunnel.ConnID]*awaitingBidiPipe
	dials               chan *rpc.DialRequest
}

// EstablishBidiPipe registers the given stream as waiting for a matching stream to arrive in a call
// to Tunnel, sends a DialRequest to the owner of this sessionState, and then waits. When the call
// arrives, a BidiPipe connecting the two streams is returned.
func (ss *sessionState) EstablishBidiPipe(ctx context.Context, stream tunnel.Stream) (tunnel.Endpoint, error) {
	// Dispatch directly to agent and let the dial happen there
	bidiPipeCh := make(chan tunnel.Endpoint)
	id := stream.ID()
	abp := &awaitingBidiPipe{stream: stream, bidiPipeCh: bidiPipeCh}

	ss.Lock()
	if ss.awaitingBidiPipeMap == nil {
		ss.awaitingBidiPipeMap = map[tunnel.ConnID]*awaitingBidiPipe{id: abp}
	} else {
		ss.awaitingBidiPipeMap[id] = abp
	}
	ss.Unlock()

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

// OnConnect checks if a stream is waiting for the given stream to arrive in order to create a BidiPipe.
// If that's the case, the BidiPipe is created, started, and returned by both this method and the EstablishBidiPipe
// method that registered the waiting stream. Otherwise, this method returns nil.
func (ss *sessionState) OnConnect(ctx context.Context, stream tunnel.Stream) (tunnel.Endpoint, error) {
	id := stream.ID()
	ss.Lock()
	abp, ok := ss.awaitingBidiPipeMap[id]
	if ok {
		delete(ss.awaitingBidiPipeMap, id)
	}
	ss.Unlock()

	if !ok {
		return nil, nil
	}
	dlog.Debugf(ctx, "   FWD %s, connect session %s with %s", id, abp.stream.SessionID(), stream.SessionID())
	bidiPipe := tunnel.NewBidiPipe(abp.stream, stream)
	bidiPipe.Start(ctx)

	defer close(abp.bidiPipeCh)
	select {
	case <-ss.Done():
		return nil, status.Error(codes.Canceled, "session cancelled")
	case abp.bidiPipeCh <- bidiPipe:
		return bidiPipe, nil
	}
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
	return ss.lastMarked
}

func (ss *sessionState) SetLastMarked(lastMarked time.Time) {
	ss.lastMarked = lastMarked
}

func newSessionState(ctx context.Context, now time.Time) sessionState {
	ctx, cancel := context.WithCancel(ctx)
	return sessionState{
		doneCh:     ctx.Done(),
		cancel:     cancel,
		lastMarked: now,
		dials:      make(chan *rpc.DialRequest),
	}
}

type clientSessionState struct {
	sessionState
	pool *tunnel.Pool
}

func newClientSessionState(ctx context.Context, ts time.Time) *clientSessionState {
	return &clientSessionState{
		sessionState: newSessionState(ctx, ts),
		pool:         tunnel.NewPool(),
	}
}

type agentSessionState struct {
	sessionState
	dnsRequests  chan *rpc.DNSRequest
	dnsResponses map[string]chan *rpc.DNSResponse
}

func newAgentSessionState(ctx context.Context, ts time.Time) *agentSessionState {
	return &agentSessionState{
		sessionState: newSessionState(ctx, ts),
		dnsRequests:  make(chan *rpc.DNSRequest),
		dnsResponses: make(map[string]chan *rpc.DNSResponse),
	}
}

func (ss *agentSessionState) Cancel() {
	close(ss.dnsRequests)
	for k, lr := range ss.dnsResponses {
		delete(ss.dnsResponses, k)
		close(lr)
	}
	ss.sessionState.Cancel()
}
