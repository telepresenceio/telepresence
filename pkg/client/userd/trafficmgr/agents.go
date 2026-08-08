package trafficmgr

import (
	"context"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// agentPod is the client's internal record of one traffic-agent pod, holding
// exactly what its consumers read. Built from AgentPodInfo in combined mode
// and projected from AgentInfo in legacy fallback mode.
type agentPod struct {
	workload  string
	namespace string
	podName   string
	version   string
	nodeAgent bool
}

// agentPodFromPodInfo builds an agentPod from the manager's AgentPodInfo
// projection (combined mode).
func agentPodFromPodInfo(ap *manager.AgentPodInfo) agentPod {
	return agentPod{
		workload:  ap.WorkloadName,
		namespace: ap.Namespace,
		podName:   ap.PodName,
		version:   ap.Version,
		nodeAgent: ap.NodeAgent,
	}
}

// agentPodFromAgentInfo builds an agentPod from the manager's AgentInfo
// (legacy fallback mode).
func agentPodFromAgentInfo(ai *manager.AgentInfo) agentPod {
	return agentPod{
		workload:  ai.Name,
		namespace: ai.Namespace,
		podName:   ai.PodName,
		version:   ai.Version,
		nodeAgent: ai.NodeAgent,
	}
}

// watchAgentsLoop drives the legacy WatchAgentsDelta/WatchAgents watcher, used
// only by watchSessionEventsLegacy. Its snapshots are scoped to the connected
// namespace, so the covered predicate it feeds handleAgentPodSnapshot only
// ever declares that one namespace covered.
func (s *session) watchAgentsLoop(ctx context.Context) error {
	covered := func(namespace string) bool { return namespace == s.Namespace }
	snapMap := make(map[string]*manager.AgentInfo)
	toPods := func() []agentPod {
		pods := make([]agentPod, 0, len(snapMap))
		for _, ai := range snapMap {
			pods = append(pods, agentPodFromAgentInfo(ai))
		}
		return pods
	}
	err := watcher.WatchWithRetry(ctx, "WatchAgentsDelta", client.GetConfig(ctx).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentInfoDelta], error) {
			return s.ManagerClient().WatchAgentsDelta(ctx, s.SessionInfo())
		},
		func(delta *manager.AgentInfoDelta) error {
			maps.DeltaUpdate(snapMap, delta.Upserts, delta.Removals)
			s.handleAgentPodSnapshot(ctx, toPods(), covered)
			return nil
		}, func() error {
			clear(snapMap)
			return nil
		})

	if err != nil && status.Code(err) == codes.Unimplemented {
		clog.Warnf(ctx, "WatchAgentsDelta is not implemented by the traffic-manager, falling back to WatchAgents and full snapshots")
		err = watcher.WatchWithRetry(ctx, "WatchAgents", client.GetConfig(ctx).Grpc().WatchRetryInterval,
			func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentInfoSnapshot], error) {
				return s.ManagerClient().WatchAgents(ctx, s.SessionInfo())
			},
			func(snapshot *manager.AgentInfoSnapshot) error {
				pods := make([]agentPod, len(snapshot.Agents))
				for i, ai := range snapshot.Agents {
					pods[i] = agentPodFromAgentInfo(ai)
				}
				s.handleAgentPodSnapshot(ctx, pods, covered)
				return nil
			}, nil)
	}
	// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are canceled correctly.
	s.handleAgentPodSnapshot(ctx, nil, covered)
	return err
}

// ingestPodDecision is the outcome of matching one ingest's key and current
// pod name against a fresh agent-pod snapshot.
type ingestPodDecision int

const (
	// ingestPodNoMatch: no pod in the snapshot matches the ingest's workload
	// and namespace. The ingest is left untouched this round; cancelUnwanted
	// decides its fate based on whether its namespace is covered.
	ingestPodNoMatch ingestPodDecision = iota
	// ingestPodKeepAlive: the ingest's current pod is still among the
	// matching pods; its mounts/port-forwards stay alive.
	ingestPodKeepAlive
	// ingestPodReplaced: matching pods exist, but not under the ingest's
	// current pod name; its agent must be refetched.
	ingestPodReplaced
)

