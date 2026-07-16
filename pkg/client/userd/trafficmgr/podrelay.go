package trafficmgr

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/telepresenceio/clog"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// podRelayApplier is implemented by the root daemon's in-process session,
// letting the relay bypass gRPC entirely when the root daemon runs in the
// same process as the user daemon.
type podRelayApplier interface {
	ApplyAgentPodsDelta(context.Context, *rootdRpc.AgentPodsDelta) error
}

// podRelayUnimplementedWarning is logged once, the only time the root daemon
// reports that it doesn't support the relay (it's old enough to self-watch
// agent pods instead).
const podRelayUnimplementedWarning = "root daemon does not support relayed agent-pod events; it watches agent pods itself"

// podRelay accumulates the manager-computed AgentPodInfo -- relayed as-is in
// both combined and legacy fallback mode, since the manager (not the client)
// computes the projection in the new design -- and relays it to the root
// daemon, either in-process (podRelayApplier) or over the WatchAgentPods
// client-streaming RPC.
type podRelay struct {
	mu       sync.Mutex
	current  map[string]*manager.AgentPodInfo
	lastSent map[string]*manager.AgentPodInfo // nil means the next send must be a full reset
	syncing  bool
	notifyCh chan struct{}
}

func newPodRelay() *podRelay {
	return &podRelay{notifyCh: make(chan struct{}, 1)}
}

// beginSync discards the accumulated state and suppresses relay pushes until
// the next apply call, at which point the push is a full reset: the upstream
// watcher (manager stream, in combined or legacy mode) is being
// re-established, and the root daemon's last snapshot may no longer be
// current by the time it's rebuilt, so it must be replaced wholesale rather
// than diffed against.
func (r *podRelay) beginSync() {
	r.mu.Lock()
	r.current = nil
	r.lastSent = nil
	r.syncing = true
	r.mu.Unlock()
}

// apply feeds a delta of manager-computed AgentPodInfo, relayed as-is,
// whether it originated from the combined stream's AgentPods field or from
// the legacy agent-pod watch chain.
func (r *podRelay) apply(upserts map[string]*manager.AgentPodInfo, removals []string) {
	r.mu.Lock()
	if r.current == nil {
		r.current = make(map[string]*manager.AgentPodInfo, len(upserts))
	}
	maps.DeltaUpdate(r.current, upserts, removals)
	r.syncing = false
	r.mu.Unlock()
	r.notify()
}

func (r *podRelay) notify() {
	select {
	case r.notifyCh <- struct{}{}:
	default:
	}
}

// nextDelta computes the AgentPodsDelta to send next, together with the
// current snapshot that commitSent must be called with after a successful
// send. ok is false when there's nothing to send: a sync is in progress, or
// nothing has changed since the last successful send.
func (r *podRelay) nextDelta() (delta *rootdRpc.AgentPodsDelta, snapshot map[string]*manager.AgentPodInfo, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.syncing {
		return nil, nil, false
	}
	if r.lastSent == nil {
		snapshot = maps.Copy(r.current)
		return &rootdRpc.AgentPodsDelta{Reset_: true, Upserts: snapshot}, snapshot, true
	}
	upserts := make(map[string]*manager.AgentPodInfo)
	for k, v := range r.current {
		if old, ok := r.lastSent[k]; !ok || !proto.Equal(old, v) {
			upserts[k] = v
		}
	}
	var removals []string
	for k := range r.lastSent {
		if _, ok := r.current[k]; !ok {
			removals = append(removals, k)
		}
	}
	if len(upserts) == 0 && len(removals) == 0 {
		return nil, nil, false
	}
	snapshot = maps.Copy(r.current)
	return &rootdRpc.AgentPodsDelta{Upserts: upserts, Removals: removals}, snapshot, true
}

// commitSent records snapshot as the state last successfully relayed.
func (r *podRelay) commitSent(snapshot map[string]*manager.AgentPodInfo) {
	r.mu.Lock()
	r.lastSent = snapshot
	r.mu.Unlock()
}

// resetSent forgets what was last relayed, so the next successful send is a
// full reset. Used after a failed send, so the root daemon (which may not
// have received that message, or may be a newly (re)connected one) is
// brought back to a known state rather than trusted to hold a partial one.
func (r *podRelay) resetSent() {
	r.mu.Lock()
	r.lastSent = nil
	r.mu.Unlock()
}

// run relays the accumulated projection to the root daemon until ctx is
// done, or the root daemon reports that it doesn't support the relay (an old
// root daemon self-watches agent pods; there is nothing more to do for this
// session, ever).
func (r *podRelay) run(ctx context.Context, s *session) error {
	interval := client.GetConfig(ctx).Grpc().WatchRetryInterval
	var stream rootdRpc.Daemon_WatchAgentPodsClient
	defer func() {
		if stream != nil {
			_, _ = stream.CloseAndRecv()
		}
	}()

	var lastGeneration uint64
	needRetry := false
	for {
		var retryCh <-chan time.Time
		if needRetry {
			retryCh = time.After(interval)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-r.notifyCh:
		case <-retryCh:
		}
		needRetry = false

		rd, generation := s.getRootDaemon()
		if rd == nil {
			needRetry = true
			continue
		}
		if generation != lastGeneration {
			// A (re)connected root daemon holds none of our state; the next
			// send must be a full reset, and any stream referencing the
			// previous connection is stale.
			stream = nil
			r.resetSent()
			lastGeneration = generation
		}

		if applier, ok := rd.(podRelayApplier); ok {
			delta, snapshot, ok := r.nextDelta()
			if !ok {
				continue
			}
			if err := applier.ApplyAgentPodsDelta(ctx, delta); err != nil {
				if status.Code(err) == codes.Unimplemented {
					clog.Warnf(ctx, podRelayUnimplementedWarning)
					return nil
				}
				r.resetSent()
				needRetry = true
				continue
			}
			r.commitSent(snapshot)
			continue
		}

		if stream == nil {
			var err error
			stream, err = rd.WatchAgentPods(ctx)
			if err != nil {
				if status.Code(err) == codes.Unimplemented {
					clog.Warnf(ctx, podRelayUnimplementedWarning)
					return nil
				}
				r.resetSent()
				needRetry = true
				continue
			}
		}

		delta, snapshot, ok := r.nextDelta()
		if !ok {
			continue
		}
		if sendErr := stream.Send(delta); sendErr != nil {
			failed := stream
			stream = nil
			if errors.Is(sendErr, io.EOF) {
				// Send does not surface the server's actual status when the
				// stream ended from the server side (e.g. Unimplemented):
				// grpc-go only returns io.EOF there, and the real status
				// must be retrieved by finishing the call.
				_, sendErr = failed.CloseAndRecv()
			}
			if status.Code(sendErr) == codes.Unimplemented {
				clog.Warnf(ctx, podRelayUnimplementedWarning)
				return nil
			}
			r.resetSent()
			needRetry = true
			continue
		}
		r.commitSent(snapshot)
	}
}
