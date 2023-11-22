package tunnel

import (
	"context"
	"fmt"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type StreamMetrics struct {
	ClientSessionID string
	IngressBytes    *CounterProbe
	EgressBytes     *CounterProbe
}

type StreamProvider interface {
	CreateClientStream(ctx context.Context, clientSessionID string, id ConnID, roundTripLatency, dialTimeout time.Duration) (Stream, error)
}

type ClientStreamProvider interface {
	CreateClientStream(ctx context.Context, clientSessionID string, id ConnID, roundTripLatency, dialTimeout time.Duration) (Stream, error)
	ReportMetrics(ctx context.Context, metrics *manager.TunnelMetrics)
}

type TrafficManagerStreamProvider struct {
	Manager        manager.ManagerClient
	AgentSessionID string
}

func (sp *TrafficManagerStreamProvider) CreateClientStream(
	ctx context.Context,
	clientSessionID string,
	id ConnID,
	roundTripLatency,
	dialTimeout time.Duration,
) (Stream, error) {
	dlog.Debugf(ctx, "creating tunnel to manager for id %s", id)
	ms, err := sp.Manager.Tunnel(ctx)
	if err != nil {
		return nil, fmt.Errorf("call to manager.Tunnel() failed. Id %s: %v", id, err)
	}

	s, err := NewClientStream(ctx, ms, id, sp.AgentSessionID, roundTripLatency, dialTimeout)
	if err != nil {
		return nil, err
	}
	if err = s.Send(ctx, SessionMessage(clientSessionID)); err != nil {
		return nil, fmt.Errorf("unable to send client session id. Id %s: %v", id, err)
	}
	return s, nil
}