// decideIngestPod matches key (workload+namespace; container matching is not
// needed since membership was already validated when the ingest was
// created) and the ingest's current pod name against pods, a fresh
// agent-pod snapshot. matching is the set of pods sharing key's workload and
// namespace -- non-empty exactly when the decision isn't ingestPodNoMatch.
func decideIngestPod(pods []agentPod, key ingestKey, currentPodName string) (decision ingestPodDecision, matching []agentPod) {
	for _, ap := range pods {
		if ap.workload == key.workload && ap.namespace == key.namespace {
			matching = append(matching, ap)
		}
	}
	if len(matching) == 0 {
		return ingestPodNoMatch, nil
	}
	if slices.ContainsFunc(matching, func(ap agentPod) bool { return ap.podName == currentPodName }) {
		return ingestPodKeepAlive, matching
	}
	return ingestPodReplaced, matching
}

// selectReplacementAgent picks the AgentInfo to adopt for a replaced ingest
// pod: the first candidate whose PodName is among matching, or (when none
// matches, which should not normally happen) the first candidate. Returns
// nil when candidates is empty.
func selectReplacementAgent(candidates []*manager.AgentInfo, matching []agentPod) *manager.AgentInfo {
	for _, cand := range candidates {
		if slices.ContainsFunc(matching, func(ap agentPod) bool { return ap.podName == cand.PodName }) {
			return cand
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return nil
}

// handleAgentPodSnapshot updates the internal agent-pod cache and drives the
// ingest keep-alive/replacement lifecycle from a fresh agent-pod snapshot,
// then cancels port-forwards and mounts left over from pods that are no
// longer present, scoped to the namespaces this snapshot actually covers.
func (s *session) handleAgentPodSnapshot(ctx context.Context, pods []agentPod, covered func(namespace string) bool) {
	s.ingestTracker.initSnapshot()
	s.setCurrentAgentPods(pods)

	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		decision, matching := decideIngestPod(pods, key, ig.PodName)
		switch decision {
		case ingestPodNoMatch:
			// Not covered by this snapshot; cancelUnwanted below decides its fate.
			return true
		case ingestPodKeepAlive:
			s.startIngestPodAccess(ctx, ig, false)
			return true
		}

		// ingestPodReplaced: the pod backing this ingest is gone; refetch its
		// AgentInfo so replacement failover works for cross-namespace ingests too.
		timeoutCtx, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutTrafficManagerAPI)
		as, err := s.ManagerClient().EnsureAgent(timeoutCtx, &manager.EnsureAgentRequest{
			Session:      s.sessionInfo,
			Name:         key.workload,
			Namespace:    key.namespace,
			NodeAgent:    ig.NodeAgent,
			WorkloadKind: ig.Kind,
		})
		cancel()
		if err != nil {
			clog.Errorf(ctx, "failed to refetch agent for ingest %s: %v", key, err)
			return true
		}
		ai := selectReplacementAgent(as.Agents, matching)
		if ai == nil {
			clog.Errorf(ctx, "EnsureAgent returned no agents for ingest %s", key)
			return true
		}
		if _, ok := ai.Containers[ig.container]; !ok {
			clog.Errorf(ctx, "workload %s has no container named %s after replacement", key.workload, ig.container)
			return true
		}
		if err := s.translateContainerEnv(ctx, ai, ig.container); err != nil {
			clog.Errorf(ctx, "failed to translate container env: %v", err)
			return true
		}
		ig.AgentInfo = ai
		s.startIngestPodAccess(ctx, ig, false)
		return true
	})
	s.ingestTracker.cancelUnwanted(ctx, covered)
}

func (s *session) getCurrentAgentPods() []agentPod {
	s.currentInterceptsLock.Lock()
	pods := s.currentAgentPods
	s.currentInterceptsLock.Unlock()
	return pods
}

func (s *session) setCurrentAgentPods(pods []agentPod) {
	s.currentInterceptsLock.Lock()
	s.currentAgentPods = pods
	s.currentInterceptsLock.Unlock()
}
