package state

import (
	"context"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func nodeAgentDiffTarget(podName string) nodeAgentPodTarget {
	return nodeAgentPodTarget{
		nodeName:     "node-1",
		containerIDs: map[string]string{"app": "containerd://" + podName},
		podName:      podName,
		podIP:        "10.42.0.1",
	}
}

func nodeAgentDiffJob(jobName, podName string) nodeAgentJobTarget {
	return nodeAgentJobTarget{jobName: jobName, podName: podName}
}

func podNameSet(names ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(names))
	for _, n := range names {
		s[n] = struct{}{}
	}
	return s
}

func targetNames(targets []nodeAgentPodTarget) []string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.podName
	}
	return names
}

// TestNodeAgentJobDiff_InterceptClaim_CreatesForEveryMissingTarget verifies
// that a live node-agent intercept wants a Job on every admissible target,
// so a workload with 3 targets and only 1 existing Job needs 2 creates and no
// deletes.
func TestNodeAgentJobDiff_InterceptClaim_CreatesForEveryMissingTarget(t *testing.T) {
	t.Parallel()

	targets := []nodeAgentPodTarget{nodeAgentDiffTarget("p0"), nodeAgentDiffTarget("p1"), nodeAgentDiffTarget("p2")}
	jobs := []nodeAgentJobTarget{nodeAgentDiffJob("job-p0", "p0")}
	live := podNameSet("p0", "p1", "p2")

	createFor, deleteJobs := nodeAgentJobDiff(targets, live, jobs, true)
	assert.ElementsMatch(t, []string{"p1", "p2"}, targetNames(createFor))
	assert.Empty(t, deleteJobs)
}

// TestNodeAgentJobDiff_LeaseOnly_CreatesExactlyOne verifies that a lease-only
// (ingest) claim with no existing Job creates exactly one, for the first
// admissible target -- never more, since an ingest gains nothing from
// additional replicas.
func TestNodeAgentJobDiff_LeaseOnly_CreatesExactlyOne(t *testing.T) {
	t.Parallel()

	targets := []nodeAgentPodTarget{nodeAgentDiffTarget("p0"), nodeAgentDiffTarget("p1")}
	live := podNameSet("p0", "p1")

	createFor, deleteJobs := nodeAgentJobDiff(targets, live, nil, false)
	require.Len(t, createFor, 1)
	assert.Equal(t, "p0", createFor[0].podName)
	assert.Empty(t, deleteJobs)
}

// TestNodeAgentJobDiff_LeaseOnly_ReusesLiveJob verifies that a lease-only
// claim with a Job already targeting a current admissible pod creates
// nothing more, even though other admissible targets exist.
func TestNodeAgentJobDiff_LeaseOnly_ReusesLiveJob(t *testing.T) {
	t.Parallel()

	targets := []nodeAgentPodTarget{nodeAgentDiffTarget("p0"), nodeAgentDiffTarget("p1")}
	jobs := []nodeAgentJobTarget{nodeAgentDiffJob("job-p1", "p1")}
	live := podNameSet("p0", "p1")

	createFor, deleteJobs := nodeAgentJobDiff(targets, live, jobs, false)
	assert.Empty(t, createFor)
	assert.Empty(t, deleteJobs)
}

// TestNodeAgentJobDiff_DeletesJobForGonePod verifies that a Job whose target
// pod is entirely gone (not merely unready) is deleted, regardless of claim
// kind.
func TestNodeAgentJobDiff_DeletesJobForGonePod(t *testing.T) {
	t.Parallel()

	targets := []nodeAgentPodTarget{nodeAgentDiffTarget("p0")}
	jobs := []nodeAgentJobTarget{nodeAgentDiffJob("job-gone", "gone-pod")}
	live := podNameSet("p0")

	createFor, deleteJobs := nodeAgentJobDiff(targets, live, jobs, true)
	assert.ElementsMatch(t, []string{"p0"}, targetNames(createFor), "the admissible target still needs its own Job")
	assert.Equal(t, []string{"job-gone"}, deleteJobs)
}

