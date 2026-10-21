package mutator

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/puzpuzpuz/xsync/v4"

	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	evictionRequeueBase = time.Second
	evictionRequeueMax  = 30 * time.Second
	evictionMaxFailures = 10
	evictionWorkers     = 2

	// evictionDeferralNotice is how long an agent update may be deferred before that is logged at
	// info level.
	evictionDeferralNotice = time.Minute

	// evictionDeferralCeiling is how long an agent update may be deferred before the workload is
	// restarted through its controller instead.
	evictionDeferralCeiling = 5 * time.Minute

	// evictionBudgetGrace is how long a PodDisruptionBudget may keep refusing evictions before the
	// workload is restarted through its controller instead. The budget's status trails pod readiness,
	// so a refusal right after a batch is normally transient.
	evictionBudgetGrace = 30 * time.Second
)

type evictionQueue = workqueue.TypedRateLimitingInterface[WorkloadKey]

func newEvictionQueue(base, maxDelay time.Duration) evictionQueue {
	return workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[WorkloadKey](base, maxDelay))
}

// requestEviction records the agent config the pods of wl must carry and schedules a reconcile pass.
// It also hides the agents of the pods that must be replaced.
func (c *configWatcher) requestEviction(ctx context.Context, wl k8sapi.Workload, wanted *agentconfig.Sidecar) {
	key := workloadKeyOf(wl)
	clog.Debugf(ctx, "Requesting replacement of pods with agent config mismatch for %s", wl)
	state := c.lockEvictionState(key)
	if !state.requested {
		state.requested = true
		c.pendingRequests.Add(1)
	}
	state.wanted = wanted
	if pods, err := workloadPods(ctx, wl); err == nil {
		c.markPendingEviction(pods, podsWithAgentConfigMismatch(ctx, pods, wanted))
	} else {
		clog.Debugf(ctx, "Unable to list pods of %s: %v", wl, err)
	}
	state.Unlock()
	c.evictionQueue.Add(key)
}

// markPendingEviction hides the agents of the mismatched pods until a pass evicts them, and shows
// the agents of the other pods again if an earlier pass hid them.
func (c *configWatcher) markPendingEviction(pods, mismatched []*core.Pod) {
	for _, pod := range pods {
		pending := slices.Contains(mismatched, pod)
		c.inactivePods.Compute(pod.UID, func(v inactivation, loaded bool) (inactivation, xsync.ComputeOp) {
			switch {
			case loaded && v.deleted:
				return v, xsync.CancelOp
			case pending:
				return inactivation{Time: time.Now(), pending: true}, xsync.UpdateOp
			case loaded && v.pending:
				return v, xsync.DeleteOp
			default:
				return v, xsync.CancelOp
			}
		})
	}
}

// finishRequest clears the request. The caller holds the state's lock.
func (c *configWatcher) finishRequest(state *workloadEvictionState) {
	if state.requested {
		state.requested = false
		c.pendingRequests.Add(-1)
	}
	state.wanted = nil
	state.rolloutGeneration = 0
	state.failures = 0
	state.resetDeferral()
}

func (c *configWatcher) startEvictionWorkers(ctx context.Context) {
	for range evictionWorkers {
		go c.runEvictionWorker(ctx)
	}
	go func() {
		<-ctx.Done()
		c.evictionQueue.ShutDown()
	}()
}

func (c *configWatcher) runEvictionWorker(ctx context.Context) {
	q := c.evictionQueue
	for {
		key, shutdown := q.Get()
		if shutdown {
			return
		}
		pending, err := c.reconcile(ctx, key)
		switch {
		case err != nil && ctx.Err() == nil:
			clog.Warnf(ctx, "Agent pod eviction for %s will be retried: %v", key, err)
			q.AddRateLimited(key)
		case err == nil && pending:
			q.AddRateLimited(key)
		default:
			q.Forget(key)
		}
		q.Done(key)
	}
}

// reconcile runs one eviction pass for the workload and reports whether more passes are needed.
func (c *configWatcher) reconcile(ctx context.Context, key WorkloadKey) (pending bool, err error) {
	state := c.lockEvictionState(key)
	defer state.Unlock()
	if !state.requested {
		return false, nil
	}
	pending, err = c.reconcileLocked(ctx, key, state)
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return false, err
		}
		state.failures++
		if state.failures >= evictionMaxFailures {
			clog.Errorf(ctx, "Giving up on agent pod eviction for %s: %v", key, err)
			c.finishRequest(state)
			return false, nil
		}
	case !pending:
		c.finishRequest(state)
	default:
		state.failures = 0
	}
	return pending, err
}

