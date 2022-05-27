package trafficmgr

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (tm *TrafficManager) dialRequestWatcher(ctx context.Context) error {
	// Deal with dial requests from the manager
	dialerStream, err := tm.managerClient.WatchDial(ctx, tm.sessionInfo)
	if err != nil {
		return err
	}
	return tunnel.DialWaitLoop(ctx, tm.managerClient, dialerStream, tm.sessionInfo.SessionId)
}
