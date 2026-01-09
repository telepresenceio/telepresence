package tunnel

import (
	"context"
	"fmt"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type StreamProvider interface {
	CreateClientStream(ctx context.Context, tag Tag, clientSessionID SessionID, id ConnID, roundTripLatency, dialTimeout time.Duration) (Stream, error)
}

type ClientStreamProvider interface {
	CreateClientStream(ctx context.Context, tag Tag, clientSessionID SessionID, id ConnID, roundTripLatency, dialTimeout time.Duration) (Stream, error)
	ReportMetrics(ctx context.Context, metrics *manager.TunnelMetrics)
	MetricsEnabled() bool
}

type TrafficManagerStreamProvider struct {
	Manager        manager.ManagerClient
	AgentSessionID SessionID
}

func (sp *TrafficManagerStreamProvider) CreateClientStream(
	ctx context.Context,
	tag Tag,
	clientSessionID SessionID,
	id ConnID,
	roundTripLatency,
	dialTimeout time.Duration,
) (Stream, error) {
	clog.Debugf(ctx, "creating tunnel to manager for id %s", id)
	ms, err := sp.Manager.Tunnel(ctx)
	if err != nil {
		return nil, fmt.Errorf("call to manager.Tunnel() failed. Id %s: %v", id, err)
	}

	s, err := NewClientStream(ctx, tag, ms, id, sp.AgentSessionID, roundTripLatency, dialTimeout)
	if err != nil {
		return nil, err
	}
	if err = s.Send(ctx, SessionMessage(clientSessionID)); err != nil {
		return nil, fmt.Errorf("unable to send client session id. Id %s: %v", id, err)
	}
	return s, nil
}
