package mutator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestReconcileBatchesByAllowedUnavailability(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, wantedJSON, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	maxUnavailable := intstr.FromInt32(2)
	deployment := stableDeployment(ns, name, 6, &maxUnavailable)
	objects := []runtime.Object{deployment}
	for i := range 6 {
		objects = append(objects, agentPod(ns, fmt.Sprintf("echo-old-%d", i), name, k8sapi.DeploymentKind, staleJSON, true))
	}
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, objects...)
	f.evictionsSucceed()

	wl := k8sapi.Deployment(deployment)
	key := workloadKeyOf(wl)
	f.cw.requestEviction(f.ctx, wl, wanted)
	for pass := int32(1); pass <= 3; pass++ {
		pending, err := f.cw.reconcile(f.ctx, key)
		require.NoError(t, err)
		require.True(t, pending, "pass %d", pass)
		assert.Equal(t, 2*pass, f.evictions.Load(), "pass %d evicts one batch", pass)

		if pass < 3 {
			// Until the replacements are ready, another pass changes nothing.
			pending, err = f.cw.reconcile(f.ctx, key)
			require.NoError(t, err)
			require.True(t, pending)
			assert.Equal(t, 2*pass, f.evictions.Load(), "pass %d waits for the batch", pass)
		}
		for i := range 2 {
			f.addToStore(agentPod(ns, fmt.Sprintf("echo-new-%d-%d", pass, i), name, k8sapi.DeploymentKind, wantedJSON, true))
		}
	}
	pending, err := f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	assert.False(t, pending, "every pod carries the wanted config")
	assert.Equal(t, int32(6), f.evictions.Load())
	assert.Zero(t, f.patches("deployments"), "no rollout was needed")
	assert.Zero(t, f.gets("deployments"), "workloads are read from the cache")
	assert.Zero(t, f.cw.pendingRequests.Load())
}

func TestReconcileDefersWhileRolloutInProgress(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, _, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 2, nil)
	deployment.Status.Replicas = 3
	deployment.Status.UpdatedReplicas = 1
	pod := agentPod(ns, "echo-old", name, k8sapi.DeploymentKind, staleJSON, true)
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, pod)
	f.evictionsSucceed()

	wl := k8sapi.Deployment(deployment)
	f.cw.requestEviction(f.ctx, wl, wanted)
	pending, err := f.cw.reconcile(f.ctx, workloadKeyOf(wl))
	require.NoError(t, err)
	assert.True(t, pending)
	assert.Zero(t, f.evictions.Load())
	assert.False(t, f.cw.isEvicted(pod.UID), "a deferred pod is not marked as deleted")
	state := f.cw.lockEvictionState(workloadKeyOf(wl))
	assert.False(t, state.deferredSince.IsZero())
	state.Unlock()
}

func TestTriggeredRolloutIsNotRepatched(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, wantedJSON, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 2, nil)
	oldPods := []*core.Pod{
		agentPod(ns, "echo-old-0", name, k8sapi.DeploymentKind, staleJSON, true),
		agentPod(ns, "echo-old-1", name, k8sapi.DeploymentKind, staleJSON, true),
	}
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, oldPods[0], oldPods[1])
	f.evictionsRefused()
	f.cw.budgetGrace = 0

	wl := k8sapi.Deployment(deployment)
	key := workloadKeyOf(wl)
	f.cw.requestEviction(f.ctx, wl, wanted)
	pending, err := f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	require.True(t, pending)
	assert.Equal(t, 1, f.patches("deployments"), "the refused eviction restarts the workload")

	// The controller has not reacted yet: the cached workload is unchanged.
	pending, err = f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	require.True(t, pending)
	assert.Equal(t, 1, f.patches("deployments"), "no second restart while the first is pending")
	assert.Equal(t, int32(1), f.evictions.Load(), "no eviction while the restart is pending")

	// The controller finished: a later generation is observed and the pods are new.
	finished := deployment.DeepCopy()
	finished.Generation = 4
	finished.Status.ObservedGeneration = 4
	f.updateStore(finished)
	for _, pod := range oldPods {
		f.removeFromStore(pod)
	}
	f.addToStore(agentPod(ns, "echo-new-0", name, k8sapi.DeploymentKind, wantedJSON, true))
	f.addToStore(agentPod(ns, "echo-new-1", name, k8sapi.DeploymentKind, wantedJSON, true))
	pending, err = f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	assert.False(t, pending)
	assert.Equal(t, 1, f.patches("deployments"))
}