// TestNodeAgentJobDiff_KeepsJobForUnreadyPod verifies the no-shrink
// invariant: a Job whose target pod still exists, but is merely unready (so
// it doesn't appear in targets), is kept -- not deleted and not duplicated.
func TestNodeAgentJobDiff_KeepsJobForUnreadyPod(t *testing.T) {
	t.Parallel()

	// p0 exists (in live) but is not a current admissible target (e.g. it
	// just failed its readiness probe), so targets is empty.
	jobs := []nodeAgentJobTarget{nodeAgentDiffJob("job-p0", "p0")}
	live := podNameSet("p0")

	createFor, deleteJobs := nodeAgentJobDiff(nil, live, jobs, true)
	assert.Empty(t, createFor)
	assert.Empty(t, deleteJobs)
}

// TestNodeAgentJobDiff_MixedCreateAndDelete verifies that one reconcile pass
// can both create for a newly-admissible target and delete for a pod that
// vanished, in the same call.
func TestNodeAgentJobDiff_MixedCreateAndDelete(t *testing.T) {
	t.Parallel()

	targets := []nodeAgentPodTarget{nodeAgentDiffTarget("p0"), nodeAgentDiffTarget("p2")}
	jobs := []nodeAgentJobTarget{nodeAgentDiffJob("job-p1", "p1")}
	live := podNameSet("p0", "p2") // p1 is gone

	createFor, deleteJobs := nodeAgentJobDiff(targets, live, jobs, true)
	assert.ElementsMatch(t, []string{"p0", "p2"}, targetNames(createFor))
	assert.Equal(t, []string{"job-p1"}, deleteJobs)
}

// TestReconcileNodeAgentJobs_TargetPodGone verifies the sweep extension: a
// Job past the orphan grace period is reaped once its target pod no longer
// exists in the workload namespace, even though the workload still has a
// live node-agent intercept claiming it -- covering a per-workload pod-set
// watcher lost to a manager restart. A Job whose target pod still exists is
// left alone.
func TestReconcileNodeAgentJobs_TargetPodGone(t *testing.T) {
	t.Parallel()

	const ns = "ambassador"
	const workloadNs = "ns1"
	naJob := func(name, targetPod string, age time.Duration) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: meta.ObjectMeta{
				Name:              name,
				Namespace:         ns,
				CreationTimestamp: meta.NewTime(time.Now().Add(-age)),
				Labels: map[string]string{
					nodeAgentAppLabel:       nodeAgentAppLabelValue,
					nodeAgentNameLabel:      "live-agent",
					nodeAgentNamespaceLabel: workloadNs,
					nodeAgentTargetPodLabel: targetPod,
				},
			},
		}
	}

	podGone := naJob("tel-node-agent-pod-gone", "vanished-pod", 10*time.Minute)
	podPresent := naJob("tel-node-agent-pod-present", "still-here-pod", 10*time.Minute)
	stillHerePod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "still-here-pod", Namespace: workloadNs}}

	ci := fake.NewSimpleClientset(podGone, podPresent, stillHerePod)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: ns})

	s := &State{
		backgroundCtx: ctx,
		intercepts:    cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		clients:       xsync.NewMap[tunnel.SessionID, *ClientSession](),
		leases:        xsync.NewMap[leaseKey, struct{}](),
	}
	storeLiveNodeAgentIntercept(s, "s:live-agent", "live-agent", workloadNs)

	require.NoError(t, s.reconcileNodeAgentJobs(ctx))

	jobs, err := ci.BatchV1().Jobs(ns).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	names := make([]string, len(jobs.Items))
	for i, j := range jobs.Items {
		names[i] = j.Name
	}
	assert.NotContains(t, names, podGone.Name,
		"a Job whose target pod is gone must be reaped even though the workload has a live claim")
	assert.Contains(t, names, podPresent.Name, "a Job whose target pod still exists must be kept")
}

