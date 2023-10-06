package trafficmgr

import (
	"context"

	"go.opentelemetry.io/otel"

	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (s *session) dialRequestWatcher(ctx context.Context) error {
	return runWithRetry(ctx, s._dialRequestWatcher)
}

func (s *session) _dialRequestWatcher(ctx context.Context) (err error) {
	ctx, span := otel.Tracer("").Start(ctx, "_dialRequestWatcher")
	defer tracing.EndAndRecord(span, err)
	// Deal with dial requests from the manager
	dialerStream, err := s.managerClient.WatchDial(ctx, s.sessionInfo)
	if err != nil {
		return err
	}
	return tunnel.DialWaitLoop(ctx, s.managerClient, dialerStream, s.sessionInfo.SessionId)
}
