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
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type SessionState interface {
	Cancel()
	Done() <-chan struct{}
	LastMarked() time.Time
	SetLastMarked(lastMarked time.Time)
	Dials() chan *rpc.DialRequest
	EstablishBidiPipe(context.Context, tunnel.Stream, tunnel.ConnID) (tunnel.Endpoint, error)
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
func (ss *sessionState) EstablishBidiPipe(ctx context.Context, stream tunnel.Stream, id tunnel.ConnID) (tunnel.Endpoint, error) {
	// Dispatch directly to agent and let the dial happen there
	bidiPipeCh := make(chan tunnel.Endpoint)
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

func (ss *sessionState) Dials() chan *rpc.DialRequest {
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

type remoteFsMapping struct {
	ftp    *l3Mapping
	sftp   *l3Mapping
	gcMark bool
}

func (rm *remoteFsMapping) mappedID(id tunnel.ConnID) (tunnel.ConnID, bool) {
	if rm.ftp != nil {
		if mid, ok := rm.ftp.mappedID(id); ok {
			return mid, true
		}
	}
	if rm.sftp != nil {
		if mid, ok := rm.sftp.mappedID(id); ok {
			return mid, true
		}
	}
	return "", false
}

func (rm *remoteFsMapping) setActive(active bool) {
	if rm.ftp != nil {
		rm.ftp.setActive(active)
	}
	if rm.sftp != nil {
		rm.sftp.setActive(active)
	}
}

type clientSessionState struct {
	sessionState
	pool     *tunnel.Pool
	mappings map[string]*remoteFsMapping
}

func newClientSessionState(ctx context.Context, ts time.Time) *clientSessionState {
	return &clientSessionState{
		sessionState: newSessionState(ctx, ts),
		pool:         tunnel.NewPool(),
	}
}

// lockFileServerIP calls lockFileServerIP for each of given the intercepts snapshot and then
// removes mappings that are no longer in use.
func (ss *clientSessionState) lockFileServerIPs(cepts []*rpc.InterceptInfo) {
	ss.Lock()
	defer ss.Unlock()
	if len(cepts) == 0 {
		ss.mappings = nil
		return
	}
	if len(ss.mappings) == 0 {
		ss.mappings = make(map[string]*remoteFsMapping)
	} else {
		// Make all mappings candidates for GC
		for _, m := range ss.mappings {
			m.gcMark = true
		}
	}
	for _, cept := range cepts {
		ss.lockFileServerIPLocked(cept)
	}
	for k, m := range ss.mappings {
		if m.gcMark {
			delete(ss.mappings, k)
		}
	}
}

// lockFileServerIP ensures that the IP:port that an intercept use for ftp or sftp stays
// stable for the duration of the intercept. It does this by adding l4mappings for the initial
// IP and port 21 to the initial IP and the real port.
// Subsequent IP/port changes caused by stopping/starting intercepted pods will then just change
// the target of this mapping, and thereby hiding the changes from the client.
func (ss *clientSessionState) lockFileServerIP(cept *rpc.InterceptInfo) {
	ss.Lock()
	defer ss.Unlock()
	ss.lockFileServerIPLocked(cept)
}

func (ss *clientSessionState) lockFileServerIPLocked(cept *rpc.InterceptInfo) {
	rm := ss.mappings[cept.Id]
	if rm != nil {
		// Intercept still exists in some form, so don't gc this one.
		rm.gcMark = false
	}

	if cept.Disposition != rpc.InterceptDispositionType_ACTIVE {
		// Intercept is not active and its remote FS cannot be accessed at this point
		if rm != nil {
			rm.setActive(false)
		}
		return
	}

	podIP := iputil.Parse(cept.PodIp)
	if podIP == nil || cept.FtpPort == 0 && cept.SftpPort == 0 {
		return
	}

	if rm == nil {
		rm = &remoteFsMapping{}
		ss.mappings[cept.Id] = rm
	}
	mapPort := func(port, dfltPort int32, mp **l3Mapping) int32 {
		if port == 0 {
			// Agent no longer provides ftp/sftp. Odd, but OK.
			*mp = nil
			return 0
		}
		if m := *mp; m == nil {
			// Lock the FTP for this intercept to podIP:21. This is what the
			// client will see henceforth.
			*mp = newMapping(ipproto.TCP, podIP, podIP, uint16(dfltPort), uint16(port))
		} else {
			m.setDestination(podIP, uint16(port))
			podIP = m.Source() // ftp and sftp will always have the same source
		}
		return dfltPort
	}
	cept.FtpPort = mapPort(cept.FtpPort, 21, &rm.ftp)
	cept.SftpPort = mapPort(cept.SftpPort, 22, &rm.sftp)
	cept.PodIp = podIP.String()
}

func (ss *clientSessionState) mappedID(id tunnel.ConnID) tunnel.ConnID {
	ss.Lock()
	for _, rm := range ss.mappings {
		if mid, ok := rm.mappedID(id); ok {
			id = mid
			break
		}
	}
	ss.Unlock()
	return id
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
