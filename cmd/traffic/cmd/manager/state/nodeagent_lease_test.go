package state

import (
	"context"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// newLeaseTestState returns a State with just the maps that the lease
// registry, nodeAgentWanted, and the intercept map need, backed by ctx.
func newLeaseTestState(ctx context.Context) *State {
	return &State{
		backgroundCtx: ctx,
		intercepts:    cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		clients:       xsync.NewMap[tunnel.SessionID, *ClientSession](),
		leases:        xsync.NewMap[leaseKey, struct{}](),
	}
}

// storeLiveNodeAgentIntercept registers a live, non-child, node-agent
// intercept for name/namespace directly in s.intercepts, bypassing the k8s
// scaffolding AddIntercept would otherwise require.
func storeLiveNodeAgentIntercept(s *State, id, name, namespace string) *Intercept {
	ic := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          id,
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Spec:        &rpc.InterceptSpec{Agent: name, Namespace: namespace, NodeAgent: true},
	}}
	s.intercepts.Store(id, ic)
	return ic
}

// TestNodeAgentWanted verifies the predicate shared by the reap finalizers,
// ReleaseAgent, and the reconciler: a live node-agent intercept or a lease
// whose session is still alive both count as "wanted"; a lease whose session
// has gone, or the complete absence of either, does not.
func TestNodeAgentWanted(t *testing.T) {
	t.Parallel()

	const name, namespace = "test-agent", "test-namespace"

	t.Run("neither intercept nor lease", func(t *testing.T) {
		t.Parallel()
		s := newLeaseTestState(context.Background())
		assert.False(t, s.nodeAgentWanted(name, namespace))
	})

	t.Run("live node-agent intercept only", func(t *testing.T) {
		t.Parallel()
		s := newLeaseTestState(context.Background())
		storeLiveNodeAgentIntercept(s, "c1:ic1", name, namespace)
		assert.True(t, s.nodeAgentWanted(name, namespace))
	})

	t.Run("lease with live session only", func(t *testing.T) {
		t.Parallel()
		s := newLeaseTestState(context.Background())
		sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
		s.addLease(sid, name, namespace)
		assert.True(t, s.nodeAgentWanted(name, namespace))
	})

	t.Run("lease with dead session", func(t *testing.T) {
		t.Parallel()
		s := newLeaseTestState(context.Background())
		s.addLease(tunnel.SessionID("gone-session"), name, namespace)
		assert.False(t, s.nodeAgentWanted(name, namespace),
			"a lease whose session is no longer in s.clients must not count as wanted")
	})

	t.Run("lease for a different workload does not leak", func(t *testing.T) {
		t.Parallel()
		s := newLeaseTestState(context.Background())
		sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
		s.addLease(sid, "other-agent", "other-namespace")
		assert.False(t, s.nodeAgentWanted(name, namespace))
	})
}

// TestReleaseAgent verifies that releasing a lease reaps the node-agent Job
// once it was the last claim, leaves the Job alone while a node-agent
// intercept still claims it, and is a no-op when the caller holds no lease
// on the agent at all.
func TestReleaseAgent(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	const name, workloadNs = "test-agent", "ns1"

	newJob := func() *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: meta.ObjectMeta{
				Name:      "tel-node-agent-x",
				Namespace: mgrNs,
				Labels: map[string]string{
					nodeAgentAppLabel:       nodeAgentAppLabelValue,
					nodeAgentNameLabel:      name,
					nodeAgentNamespaceLabel: workloadNs,
				},
			},
		}
	}

	setup := func(t *testing.T) (*State, context.Context, *fake.Clientset) {
		t.Helper()
		ci := fake.NewSimpleClientset(newJob())
		installDeleteCollectionReactor(ci)
		ctx := k8sapi.WithK8sInterface(t.Context(), ci)
		ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs})
		return newLeaseTestState(ctx), ctx, ci
	}

	jobNames := func(t *testing.T, ci *fake.Clientset) []string {
		t.Helper()
		jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
		require.NoError(t, err)
		names := make([]string, len(jobs.Items))
		for i, j := range jobs.Items {
			names[i] = j.Name
		}
		return names
	}

	t.Run("reaps when last claim", func(t *testing.T) {
		t.Parallel()
		s, ctx, ci := setup(t)
		sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
		s.addLease(sid, name, workloadNs)

		require.NoError(t, s.ReleaseAgent(ctx, sid, name, workloadNs))
		assert.Empty(t, jobNames(t, ci))
	})

	t.Run("does not reap while a node-agent intercept lives", func(t *testing.T) {
		t.Parallel()
		s, ctx, ci := setup(t)
		sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
		s.addLease(sid, name, workloadNs)
		storeLiveNodeAgentIntercept(s, "c1:ic1", name, workloadNs)

		require.NoError(t, s.ReleaseAgent(ctx, sid, name, workloadNs))
		assert.Contains(t, jobNames(t, ci), "tel-node-agent-x",
			"Job must survive while an intercept still claims the agent")
	})

	t.Run("no-op for unknown lease", func(t *testing.T) {
		t.Parallel()
		s, ctx, ci := setup(t)

		require.NoError(t, s.ReleaseAgent(ctx, tunnel.SessionID("unknown"), name, workloadNs))
		assert.Contains(t, jobNames(t, ci), "tel-node-agent-x",
			"releasing a claim nobody holds must not reap the Job")
	})
}

