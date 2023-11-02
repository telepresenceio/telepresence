package trafficmgr

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (s *session) dialRequestWatcher(ctx context.Context) error {
	return runWithRetry(ctx, s._dialRequestWatcher)
}

func (s *session) _dialRequestWatcher(ctx context.Context) error {
	// Deal with dial requests from the manager
	dialerStream, err := s.managerClient.WatchDial(ctx, s.sessionInfo)
	if err != nil {
		return err
	}
	return tunnel.DialWaitLoop(ctx, tunnel.ManagerProvider(s.managerClient), dialerStream, s.sessionInfo.SessionId)
}
