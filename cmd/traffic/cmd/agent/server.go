package agent

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type awaitingForward struct {
	streamCh chan tunnel.Stream
	doneCh   <-chan struct{}
}

func (s *state) Version(context.Context, *emptypb.Empty) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Name: DisplayName, Version: version.Version}, nil
}

func (s *state) Lookup(ctx context.Context, request *rpc.LookupRequest) (*rpc.LookupResponse, error) {
	name := request.Name
	clog.Debugf(ctx, "lookup %q", name)
	nl := len(name)
	if nl < 2 || name[nl-1] != '.' {
		return nil, status.Errorf(codes.InvalidArgument, "empty name")
	}
	var ips []netip.Addr
	response := &rpc.LookupResponse{}
	// The caller's gRPC deadline (the client bounds every cluster lookup with its
	// dns.lookupTimeout) governs the lookup. A resolution that needs search-path
	// expansion, or that passes through a service-mesh DNS proxy such as Istio's,
	// can easily take longer than a fraction of a second, so no shorter timeout is
	// imposed here. The fallback only guards against a caller without a deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", name[:nl-1])
	if err != nil {
		_, err = dnsproxy.MakeDNSError(err)
	} else {
		response.Ips = make([][]byte, len(ips))
		for i, ip := range ips {
			if ip.Is4In6() {
				ip = netip.AddrFrom4(ip.As4())
				ips[i] = ip
			}
			response.Ips[i], _ = ip.MarshalBinary()
		}
	}
	clog.Debugf(ctx, "lookup %q => %v", request.Name, ips)
	return response, err
}

func (s *state) Tunnel(server agent.Agent_TunnelServer) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, tunnel.ClientToAgent, server)
	if err != nil {
		return errors.FromError(err, codes.FailedPrecondition, err.Error())
	}
	// The session this gate lets through is the one every use of stream.SessionID()
	// below (including the ClientSessionId reported to ReportMetrics) relies on.
	if _, err := s.fileShareAuth.verifySession(ctx, stream.SessionID()); err != nil {
		return err
	}
	if awc, ok := s.awaitingForwards.Load(stream.SessionID()); ok {
		if awf, ok := awc.LoadAndDelete(stream.ID()); ok {
			awf.streamCh <- stream
			<-awf.doneCh
			return nil
		}
	}
	reporting := s.MetricsEnabled()
	var ingressBytes, egressBytes *tunnel.CounterProbe
	if reporting {
		ingressBytes = tunnel.NewCounterProbe("FromClientBytes")
		egressBytes = tunnel.NewCounterProbe("ToClientBytes")
	}

	endPoint := tunnel.NewDialer(stream, func() {}, ingressBytes, egressBytes)
	endPoint.Start(ctx)
	<-endPoint.Done()

	if reporting {
		s.ReportMetrics(ctx, &rpc.TunnelMetrics{
			ClientSessionId: string(stream.SessionID()),
			IngressBytes:    ingressBytes.GetValue(),
			EgressBytes:     egressBytes.GetValue(),
		})
	}
	return nil
}

func (s *state) WatchDial(session *rpc.SessionInfo, server agent.Agent_WatchDialServer) error {
	ctx := server.Context()
	sid := tunnel.SessionID(session.SessionId)
	verified, err := s.fileShareAuth.verifySession(ctx, sid)
	if err != nil {
		return err
	}
	clog.Debugf(ctx, "WatchDial called from client %s", session.SessionId)
	defer clog.Debugf(ctx, "WatchDial ended from client %s", session.SessionId)
	drCh := make(chan *rpc.DialRequest)

	// Displacement policy: a verified caller always takes over the slot, even from a
	// live watcher (verified or not) -- WatchDial exists to bind one channel to one
	// session, and a verified caller has proven it is that session's client. An
	// unverified caller may only take the slot when nothing is registered yet (e.g. an
	// old client racing to be first against a permissive agent); finding a live
	// watcher, it is refused rather than displacing something it hasn't proven it owns.
	if verified {
		displaced := false
		s.dialWatchers.Compute(sid, func(_ chan *rpc.DialRequest, loaded bool) (chan *rpc.DialRequest, xsync.ComputeOp) {
			displaced = loaded
			return drCh, xsync.UpdateOp
		})
		if displaced {
			clog.Debugf(ctx, "WatchDial for client %s displaced an existing dial watcher", session.SessionId)
		}
	} else if _, loaded := s.dialWatchers.LoadOrStore(sid, drCh); loaded {
		return status.Error(codes.AlreadyExists, "a dial watcher for this session is already active")
	}
	defer func() {
		// Delete only this handler's own registration: if a verified caller has since
		// displaced it, the stored channel is no longer drCh, and Compute leaves the
		// successor's entry alone.
		s.dialWatchers.Compute(sid,
			func(oldValue chan *rpc.DialRequest, loaded bool) (chan *rpc.DialRequest, xsync.ComputeOp) {
				if loaded && oldValue == drCh {
					return nil, xsync.DeleteOp
				}
				return oldValue, xsync.CancelOp
			})
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case dr, ok := <-drCh:
			if !ok {
				return nil
			}
			if err := server.Send(dr); err != nil {
				clog.Errorf(ctx, "send of DialRequest failed: %v", err)
				return nil
			}
		}
	}
}

