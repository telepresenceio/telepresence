package userd_trafficmgr

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// getCurrentAgents returns a copy of the current agent snapshot
func (tm *trafficManager) getCurrentAgents() []*manager.AgentInfo {
	// Copy the current snapshot
	tm.currentAgentsLock.Lock()
	agents := make([]*manager.AgentInfo, len(tm.currentAgents))
	for i, ii := range tm.currentAgents {
		agents[i] = proto.Clone(ii).(*manager.AgentInfo)
	}
	tm.currentAgentsLock.Unlock()
	return agents
}

// getCurrentAgentsInNamespace returns a map of agents matching the given namespace from the current agent snapshot.
// The map contains the first agent for each name found. Agents from replicas of the same workload are ignored.
func (tm *trafficManager) getCurrentAgentsInNamespace(ns string) map[string]*manager.AgentInfo {
	// Copy the current snapshot
	tm.currentAgentsLock.Lock()
	agents := make(map[string]*manager.AgentInfo)
	for _, ii := range tm.currentAgents {
		if ii.Namespace == ns {
			// There may be any number or replicas of the agent. Avoid cloning all of them.
			if _, ok := agents[ii.Name]; !ok {
				agents[ii.Name] = proto.Clone(ii).(*manager.AgentInfo)
			}
		}
	}
	tm.currentAgentsLock.Unlock()
	return agents
}

func (tm *trafficManager) setCurrentAgents(agents []*manager.AgentInfo) {
	tm.currentAgentsLock.Lock()
	tm.currentAgents = agents
	tm.currentAgentsLock.Unlock()
}

func (tm *trafficManager) agentInfoWatcher(ctx context.Context) error {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		<-tm.startup
		stream, err := tm.managerClient.WatchAgents(ctx, tm.session())
		if err != nil {
			err = fmt.Errorf("manager.WatchAgents dial: %w", err)
		}
		for err == nil && ctx.Err() == nil {
			if snapshot, err := stream.Recv(); err != nil {
				if ctx.Err() == nil {
					dlog.Errorf(ctx, "manager.WatchAgents recv: %v", err)
					break
				}
			} else {
				tm.setCurrentAgents(snapshot.Agents)

				// Notify waiters for agents
				for _, agent := range snapshot.Agents {
					fullName := agent.Name + "." + agent.Namespace
					if chUt, loaded := tm.agentWaiters.LoadAndDelete(fullName); loaded {
						if ch, ok := chUt.(chan *manager.AgentInfo); ok {
							dlog.Debugf(ctx, "wait status: agent %s arrived", fullName)
							ch <- agent
							close(ch)
						}
					}
				}
			}
		}

		dtime.SleepWithContext(ctx, backoff)
		backoff *= 2
		if backoff > 3*time.Second {
			backoff = 3 * time.Second
		}
	}
	return nil
}
