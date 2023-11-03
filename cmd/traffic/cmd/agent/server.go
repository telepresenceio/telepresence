package agent

import (
	"context"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
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

func (s *state) Tunnel(server agent.Agent_TunnelServer) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, server)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to connect stream: %v", err)
	}
	if awc, ok := s.awaitingForwards.Load(stream.SessionID()); ok {
		if awf, ok := awc.Load(stream.ID()); ok {
			awf.streamCh <- stream
			<-awf.doneCh
			return nil
		}
	}

	ingressBytes := tunnel.NewCounterProbe("FromClientBytes")
	egressBytes := tunnel.NewCounterProbe("ToClientBytes")
	endPoint := tunnel.NewDialer(stream, func() {}, ingressBytes, egressBytes)
	endPoint.Start(ctx)
	<-endPoint.Done()

	s.ReportMetrics(ctx, &rpc.TunnelMetrics{
		ClientSessionId: stream.SessionID(),
		IngressBytes:    ingressBytes.GetValue(),
		EgressBytes:     egressBytes.GetValue(),
	})
	return nil
}

func (s *state) WatchDial(session *rpc.SessionInfo, server agent.Agent_WatchDialServer) error {
	ctx := server.Context()
	dlog.Debugf(ctx, "WatchDial called from client %s", session.SessionId)
	defer dlog.Debugf(ctx, "WatchDial ended from client %s", session.SessionId)
	drCh := make(chan *rpc.DialRequest)
	s.dialWatchers.Store(session.SessionId, drCh)
	defer func() {
		s.dialWatchers.Delete(session.SessionId)
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
				dlog.Errorf(ctx, "send of DialRequest failed: %v", err)
				return nil
			}
		}
	}
}

func (s *state) CreateClientStream(ctx context.Context, sessionID string, id tunnel.ConnID, roundTripLatency, dialTimeout time.Duration) (tunnel.Stream, error) {
	dlog.Debugf(ctx, "Creating tunnel to client %s for id %s", sessionID, id)
	drCh, ok := s.dialWatchers.Load(sessionID)
	var stCh <-chan tunnel.Stream
	if ok {
		awc, _ := s.awaitingForwards.LoadOrCompute(sessionID, func() *xsync.MapOf[tunnel.ConnID, *awaitingForward] {
			return xsync.NewMapOf[tunnel.ConnID, *awaitingForward]()
		})
		aw, _ := awc.LoadOrCompute(id, func() *awaitingForward {
			return &awaitingForward{
				streamCh: make(chan tunnel.Stream),
				doneCh:   ctx.Done(),
			}
		})
		stCh = aw.streamCh
	}
	if !ok {
		dlog.Debugf(ctx, "Unable to create tunnel to client %s for id %s: no dial watcher", sessionID, id)
		return nil, nil
	}
	drCh <- &rpc.DialRequest{ConnId: []byte(id), DialTimeout: int64(dialTimeout), RoundtripLatency: int64(roundTripLatency)}

	select {
	case <-ctx.Done():
		dlog.Errorf(ctx, "unable to create tunnel to client %s for id %s: %v", sessionID, id, ctx.Done())
		return nil, ctx.Err()
	case stream := <-stCh:
		dlog.Debugf(ctx, "Created tunnel to client %s for id %s", sessionID, id)
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
		if err != nil {
			dlog.Errorf(ctx, "ReportMetrics failed: %v", err)
		}
	}()
}