// newNodeAgentWatchTestState returns a State with the maps
// startNodeAgentPodWatch and its loop need, backed by ctx, with the watch
// intervals shrunk to keep these tests fast.
func newNodeAgentWatchTestState(ctx context.Context) *State {
	return &State{
		backgroundCtx:        ctx,
		intercepts:           cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		clients:              xsync.NewMap[tunnel.SessionID, *ClientSession](),
		leases:               xsync.NewMap[leaseKey, struct{}](),
		nodeAgentPodWatchers: xsync.NewMap[nodeAgentWatchKey, struct{}](),
		nodeAgentWatchTimings: nodeAgentWatchTimings{
			debounce: 10 * time.Millisecond,
			resync:   50 * time.Millisecond,
			backoff:  20 * time.Millisecond,
		},
	}
}

// TestStartNodeAgentPodWatch_Idempotent verifies that a second call for the
// same workload does not start a second watcher: the map holds exactly one
// entry regardless of how many times it's called.
func TestStartNodeAgentPodWatch_Idempotent(t *testing.T) {
	t.Parallel()

	const name, namespace = "test-agent", "test-namespace"
	ci := fake.NewSimpleClientset()
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: "ambassador"})

	s := newNodeAgentWatchTestState(ctx)
	storeLiveNodeAgentIntercept(s, "s:ic1", name, namespace)

	s.startNodeAgentPodWatch(name, namespace)
	s.startNodeAgentPodWatch(name, namespace)
	s.startNodeAgentPodWatch(name, namespace)

	assert.Equal(t, 1, s.nodeAgentPodWatchers.Size(), "a repeated call must not start a second watcher goroutine")

	// Let the watcher settle so it doesn't linger past the end of the test.
	s.intercepts.Delete("s:ic1")
	require.Eventually(t, func() bool {
		return s.nodeAgentPodWatchers.Size() == 0
	}, 2*time.Second, 10*time.Millisecond, "watcher must exit once nothing claims the workload anymore")
}

// TestNodeAgentPodWatchLoop_ExitsWhenNotWanted verifies that the watcher
// goroutine removes its own map entry and exits once nodeAgentWanted turns
// false, driven here by dropping the sole node-agent intercept that was
// keeping the claim alive.
func TestNodeAgentPodWatchLoop_ExitsWhenNotWanted(t *testing.T) {
	t.Parallel()

	const name, namespace = "test-agent", "test-namespace"
	ci := fake.NewSimpleClientset()
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: "ambassador"})

	s := newNodeAgentWatchTestState(ctx)
	storeLiveNodeAgentIntercept(s, "s:ic1", name, namespace)

	s.startNodeAgentPodWatch(name, namespace)
	require.Equal(t, 1, s.nodeAgentPodWatchers.Size())

	s.intercepts.Delete("s:ic1")

	require.Eventually(t, func() bool {
		return s.nodeAgentPodWatchers.Size() == 0
	}, 2*time.Second, 10*time.Millisecond, "watcher goroutine must exit once its claim ends")
}

// TestNodeAgentPodWatchLoop_ExitsWhenNotWanted_Lease is the lease-side
// variant: the claim is a lease with a live session rather than an intercept,
// and it ends when the session is removed from s.clients.
func TestNodeAgentPodWatchLoop_ExitsWhenNotWanted_Lease(t *testing.T) {
	t.Parallel()

	const name, namespace = "test-agent", "test-namespace"
	ci := fake.NewSimpleClientset()
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: "ambassador"})

	s := newNodeAgentWatchTestState(ctx)
	sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
	s.addLease(sid, name, namespace)

	s.startNodeAgentPodWatch(name, namespace)
	require.Equal(t, 1, s.nodeAgentPodWatchers.Size())

	s.clients.Delete(sid)

	require.Eventually(t, func() bool {
		return s.nodeAgentPodWatchers.Size() == 0
	}, 2*time.Second, 10*time.Millisecond, "watcher goroutine must exit once the lease's session is gone")
}
