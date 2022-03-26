package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// getCurrentAgents returns a copy of the current agent snapshot
// Deprecated
func (tm *TrafficManager) getCurrentAgents() []*manager.AgentInfo {
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
// Deprecated
func (tm *TrafficManager) getCurrentAgentsInNamespace(ns string) map[string]*manager.AgentInfo {
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

func (tm *TrafficManager) setCurrentAgents(agents []*manager.AgentInfo) {
	tm.currentAgentsLock.Lock()
	tm.currentAgents = agents
	tm.currentAgentsLock.Unlock()
}

func (tm *TrafficManager) notifyAgentWatchers(ctx context.Context, agents []*manager.AgentInfo) {
	tm.currentAgentsLock.Lock()
	aiws := tm.agentInitWaiters
	tm.agentInitWaiters = nil
	tm.currentAgentsLock.Unlock()
	for _, aiw := range aiws {
		close(aiw)
	}

	// Notify waiters for agents
	for _, agent := range agents {
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

func (tm *TrafficManager) watchAgentsNS(ctx context.Context) error {
	// Cancel this watcher whenever the set of active namespaces change
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tm.addActiveNamespaceListener(func() {
		cancel()
	})

	nss := tm.getActiveNamespaces(ctx)
	if len(nss) == 0 {
		// Not much point in watching for nothing, so just wait until
		// the set of namespaces change. Returning nil here means that
		// we want a restart unless the caller too is cancelled
		<-ctx.Done()
		return nil
	}

	dlog.Debugf(ctx, "start watchAgentNS %v", nss)
	defer dlog.Debugf(ctx, "end watchAgentNS %v", nss)

	var opts []grpc.CallOption
	cfg := client.GetConfig(ctx)
	if !cfg.Grpc.MaxReceiveSize.IsZero() {
		if mz, ok := cfg.Grpc.MaxReceiveSize.AsInt64(); ok {
			opts = append(opts, grpc.MaxCallRecvMsgSize(int(mz)))
		}
	}

	wm := "WatchAgentsNS"
	stream, err := tm.managerClient.WatchAgentsNS(ctx, &manager.AgentsRequest{
		Session:    tm.session(),
		Namespaces: nss,
	}, opts...)

	if err != nil {
		if ctx.Err() == nil {
			err = fmt.Errorf("manager.WatchAgentsNS dial: %w", err)
		} else {
			err = nil
		}
		tm.setCurrentAgents(nil)
		return err
	}

	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			if gErr, ok := status.FromError(err); ok && gErr.Code() == codes.Unimplemented {
				// Fall back to old method of watching all namespaces
				wm = "WatchAgents"
				err = tm.watchAgents(ctx, opts)
			}
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				dlog.Errorf(ctx, "manager.%s recv: %v", wm, err)
			} else {
				err = nil
			}
			tm.setCurrentAgents(nil)
			return err
		}
		tm.setCurrentAgents(snapshot.Agents)
		tm.notifyAgentWatchers(ctx, snapshot.Agents)
	}
	return nil
}

func (tm *TrafficManager) watchAgents(ctx context.Context, opts []grpc.CallOption) error {
	stream, err := tm.managerClient.WatchAgents(ctx, tm.session(), opts...)
	if err != nil {
		return err
	}

	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			return err
		}
		tm.setCurrentAgents(snapshot.Agents)
		tm.notifyAgentWatchers(ctx, snapshot.Agents)
	}
	return nil
}

func (tm *TrafficManager) agentInfoWatcher(ctx context.Context) error {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		if err := tm.watchAgentsNS(ctx); err != nil {
			dlog.Error(ctx, err)
			dtime.SleepWithContext(ctx, backoff)
			backoff *= 2
			if backoff > 3*time.Second {
				backoff = 3 * time.Second
			}
		}
	}
	return nil
}

// Deprecated
func (tm *TrafficManager) addAgent(
	c context.Context,
	svcProps *serviceProps,
	agentImageName string,
	telepresenceAPIPort uint16,
) (map[string]string, *rpc.InterceptResult) {
	workload := svcProps.workload
	agentName := workload.GetName()
	namespace := workload.GetNamespace()
	_, kind, err := tm.EnsureAgent(c, workload, svcProps, agentImageName, telepresenceAPIPort)
	if err != nil {
		if err == agentNotFound {
			return nil, &rpc.InterceptResult{
				Error:     rpc.InterceptError_NOT_FOUND,
				ErrorText: agentName,
			}
		}
		dlog.Error(c, err)
		return nil, &rpc.InterceptResult{
			Error:     rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}

	dlog.Infof(c, "Waiting for agent for %s %s.%s", kind, agentName, namespace)
	ai, err := tm.waitForAgent(c, agentName, namespace)
	if err != nil {
		dlog.Error(c, err)
		return nil, &rpc.InterceptResult{
			Error:     rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}
	dlog.Infof(c, "Agent found or created for %s %s.%s", kind, agentName, namespace)
	result := svcProps.interceptResult()
	return ai.Environment, result
}

// Deprecated
func (tm *TrafficManager) waitForAgent(ctx context.Context, name, namespace string) (*manager.AgentInfo, error) {
	fullName := name + "." + namespace
	waitCh := make(chan *manager.AgentInfo)
	tm.agentWaiters.Store(fullName, waitCh)
	tm.wlWatcher.ensureStarted(ctx, namespace, nil)
	defer tm.agentWaiters.Delete(fullName)

	// Agent may already exist.
	for _, agent := range tm.getCurrentAgentsInNamespace(namespace) {
		if agent.Name == name {
			return agent, nil
		}
	}

	ctx, cancel := client.GetConfig(ctx).Timeouts.TimeoutContext(ctx, client.TimeoutAgentInstall) // installing a new agent can take some time
	defer cancel()

	select {
	case <-ctx.Done():
		return nil, client.CheckTimeout(ctx, fmt.Errorf("waiting for agent %q to be present", fullName))
	case agent := <-waitCh:
		return agent, nil
	}
}