func (s *state) CreateClientStream(ctx context.Context, _ tunnel.Tag, sessionID tunnel.SessionID, id tunnel.ConnID, roundTripLatency, dialTimeout time.Duration,
) (tunnel.Stream, error) {
	clog.Debugf(ctx, "Creating tunnel to client %s for id %s", sessionID, id)
	var drCh chan<- *rpc.DialRequest
	var awc *xsync.Map[tunnel.ConnID, *awaitingForward]
	var aw *awaitingForward
	var stCh <-chan tunnel.Stream

	// A retry is needed here because what actually happens is that the dial watcher channel drCh is inserted when the
	// client calls WatchDial. That call arrives only after the client received confirmation that it is intercepting
	// this agent, and some latency is to be expected.
	err := backoff.Retry(func() error {
		var ok bool
		drCh, ok = s.dialWatchers.Load(sessionID)
		if ok {
			awc, _ = s.awaitingForwards.LoadOrCompute(sessionID, func() (*xsync.Map[tunnel.ConnID, *awaitingForward], bool) {
				return xsync.NewMap[tunnel.ConnID, *awaitingForward](), false
			})
			aw, _ = awc.LoadOrCompute(id, func() (*awaitingForward, bool) {
				return &awaitingForward{
					streamCh: make(chan tunnel.Stream),
					doneCh:   ctx.Done(),
				}, false
			})
			stCh = aw.streamCh
			return nil
		}
		return fmt.Errorf("unable to create tunnel to client %s for id %s: no dial watcher", sessionID, id)
	}, backoff.WithContext(backoff.NewConstantBackOff(20*time.Millisecond), ctx))
	if err != nil {
		return nil, err
	}
	defer func() {
		if awc != nil && aw != nil {
			awc.Compute(id, func(current *awaitingForward, loaded bool) (*awaitingForward, xsync.ComputeOp) {
				if loaded && current == aw {
					return nil, xsync.DeleteOp
				}
				return current, xsync.CancelOp
			})
		}
	}()

	select {
	case <-ctx.Done():
		clog.Errorf(ctx, "unable to send DialRequest to client %s for id %s: %v", sessionID, id, ctx.Err())
		return nil, ctx.Err()
	case drCh <- &rpc.DialRequest{ConnId: []byte(id), DialTimeout: int64(dialTimeout), RoundtripLatency: int64(roundTripLatency)}:
	}

	select {
	case <-ctx.Done():
		clog.Errorf(ctx, "unable to create tunnel to client %s for id %s: %v", sessionID, id, ctx.Err())
		return nil, ctx.Err()
	case stream := <-stCh:
		clog.Debugf(ctx, "Created tunnel to client %s for id %s", sessionID, id)
		return stream, nil
	}
}

// ReportMetrics makes an attempt to send metrics to the traffic-manager. The provided context is just
// for logging (it can be cancelled). Errors are logged but not fatal.
func (s *state) ReportMetrics(ctx context.Context, metrics *rpc.TunnelMetrics) {
	go func() {
		mCtx, mCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer mCancel()
		_, err := s.manager.ReportMetrics(mCtx, metrics)
		if err != nil && status.Code(err) != codes.Canceled {
			clog.Errorf(ctx, "ReportMetrics failed: %v", err)
		}
	}()
}

func (s *state) MetricsEnabled() bool {
	return s.AgentConfig().EnableMetrics
}