func TestRefusedEvictionIsRetriedBeforeRestart(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, wantedJSON, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 2, nil)
	pods := []*core.Pod{
		agentPod(ns, "echo-old-0", name, k8sapi.DeploymentKind, staleJSON, true),
		agentPod(ns, "echo-old-1", name, k8sapi.DeploymentKind, staleJSON, true),
	}
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, pods[0], pods[1])
	f.evictionsRefused()

	wl := k8sapi.Deployment(deployment)
	key := workloadKeyOf(wl)
	f.cw.requestEviction(f.ctx, wl, wanted)
	pending, err := f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	require.True(t, pending)
	assert.Equal(t, int32(1), f.evictions.Load())
	assert.Zero(t, f.patches("deployments"), "a fresh refusal does not restart the workload")
	state := f.cw.lockEvictionState(key)
	assert.False(t, state.budgetRefusedSince.IsZero())
	state.Unlock()

	// The budget's status caught up: the eviction goes through and the refusal is forgotten.
	f.evictionsSucceed()
	pending, err = f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	require.True(t, pending)
	assert.Equal(t, int32(2), f.evictions.Load())
	assert.Zero(t, f.patches("deployments"))
	state = f.cw.lockEvictionState(key)
	assert.True(t, state.budgetRefusedSince.IsZero())
	state.Unlock()

	f.addToStore(agentPod(ns, "echo-new-0", name, k8sapi.DeploymentKind, wantedJSON, true))
	f.evictionsRefused()
	f.cw.budgetGrace = 0
	pending, err = f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	require.True(t, pending)
	assert.Equal(t, 1, f.patches("deployments"), "a refusal that outlasts the grace restarts the workload")
}

// degradedDeployment is a four-replica Deployment allowing one unavailable pod, with one pod that
// never becomes ready, so the budget for evictions is zero.
func degradedDeployment(t *testing.T, ctx context.Context) (*evictionFixture, k8sapi.Workload) {
	const ns, name = "default", "echo"
	wanted, _, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	maxUnavailable := intstr.FromInt32(1)
	deployment := stableDeployment(ns, name, 4, &maxUnavailable)
	objects := []runtime.Object{deployment}
	for i := range 4 {
		objects = append(objects, agentPod(ns, fmt.Sprintf("echo-%d", i), name, k8sapi.DeploymentKind, staleJSON, i != 0))
	}
	f := newEvictionFixture(t, ctx, k8sapi.Kinds{k8sapi.DeploymentKind}, objects...)
	f.evictionsSucceed()
	wl := k8sapi.Deployment(deployment)
	f.cw.requestEviction(f.ctx, wl, wanted)
	return f, wl
}

func TestDeferredWorkloadProceedsAfterCeiling(t *testing.T) {
	f, wl := degradedDeployment(t, testutil.NewContext(t, false))
	f.cw.deferralCeiling = 0
	key := workloadKeyOf(wl)

	pending, err := f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	require.True(t, pending)
	assert.Zero(t, f.evictions.Load(), "no eviction beyond the budget")
	assert.Equal(t, 1, f.patches("deployments"), "the workload is restarted through its controller")

	pending, err = f.cw.reconcile(f.ctx, key)
	require.NoError(t, err)
	require.True(t, pending)
	assert.Equal(t, 1, f.patches("deployments"), "the restart it triggered is waited for without a ceiling")
}

type logRecorder struct {
	sync.Mutex
	records []slog.Record
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *logRecorder) Handle(_ context.Context, record slog.Record) error {
	r.Lock()
	r.records = append(r.records, record)
	r.Unlock()
	return nil
}

func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *logRecorder) WithGroup(string) slog.Handler { return r }