// TestEnsureAgent_SidecarRejectedByNodeAgentClaim verifies the converse
// hazard PrepareIntercept already guards against for intercepts: a sidecar
// EnsureAgent request (node_agent=false) against a workload that a
// node-agent already claims -- via a live intercept or via an ingest lease
// with a live session -- is rejected rather than silently injecting a
// sidecar and restarting the pod the node-agent depends on.
func TestEnsureAgent_SidecarRejectedByNodeAgentClaim(t *testing.T) {
	t.Parallel()

	const name, namespace = "test-agent", "test-namespace"

	t.Run("live node-agent intercept", func(t *testing.T) {
		t.Parallel()
		s := newLeaseTestState(context.Background())
		storeLiveNodeAgentIntercept(s, "c1:ic1", name, namespace)

		_, err := s.EnsureAgent(context.Background(), tunnel.SessionID("other-session"), name, namespace, false, "")
		require.Error(t, err)
		assert.Equal(t, errcat.User, errcat.GetCategory(err))
		assert.Contains(t, err.Error(), "node-agent")
	})

	t.Run("node-agent lease with live session", func(t *testing.T) {
		t.Parallel()
		s := newLeaseTestState(context.Background())
		sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
		s.addLease(sid, name, namespace)

		_, err := s.EnsureAgent(context.Background(), tunnel.SessionID("other-session"), name, namespace, false, "")
		require.Error(t, err)
		assert.Equal(t, errcat.User, errcat.GetCategory(err))
		assert.Contains(t, err.Error(), "node-agent")
	})
}

// TestReconcileNodeAgentJobs_Leases extends the orphan sweep coverage in
// TestReconcileNodeAgentJobs with the lease side of the wanted-set: a Job
// whose only claim is a lease held by a live session is kept even past the
// grace period, while a Job whose lease's session is gone is reaped like any
// other orphan.
func TestReconcileNodeAgentJobs_Leases(t *testing.T) {
	t.Parallel()

	const ns = "ambassador"
	naJob := func(name, agentName, workloadNs string, age time.Duration) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: meta.ObjectMeta{
				Name:              name,
				Namespace:         ns,
				CreationTimestamp: meta.NewTime(time.Now().Add(-age)),
				Labels: map[string]string{
					nodeAgentAppLabel:       nodeAgentAppLabelValue,
					nodeAgentNameLabel:      agentName,
					nodeAgentNamespaceLabel: workloadNs,
				},
			},
		}
	}

	leaseAlive := naJob("tel-node-agent-lease-alive", "ingest-agent", "ns1", 10*time.Minute)
	leaseGone := naJob("tel-node-agent-lease-gone", "gone-ingest-agent", "ns1", 10*time.Minute)

	ci := fake.NewSimpleClientset(leaseAlive, leaseGone)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: ns})

	s := newLeaseTestState(ctx)
	sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
	s.addLease(sid, "ingest-agent", "ns1")
	s.addLease(tunnel.SessionID("dead-session"), "gone-ingest-agent", "ns1")

	require.NoError(t, s.reconcileNodeAgentJobs(ctx))

	jobs, err := ci.BatchV1().Jobs(ns).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	names := make([]string, len(jobs.Items))
	for i, j := range jobs.Items {
		names[i] = j.Name
	}
	assert.Contains(t, names, leaseAlive.Name, "Job with a lease held by a live session must be kept")
	assert.NotContains(t, names, leaseGone.Name, "Job whose lease's session is gone must be reaped")
}

