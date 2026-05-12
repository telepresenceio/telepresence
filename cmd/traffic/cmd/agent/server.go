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

const (
	clientTunnelSlowAfter       = time.Second
	clientTunnelStillWaitingLog = 5 * time.Second
)

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
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
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
	tunnelStart := time.Now()
	if awc, ok := s.awaitingForwards.Load(stream.SessionID()); ok {
		if awf, ok := awc.LoadAndDelete(stream.ID()); ok {
			awf.streamCh <- stream
			<-awf.doneCh
			if elapsed := time.Since(tunnelStart); elapsed > clientTunnelSlowAfter {
				clog.Debugf(ctx, "agent tunnel for awaited client stream stayed open for %s: session=%s conn=%s", elapsed.Round(time.Millisecond), stream.SessionID(), stream.ID())
			}
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
	if elapsed := time.Since(tunnelStart); elapsed > clientTunnelSlowAfter {
		clog.Debugf(ctx, "agent tunnel endpoint stayed open for %s: session=%s conn=%s", elapsed.Round(time.Millisecond), stream.SessionID(), stream.ID())
	}

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
	started := time.Now()
	clog.Infof(ctx, "WatchDial called from client %s", session.SessionId)
	defer func() {
		clog.Infof(ctx, "WatchDial ended from client %s after %s: context=%v", session.SessionId, time.Since(started).Round(time.Millisecond), ctx.Err())
	}()
	drCh := make(chan *rpc.DialRequest)
	sid := tunnel.SessionID(session.SessionId)
	s.dialWatchers.Store(sid, drCh)
	defer func() {
		s.dialWatchers.Compute(sid, func(current chan *rpc.DialRequest, loaded bool) (chan *rpc.DialRequest, xsync.ComputeOp) {
			if loaded && current == drCh {
				return nil, xsync.DeleteOp
			}
			return current, xsync.CancelOp
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
	createStart := time.Now()
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
		clog.Warnf(ctx, "unable to create tunnel to client %s for id %s after %s: %v", sessionID, id, time.Since(createStart).Round(time.Millisecond), err)
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

	requestStart := time.Now()
	select {
	case <-ctx.Done():
		clog.Errorf(ctx, "unable to send DialRequest to client %s for id %s: %v", sessionID, id, ctx.Err())
		return nil, ctx.Err()
	case drCh <- &rpc.DialRequest{ConnId: []byte(id), DialTimeout: int64(dialTimeout), RoundtripLatency: int64(roundTripLatency)}:
		if sendDuration := time.Since(requestStart); sendDuration > 100*time.Millisecond {
			clog.Debugf(ctx, "Sent DialRequest to client %s for id %s after %s", sessionID, id, sendDuration)
		}
	}

	waitLog := time.NewTimer(clientTunnelSlowAfter)
	defer waitLog.Stop()
	for {
		select {
		case <-ctx.Done():
			clog.Errorf(ctx, "unable to create tunnel to client %s for id %s after %s: %v", sessionID, id, time.Since(requestStart).Round(time.Millisecond), ctx.Err())
			return nil, ctx.Err()
		case stream := <-stCh:
			if elapsed := time.Since(requestStart); elapsed > clientTunnelSlowAfter {
				clog.Warnf(ctx, "created tunnel to client %s for id %s slowly in %s", sessionID, id, elapsed.Round(time.Millisecond))
			} else {
				clog.Debugf(ctx, "Created tunnel to client %s for id %s in %s", sessionID, id, elapsed)
			}
			return stream, nil
		case <-waitLog.C:
			clog.Warnf(
				ctx,
				"still waiting for client tunnel after %s: clientSession=%s conn=%s dialTimeout=%s roundtripLatency=%s",
				time.Since(requestStart).Round(time.Millisecond),
				sessionID,
				id,
				dialTimeout,
				roundTripLatency,
			)
			waitLog.Reset(clientTunnelStillWaitingLog)
		}
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