func (c *configWatcher) reconcileLocked(ctx context.Context, key WorkloadKey, state *workloadEvictionState) (bool, error) {
	wl, err := evictionWorkload(ctx, key)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			clog.Debugf(ctx, "Agent pod eviction for %s ends because the workload is gone", key)
			return false, nil
		}
		return true, err
	}
	pods, err := workloadPods(ctx, wl)
	if err != nil {
		return true, err
	}
	mismatched := podsWithAgentConfigMismatch(ctx, pods, state.wanted)
	c.markPendingEviction(pods, mismatched)
	if len(mismatched) == 0 {
		clog.Debugf(ctx, "All pods of %s carry the wanted agent config", wl)
		return false, nil
	}
	reason, ownRollout, budget := state.pacing(wl, pods)
	if reason != "" {
		if !c.deferEviction(ctx, state, wl, reason, !ownRollout) {
			return true, nil
		}
		if err = triggerRollout(ctx, wl, mismatched[0]); err != nil {
			return true, err
		}
		state.rolloutGeneration = wl.GetGeneration()
		state.resetDeferral()
		return true, nil
	}
	evicted, refused, err := c.evictBatch(ctx, mismatched, budget)
	if err != nil {
		return true, err
	}
	if evicted > 0 {
		state.resetDeferral()
		clog.Debugf(ctx, "Evicted %d of %d pods with agent config mismatch for %s", evicted, len(mismatched), wl)
	}
	if refused == nil {
		return true, nil
	}
	if !c.budgetRefused(ctx, state, wl, refused) {
		return true, nil
	}
	if err = triggerRollout(ctx, wl, refused); err != nil {
		return true, err
	}
	state.rolloutGeneration = wl.GetGeneration()
	state.resetDeferral()
	return true, nil
}

// budgetRefused records that the disruption budget refused to evict pod and reports whether it has
// done so for long enough for the workload to be restarted through its controller instead.
func (c *configWatcher) budgetRefused(ctx context.Context, state *workloadEvictionState, wl k8sapi.Workload, pod *core.Pod) bool {
	now := time.Now()
	if state.budgetRefusedSince.IsZero() {
		state.budgetRefusedSince = now
	}
	refusing := now.Sub(state.budgetRefusedSince)
	if refusing >= c.budgetGrace {
		clog.Infof(ctx, "The disruption budget of %s has refused evictions for %s; restarting it through its controller",
			wl, refusing.Round(time.Second))
		return true
	}
	c.deferEviction(ctx, state, wl, fmt.Sprintf("the disruption budget refused to evict pod %s", pod.Name), false)
	return false
}

// pacing reports why the pass must wait, or how many pods it may evict. ownRollout is true when the
// wait is for a rollout that an earlier pass triggered.
func (s *workloadEvictionState) pacing(wl k8sapi.Workload, pods []*core.Pod) (reason string, ownRollout bool, budget int) {
	if s.rolloutGeneration != 0 {
		if wl.GetGeneration() <= s.rolloutGeneration || workloadRolloutInProgress(wl) {
			return "the rollout it triggered has not completed", true, 0
		}
		s.rolloutGeneration = 0
	}
	if workloadRolloutInProgress(wl) {
		return "a rollout is in progress", false, 0
	}
	unavailable := unavailablePods(wl, pods)
	budget = allowedUnavailable(wl) - unavailable
	if budget <= 0 {
		return fmt.Sprintf("%d of %d replicas are unavailable", unavailable, k8sapi.DesiredReplicas(wl)), false, 0
	}
	return "", false, budget
}

func (s *workloadEvictionState) resetDeferral() {
	s.deferredSince = time.Time{}
	s.deferralNoticed = false
	s.budgetRefusedSince = time.Time{}
}

// deferEviction records that the pass made no progress and reports whether the deferral has lasted
// long enough for the workload to be restarted through its controller instead.
func (c *configWatcher) deferEviction(ctx context.Context, state *workloadEvictionState, wl k8sapi.Workload, reason string, allowRestart bool) bool {
	now := time.Now()
	if state.deferredSince.IsZero() {
		state.deferredSince = now
	}
	waited := now.Sub(state.deferredSince)
	switch {
	case allowRestart && waited >= c.deferralCeiling:
		clog.Infof(ctx, "Agent update for %s was deferred for %s because %s; restarting it through its controller",
			wl, waited.Round(time.Second), reason)
		return true
	case waited >= c.deferralNotice && !state.deferralNoticed:
		state.deferralNoticed = true
		clog.Infof(ctx, "Deferring agent update for %s because %s", wl, reason)
	default:
		clog.Debugf(ctx, "Deferring agent update for %s because %s", wl, reason)
	}
	return false
}

// watchPods wakes up the workloads with outstanding eviction requests when one of their pods changes
// readiness, is created, or goes away.
func (c *configWatcher) watchPods(ctx context.Context, ix cache.SharedIndexInformer) (cache.ResourceEventHandlerRegistration, error) {
	return ix.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			c.podChanged(ctx, obj)
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldPod, oldOK := oldObj.(*core.Pod)
			newPod, newOK := newObj.(*core.Pod)
			if oldOK && newOK &&
				podIsReady(oldPod) == podIsReady(newPod) &&
				oldPod.Status.Phase == newPod.Status.Phase &&
				oldPod.DeletionTimestamp.IsZero() == newPod.DeletionTimestamp.IsZero() {
				return
			}
			c.podChanged(ctx, newObj)
		},
		DeleteFunc: func(obj any) {
			c.podChanged(ctx, obj)
		},
	})
}

func (c *configWatcher) podChanged(ctx context.Context, obj any) {
	if c.pendingRequests.Load() == 0 {
		return
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	pod, ok := obj.(*core.Pod)
	if !ok {
		return
	}
	wl, err := podOwnerWorkload(ctx, pod, managerutil.GetEnv(ctx).EnabledWorkloadKinds)
	if err != nil {
		return
	}
	key := workloadKeyOf(wl)
	state, ok := c.evictionStates.Load(key)
	if !ok {
		return
	}
	state.Lock()
	requested := state.requested
	state.Unlock()
	if requested {
		c.evictionQueue.Forget(key)
		c.evictionQueue.Add(key)
	}
}
