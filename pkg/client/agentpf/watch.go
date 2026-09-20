package agentpf

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	tpClient "github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
)

// WatchPods runs the legacy agent-pod watcher chain against rmc, trying
// WatchAgentPodsInNamespacesDelta, then WatchAgentPodsDelta, then the
// full-snapshot WatchAgentPods, falling back one level on Unimplemented.
// onDelta is invoked for every received delta (for the full-snapshot level,
// each snapshot is delivered as onReset followed by onDelta with all agents
// as upserts). onReset is invoked whenever accumulated state must be
// discarded: before a stream is re-established after failure, and when the
// chain falls back a level. onNarrowed is invoked (if non-nil) when a
// fallback narrows the watch scope to just the connected namespace.
func WatchPods(
	ctx context.Context,
	rmc manager.ManagerClient,
	session *manager.SessionInfo,
	namespaces []string,
	connectedNamespace string,
	onDelta func(upserts map[string]*manager.AgentPodInfo, removals []string) error,
	onReset func() error,
	onNarrowed func([]string),
) error {
	return WatchPodsWithClient(ctx, func() manager.ManagerClient { return rmc }, session, namespaces, connectedNamespace, onDelta, onReset, onNarrowed)
}

// WatchPodsWithClient is like WatchPods, but calls managerClient for each new
// stream so a caller that repaired its manager transport can bind retries to
// the replacement connection.
func WatchPodsWithClient(
	ctx context.Context,
	managerClient func() manager.ManagerClient,
	session *manager.SessionInfo,
	namespaces []string,
	connectedNamespace string,
	onDelta func(upserts map[string]*manager.AgentPodInfo, removals []string) error,
	onReset func() error,
	onNarrowed func([]string),
) error {
	retryInterval := tpClient.GetConfig(ctx).Grpc().WatchRetryInterval
	err := watcher.WatchWithRetry(ctx, "WatchAgentPodsInNamespacesDelta", retryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentPodInfoDelta], error) {
			clog.Debugf(ctx, "WatchAgentPodsInNamespacesDelta starting")
			return managerClient().WatchAgentPodsInNamespacesDelta(ctx, &manager.AgentsRequest{Session: session, Namespaces: namespaces})
		},
		func(delta *manager.AgentPodInfoDelta) error {
			clog.Debugf(ctx, "WatchAgentPodsInNamespacesDelta received %d upserts, %d removals", len(delta.Upserts), len(delta.Removals))
			return onDelta(delta.Upserts, delta.Removals)
		}, onReset)
	if err == nil || status.Code(err) != codes.Unimplemented {
		return err
	}

	// Older traffic-manager. Fall back to watching agents in the connected namespace.
	if onReset != nil {
		if err = onReset(); err != nil {
			return err
		}
	}
	if onNarrowed != nil {
		onNarrowed([]string{connectedNamespace})
	}
	clog.Warnf(ctx, "WatchAgentPodsInNamespacesDelta is not implemented by the traffic-manager, falling back to WatchAgentPodsDelta in namespace %s", connectedNamespace)
	err = watcher.WatchWithRetry(ctx, "WatchAgentPodsDelta", retryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentPodInfoDelta], error) {
			clog.Debugf(ctx, "WatchAgentPodsDelta starting")
			return managerClient().WatchAgentPodsDelta(ctx, session)
		},
		func(delta *manager.AgentPodInfoDelta) error {
			clog.Debugf(ctx, "WatchAgentPodsDelta received %d upserts, %d removals", len(delta.Upserts), len(delta.Removals))
			return onDelta(delta.Upserts, delta.Removals)
		}, onReset)
	if err == nil || status.Code(err) != codes.Unimplemented {
		return err
	}

	clog.Warnf(ctx, "WatchAgentPodsDelta is not implemented by the traffic-manager, falling back to WatchAgentPods and full snapshots")
	if onReset != nil {
		if err = onReset(); err != nil {
			return err
		}
	}
	return watcher.WatchWithRetry(ctx, "WatchAgentPods", retryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentPodInfoSnapshot], error) {
			clog.Debugf(ctx, "No delta support in traffic-manager, starting WatchAgentPods instead")
			return managerClient().WatchAgentPods(ctx, session)
		},
		func(snapshot *manager.AgentPodInfoSnapshot) error {
			if onReset != nil {
				if err := onReset(); err != nil {
					return err
				}
			}
			upserts := make(map[string]*manager.AgentPodInfo, len(snapshot.Agents))
			for _, ai := range snapshot.Agents {
				upserts[ai.PodName+"."+ai.Namespace] = ai
			}
			return onDelta(upserts, nil)
		}, nil)
}