func (r *logRecorder) count(level slog.Level, substr string) int {
	r.Lock()
	defer r.Unlock()
	n := 0
	for _, record := range r.records {
		if record.Level == level && strings.Contains(record.Message, substr) {
			n++
		}
	}
	return n
}

func TestDeferredWorkloadIsLoggedOnce(t *testing.T) {
	recorder := &logRecorder{}
	f, wl := degradedDeployment(t, clog.WithLogger(context.Background(), slog.New(recorder)))
	f.cw.deferralNotice = 0
	key := workloadKeyOf(wl)

	for range 3 {
		pending, err := f.cw.reconcile(f.ctx, key)
		require.NoError(t, err)
		require.True(t, pending)
	}
	assert.Zero(t, f.evictions.Load())
	assert.Zero(t, f.patches("deployments"))
	assert.Equal(t, 1, recorder.count(slog.LevelInfo, "Deferring agent update"))
	assert.Equal(t, 2, recorder.count(slog.LevelDebug, "Deferring agent update"))
}

func TestMissedTransitionIsRecoveredByRequeue(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, wantedJSON, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	maxUnavailable := intstr.FromInt32(1)
	deployment := stableDeployment(ns, name, 4, &maxUnavailable)
	objects := []runtime.Object{deployment}
	for i := range 4 {
		objects = append(objects, agentPod(ns, fmt.Sprintf("echo-old-%d", i), name, k8sapi.DeploymentKind, staleJSON, true))
	}
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, objects...)
	f.cw.evictionQueue = newEvictionQueue(time.Millisecond, 10*time.Millisecond)

	// An eviction's ready replacement appears in the cache, but no informer event announces it.
	podStore := f.store(&core.Pod{})
	f.evictionReactor(func(eviction *policy.Eviction) error {
		replacement := agentPod(ns, "new-"+eviction.Name, name, k8sapi.DeploymentKind, wantedJSON, true)
		return podStore.Add(replacement)
	})

	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.cw.startEvictionWorkers(ctx)
	wl := k8sapi.Deployment(deployment)
	f.cw.requestEviction(ctx, wl, wanted)
	require.Eventually(t, func() bool {
		return f.evictions.Load() == 4 && f.cw.pendingRequests.Load() == 0
	}, 5*time.Second, 5*time.Millisecond, "the timed requeue completes the sequence")
	assert.Zero(t, f.patches("deployments"))
}

func TestPodReadyEventWakesPendingWorkload(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, _, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 1, nil)
	pod := agentPod(ns, "echo-0", name, k8sapi.DeploymentKind, staleJSON, true)
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, pod)
	q := f.cw.evictionQueue
	drain := func() {
		key, _ := q.Get()
		q.Done(key)
		q.Forget(key)
	}

	f.cw.podChanged(f.ctx, pod)
	assert.Zero(t, q.Len(), "a pod of a workload without a request is ignored")

	wl := k8sapi.Deployment(deployment)
	f.cw.requestEviction(f.ctx, wl, wanted)
	require.Equal(t, 1, q.Len())
	drain()

	f.cw.podChanged(f.ctx, pod)
	assert.Equal(t, 1, q.Len(), "a pod of a requested workload wakes it")
	drain()

	other := agentPod(ns, "other-0", "other", k8sapi.DeploymentKind, "", true)
	f.cw.podChanged(f.ctx, other)
	assert.Zero(t, q.Len(), "a pod of another workload does not")
}

func TestRequestEvictionDoesNotTouchTheAPI(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, _, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 1, nil)
	pod := agentPod(ns, "echo-0", name, k8sapi.DeploymentKind, staleJSON, true)
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, pod)

	wl := k8sapi.Deployment(deployment)
	require.NoError(t, f.cw.EvictPodsWithAgentConfigMismatch(f.ctx, wl, wanted))
	assert.Empty(t, f.client.Actions(), "the request itself performs no API call")
	assert.Equal(t, 1, f.cw.evictionQueue.Len())
	state := f.cw.lockEvictionState(workloadKeyOf(wl))
	assert.True(t, state.requested)
	assert.Same(t, wanted, state.wanted)
	state.Unlock()
}
