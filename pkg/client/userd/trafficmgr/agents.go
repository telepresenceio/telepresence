package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func (s *session) watchAgentsLoop(ctx context.Context) error {
	stream, err := s.managerClient.WatchAgents(ctx, s.SessionInfo())
	if err != nil {
		return fmt.Errorf("manager.WatchAgents: %w", err)
	}
	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are canceled correctly.
			s.handleAgentSnapshot(ctx, nil)
			if ctx.Err() != nil || errors.Is(err, io.EOF) || grpcStatus.Code(err) == grpcCodes.NotFound {
				// Normal termination
				return nil
			}
			return fmt.Errorf("manager.WatchAgents recv: %w", err)
		}
		s.handleAgentSnapshot(ctx, snapshot.Agents)
	}
	return nil
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
				err := s.translateContainerEnv(ai, ig.container)
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
