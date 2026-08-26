package state

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	fakeargorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

func newTestDeployment(name, ns string) *apps.Deployment {
	return &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}
}

// newWatcherTestContext returns a context wired with a fake clientset seeded
// with objs and an informer factory for ns (the cluster-wide factory when ns
// is ""), with the Deployments informer started and synced. The returned
// cancel func stops the informer and any watcher goroutines started against
// this context; it's registered with t.Cleanup by the caller.
func newWatcherTestContext(t *testing.T, ns string, objs ...runtime.Object) (context.Context, *fake.Clientset) {
	t.Helper()
	// The fake clientset's watch doesn't emit the bookmark event this client
	// feature waits for, which would otherwise hang WaitForCacheSync.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	ctx, cancel := context.WithCancel(testutil.NewContext(t, false))
	t.Cleanup(cancel)

	fakeClient := fake.NewClientset(objs...)
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, fakeClient, fakeargorollouts.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, ns)

	f := informer.GetK8sFactory(ctx, ns)
	f.Apps().V1().Deployments().Informer()
	f.Start(ctx.Done())
	f.WaitForCacheSync(ctx.Done())
	return ctx, fakeClient
}

// eventNames extracts the workload names from a batch of events, for
// order-independent comparisons.
func eventNames(evs []Event) []string {
	names := make([]string, len(evs))
	for i, e := range evs {
		names[i] = e.Workload.GetName()
	}
	return names
}

func receiveWithTimeout(t *testing.T, ch <-chan []Event, timeout time.Duration) ([]Event, bool) {
	t.Helper()
	select {
	case evs := <-ch:
		return evs, true
	case <-time.After(timeout):
		return nil, false
	}
}

// drainSettle discards batches on ch until quiet has passed with nothing
// arriving. A SharedIndexInformer replays an Add for every object already in
// its store to any handler added after the fact, so the handler NewWatcher
// registers for an already-synced informer re-delivers the same workloads
// Subscribe's initial listing already produced. That replay is harmless
// (downstream consumers dedupe repeat adds) but would otherwise land in the
// same timer-batched delivery as a subsequent, genuinely new event and make
// these tests flaky; settle it out first.
func drainSettle(ch <-chan []Event, quiet time.Duration) {
	for {
		select {
		case <-ch:
		case <-time.After(quiet):
			return
		}
	}
}

// TestWatcher_ClusterWide_SubscriptionsFilterByNamespace verifies that a
// single cluster-wide watcher (ns == "") serves subscriptions for different
// namespaces correctly: each subscriber's initial snapshot only contains its
// own namespace's workloads, and a subsequent add in one namespace is
// delivered only to that namespace's subscriber.
func TestWatcher_ClusterWide_SubscriptionsFilterByNamespace(t *testing.T) {
	depA := newTestDeployment("dep-a", "ns-a")
	depB := newTestDeployment("dep-b", "ns-b")
	ctx, fakeClient := newWatcherTestContext(t, "", depA, depB)

	kinds := k8sapi.Kinds{k8sapi.DeploymentKind}
	w, err := NewWatcher(ctx, "", kinds)
	require.NoError(t, err)
	defer w.Close()

	chA := w.Subscribe(ctx, "ns-a")
	chB := w.Subscribe(ctx, "ns-b")

	initialA := <-chA
	initialB := <-chB
	assert.ElementsMatch(t, []string{"dep-a"}, eventNames(initialA), "ns-a subscriber's initial batch must contain only ns-a workloads")
	assert.ElementsMatch(t, []string{"dep-b"}, eventNames(initialB), "ns-b subscriber's initial batch must contain only ns-b workloads")
	drainSettle(chA, 300*time.Millisecond)
	drainSettle(chB, 300*time.Millisecond)

	// Adding a Deployment in ns-a must be delivered only to the ns-a subscriber.
	depA2 := newTestDeployment("dep-a2", "ns-a")
	_, err = fakeClient.AppsV1().Deployments("ns-a").Create(ctx, depA2, meta.CreateOptions{})
	require.NoError(t, err)

	evs, ok := receiveWithTimeout(t, chA, 2*time.Second)
	require.True(t, ok, "ns-a subscriber must receive a delta for the new ns-a Deployment")
	assert.ElementsMatch(t, []string{"dep-a2"}, eventNames(evs))

	_, ok = receiveWithTimeout(t, chB, 500*time.Millisecond)
	assert.False(t, ok, "ns-b subscriber must not receive anything for an event in ns-a")
}