// TestNodeAgentReapFinalizer_LeaseSurvivesInterceptRemoval drives the same
// finalizer AddIntercept and RestoreIntercepts register through the real
// removal path (s.RemoveIntercept, which deletes the intercept before
// invoking its finalizers, exactly as production does), to verify the
// C7-style interplay: removing a node-agent intercept must not reap a Job
// that an ingest's lease still claims, but releasing that lease afterwards
// does. This exercises the extracted decision points (nodeAgentReapFinalizer
// and ReleaseAgent) rather than the full AddIntercept lifecycle, which would
// additionally require a live pod and CRI scaffolding to create the Job in
// the first place.
func TestNodeAgentReapFinalizer_LeaseSurvivesInterceptRemoval(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	const name, workloadNs = "test-agent", "ns1"

	job := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "tel-node-agent-x",
			Namespace: mgrNs,
			Labels: map[string]string{
				nodeAgentAppLabel:       nodeAgentAppLabelValue,
				nodeAgentNameLabel:      name,
				nodeAgentNamespaceLabel: workloadNs,
			},
		},
	}
	ci := fake.NewSimpleClientset(job)
	installDeleteCollectionReactor(ci)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs})

	s := newLeaseTestState(ctx)

	// An ingest lease on the workload, held by a live session.
	sid := s.AddClient(&rpc.ClientInfo{Name: "ingest-client"}, nil, time.Now())
	s.addLease(sid, name, workloadNs)

	// A node-agent intercept on the same workload, carrying the same reap
	// finalizer that AddIntercept and RestoreIntercepts register.
	ic := storeLiveNodeAgentIntercept(s, "c1:ic1", name, workloadNs)
	ic.addFinalizer(s.nodeAgentReapFinalizer())

	jobNames := func() []string {
		jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
		require.NoError(t, err)
		names := make([]string, len(jobs.Items))
		for i, j := range jobs.Items {
			names[i] = j.Name
		}
		return names
	}

	// Removing the intercept must not reap the Job: the lease still claims it.
	s.RemoveIntercept(ic.Id)
	assert.Contains(t, jobNames(), job.Name, "Job must survive intercept removal while a lease still claims it")

	// Releasing the lease afterwards reaps it.
	require.NoError(t, s.ReleaseAgent(ctx, sid, name, workloadNs))
	assert.NotContains(t, jobNames(), job.Name, "releasing the last claim must reap the Job")
}

// TestNodeAgentReapFinalizer_SecondInterceptSurvivesFirstRemoval verifies the
// regression the guard removal in PrepareIntercept depends on: with two live
// node-agent intercepts sharing a workload's Job set, removing one (via
// s.RemoveIntercept, which deletes the intercept from s.intercepts before
// invoking its finalizers, exactly as production does) must not reap the
// Jobs while the other is still live, but removing the last one does.
func TestNodeAgentReapFinalizer_SecondInterceptSurvivesFirstRemoval(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	const name, workloadNs = "test-agent", "ns1"

	job := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "tel-node-agent-x",
			Namespace: mgrNs,
			Labels: map[string]string{
				nodeAgentAppLabel:       nodeAgentAppLabelValue,
				nodeAgentNameLabel:      name,
				nodeAgentNamespaceLabel: workloadNs,
			},
		},
	}
	ci := fake.NewSimpleClientset(job)
	installDeleteCollectionReactor(ci)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs})

	s := newLeaseTestState(ctx)

	// Two concurrent node-agent intercepts on the same workload, each
	// carrying the same reap finalizer that AddIntercept registers.
	ic1 := storeLiveNodeAgentIntercept(s, "c1:ic1", name, workloadNs)
	ic1.addFinalizer(s.nodeAgentReapFinalizer())
	ic2 := storeLiveNodeAgentIntercept(s, "c2:ic2", name, workloadNs)
	ic2.addFinalizer(s.nodeAgentReapFinalizer())

	jobNames := func() []string {
		jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
		require.NoError(t, err)
		names := make([]string, len(jobs.Items))
		for i, j := range jobs.Items {
			names[i] = j.Name
		}
		return names
	}

	// Removing the first intercept must not reap the Job: the second is
	// still live.
	s.RemoveIntercept(ic1.Id)
	assert.Contains(t, jobNames(), job.Name, "Job must survive removal of one of two concurrent node-agent intercepts")

	// Removing the last one reaps it.
	s.RemoveIntercept(ic2.Id)
	assert.NotContains(t, jobNames(), job.Name, "removing the last node-agent intercept must reap the Job")
}
