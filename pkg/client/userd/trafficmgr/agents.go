package trafficmgr

import (
	"context"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

func (s *session) watchAgentsLoop(ctx context.Context) error {
	snapMap := make(map[string]*manager.AgentInfo)
	err := watcher.WatchWithRetry(ctx, "WatchAgentsDelta", client.GetConfig(ctx).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentInfoDelta], error) {
			return s.ManagerClient().WatchAgentsDelta(ctx, s.SessionInfo())
		},
		func(delta *manager.AgentInfoDelta) error {
			maps.DeltaUpdate(snapMap, delta.Upserts, delta.Removals)
			s.handleAgentSnapshot(ctx, maps.Values(snapMap))
			return nil
		}, func() error {
			clear(snapMap)
			return nil
		})

	if err != nil && status.Code(err) == codes.Unimplemented {
		dlog.Warnf(ctx, "WatchAgentsDelta is not implemented by the traffic-manager, falling back to WatchAgents and full snapshots")
		err = watcher.WatchWithRetry(ctx, "WatchAgents", client.GetConfig(ctx).Grpc().WatchRetryInterval,
			func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentInfoSnapshot], error) {
				return s.ManagerClient().WatchAgents(ctx, s.SessionInfo())
			},
			func(snapshot *manager.AgentInfoSnapshot) error {
				s.handleAgentSnapshot(ctx, snapshot.Agents)
				return nil
			}, nil)
	}
	// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are canceled correctly.
	s.handleAgentSnapshot(ctx, nil)
	return err
}

func (s *session) handleAgentSnapshot(ctx context.Context, infos []*manager.AgentInfo) {
	s.ingestTracker.initSnapshot()
	s.setCurrentAgents(infos)

	// infoForKey returns the AgentInfos that matches the ingestKey
	infosForKey := func(key ingestKey) (ais []*manager.AgentInfo) {
		for _, info := range infos {
			if info.Name == key.workload {
				for cn := range info.Containers {
					if cn == key.container {
						ais = append(ais, info)
					}
				}
			}
		}
		return ais
	}

	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		ais := infosForKey(key)
		if len(ais) > 0 {
			if slices.IndexFunc(ais, func(cai *manager.AgentInfo) bool { return cai.PodName == ig.PodName }) < 0 {
				// The pod selected for the ingest is no longer active, so replace it.
				ai := ais[0]
				err := s.translateContainerEnv(ctx, ai, ig.container)
				if err != nil {
					dlog.Errorf(ctx, "failed to translate container env: %v", err)
				}
				ig.AgentInfo = ai
			}
			s.ingestTracker.start(ig.podAccess(s.rootDaemon))
		}
		return true
	})
	s.ingestTracker.cancelUnwanted(ctx)
}

func (s *session) getCurrentAgents() []*manager.AgentInfo {
	s.currentInterceptsLock.Lock()
	agents := s.currentAgents
	s.currentInterceptsLock.Unlock()
	return agents
}

func (s *session) setCurrentAgents(agents []*manager.AgentInfo) {
	s.currentInterceptsLock.Lock()
	s.currentAgents = agents
	s.currentInterceptsLock.Unlock()
}