// TestWatcher_Scoped_BehavesAsBefore verifies scoped-mode parity: a watcher
// created for a specific namespace, backed by a factory scoped to that
// namespace, still delivers an initial snapshot and a delta on add exactly
// as it did before subscriptions gained their own namespace.
func TestWatcher_Scoped_BehavesAsBefore(t *testing.T) {
	dep := newTestDeployment("dep-a", "ns-a")
	ctx, fakeClient := newWatcherTestContext(t, "ns-a", dep)

	kinds := k8sapi.Kinds{k8sapi.DeploymentKind}
	w, err := NewWatcher(ctx, "ns-a", kinds)
	require.NoError(t, err)
	defer w.Close()

	ch := w.Subscribe(ctx, "ns-a")
	initial := <-ch
	assert.ElementsMatch(t, []string{"dep-a"}, eventNames(initial))
	drainSettle(ch, 300*time.Millisecond)

	dep2 := newTestDeployment("dep-a2", "ns-a")
	_, err = fakeClient.AppsV1().Deployments("ns-a").Create(ctx, dep2, meta.CreateOptions{})
	require.NoError(t, err)

	evs, ok := receiveWithTimeout(t, ch, 2*time.Second)
	require.True(t, ok, "subscriber must receive a delta for the new Deployment")
	assert.ElementsMatch(t, []string{"dep-a2"}, eventNames(evs))
}

// TestState_WatchWorkloads_KeyFollowsScope pins the watcher-map keying:
// cluster-wide scope shares one watcher under "", and selector scope keys by
// the subscription's namespace.
func TestState_WatchWorkloads_KeyFollowsScope(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	newScopeState := func(ctx context.Context) *State {
		return &State{
			backgroundCtx:    ctx,
			workloadWatchers: xsync.NewMap[string, Watcher](),
		}
	}
	hasKey := func(s *State, key string) bool {
		_, ok := s.workloadWatchers.Load(key)
		return ok
	}

	t.Run("cluster-wide", func(t *testing.T) {
		// No namespaces handle in the context: namespaces.Get returns nil.
		ctx, _ := newWatcherTestContext(t, "", newTestDeployment("dep", "ns-a"))
		ctx = managerutil.WithEnv(ctx, &managerutil.Env{EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind}})
		s := newScopeState(ctx)

		_, err := s.WatchWorkloads(ctx, "ns-a")
		require.NoError(t, err)
		assert.True(t, hasKey(s, ""), "cluster-wide scope must share the \"\" watcher")
		assert.False(t, hasKey(s, "ns-a"), "cluster-wide scope must not key by namespace")

		_, err = s.WatchWorkloads(ctx, "ns-b")
		require.NoError(t, err)
		assert.False(t, hasKey(s, "ns-b"), "every namespace shares the \"\" watcher")
	})

	t.Run("selector-scoped", func(t *testing.T) {
		ctx, _ := newWatcherTestContext(t, "", newTestDeployment("dep", "ns-a"))
		ctx = managerutil.WithEnv(ctx, &managerutil.Env{EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind}})

		// A static name-in selector makes namespaces.Get return its names
		// without watching the cluster.
		selCh := make(chan *labels.Selector, 1)
		selCh <- &labels.Selector{
			MatchExpressions: []*labels.Requirement{{
				Key:      labels.NameLabelKey,
				Operator: labels.OperatorIn,
				Values:   []string{"ns-a"},
			}},
		}
		ctx, err := namespaces.InitContext(ctx, selCh)
		require.NoError(t, err)

		s := newScopeState(ctx)
		_, err = s.WatchWorkloads(ctx, "ns-a")
		require.NoError(t, err)
		assert.True(t, hasKey(s, "ns-a"), "selector scope must key by namespace")
		assert.False(t, hasKey(s, ""), "selector scope must not create the cluster-wide watcher")
	})
}

func TestWatcherDispatchSkipsCanceledSubscription(t *testing.T) {
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan []Event, 1)
	ch <- nil

	w := &watcher{
		subscriptions: map[uuid.UUID]subscription{
			uuid.New(): {
				ch:        ch,
				namespace: "default",
				done:      subCtx.Done(),
			},
		},
		events: []Event{{Workload: k8sapi.Deployment(newTestDeployment("app", "default"))}},
	}

	dispatched := make(chan struct{})
	go func() {
		w.dispatch(context.Background())
		close(dispatched)
	}()

	require.Eventually(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return w.events == nil
	}, time.Second, time.Millisecond)

	cancel()
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("dispatch blocked on canceled subscription")
	}
}
