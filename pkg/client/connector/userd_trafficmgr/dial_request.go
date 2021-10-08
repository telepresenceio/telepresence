package userd_trafficmgr

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (tm *trafficManager) dialRequestWatcher(ctx context.Context) error {
	<-tm.startup
	// Deal with dial requests from the manager
	dialerStream, err := tm.managerClient.WatchDial(ctx, tm.sessionInfo)
	if err != nil {
		return err
	}
	tunnel.DialWaitLoop(ctx, tm.managerClient, dialerStream, tm.sessionInfo.SessionId)
	return nil
}
