package mutator

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// evictionFixture backs the eviction code with a fake clientset and informer stores that hold
// whatever the test puts there. No informer runs, so the stores change only when the test, or
// evictPod itself, changes them.
type evictionFixture struct {
	t         *testing.T
	ctx       context.Context
	client    *fake.Clientset
	cw        *configWatcher
	evictions atomic.Int32
}

func newEvictionFixture(t *testing.T, ctx context.Context, kinds k8sapi.Kinds, objects ...runtime.Object) *evictionFixture {
	client := fake.NewSimpleClientset(objects...)
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, client, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{EnabledWorkloadKinds: kinds})
	f := &evictionFixture{t: t, ctx: ctx, client: client, cw: NewWatcher().(*configWatcher)}
	for _, obj := range objects {
		f.addToStore(obj)
	}
	return f
}

// evictions installs a reactor for pod evictions. A nil error from fn means the pod is deleted.
func (f *evictionFixture) evictionReactor(fn func(eviction *policy.Eviction) error) {
	f.client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		f.evictions.Add(1)
		eviction := action.(k8stesting.CreateAction).GetObject().(*policy.Eviction)
		if err := fn(eviction); err != nil {
			return true, nil, err
		}
		return true, nil, f.client.Tracker().Delete(core.SchemeGroupVersion.WithResource("pods"), eviction.Namespace, eviction.Name)
	})
}

func (f *evictionFixture) evictionsSucceed() {
	f.evictionReactor(func(*policy.Eviction) error { return nil })
}

func (f *evictionFixture) evictionsRefused() {
	f.evictionReactor(func(*policy.Eviction) error {
		return k8sErrors.NewTooManyRequests("eviction would violate the pod's disruption budget", 0)
	})
}

func (f *evictionFixture) store(obj runtime.Object) cache.Store {
	factory := informer.GetK8sFactory(f.ctx, "")
	switch obj.(type) {
	case *core.Pod:
		return factory.Core().V1().Pods().Informer().GetStore()
	case *apps.Deployment:
		return factory.Apps().V1().Deployments().Informer().GetStore()
	case *apps.ReplicaSet:
		return factory.Apps().V1().ReplicaSets().Informer().GetStore()
	case *apps.StatefulSet:
		return factory.Apps().V1().StatefulSets().Informer().GetStore()
	default:
		f.t.Fatalf("no store for %T", obj)
		return nil
	}
}

func (f *evictionFixture) addToStore(obj runtime.Object) {
	require.NoError(f.t, f.store(obj).Add(obj.DeepCopyObject()))
}

func (f *evictionFixture) updateStore(obj runtime.Object) {
	require.NoError(f.t, f.store(obj).Update(obj.DeepCopyObject()))
}

func (f *evictionFixture) removeFromStore(obj runtime.Object) {
	require.NoError(f.t, f.store(obj).Delete(obj))
}

func (f *evictionFixture) patches(resource string) int {
	return countActions(f.client.Actions(), "patch", resource)
}

func (f *evictionFixture) gets(resource string) int {
	return countActions(f.client.Actions(), "get", resource)
}

// stableDeployment is a Deployment whose controller has nothing left to do.
func stableDeployment(ns, name string, replicas int32, maxUnavailable *intstr.IntOrString) *apps.Deployment {
	d := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: name, Namespace: ns, Generation: 3},
		Spec: apps.DeploymentSpec{
			Replicas: &replicas,
			Selector: &meta.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: core.PodTemplateSpec{ObjectMeta: meta.ObjectMeta{Labels: map[string]string{"app": name}}},
		},
		Status: apps.DeploymentStatus{
			ObservedGeneration: 3,
			Replicas:           replicas,
			UpdatedReplicas:    replicas,
			ReadyReplicas:      replicas,
			AvailableReplicas:  replicas,
			Conditions: []apps.DeploymentCondition{{
				Type:   apps.DeploymentProgressing,
				Status: core.ConditionTrue,
				Reason: "NewReplicaSetAvailable",
			}},
		},
	}
	if maxUnavailable != nil {
		d.Spec.Strategy = apps.DeploymentStrategy{
			Type:          apps.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &apps.RollingUpdateDeployment{MaxUnavailable: maxUnavailable},
		}
	}
	return d
}

func stableReplicaSet(ns, name string, replicas int32) *apps.ReplicaSet {
	return &apps.ReplicaSet{
		ObjectMeta: meta.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: apps.ReplicaSetSpec{
			Replicas: &replicas,
			Selector: &meta.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
		Status: apps.ReplicaSetStatus{
			ObservedGeneration:   1,
			Replicas:             replicas,
			ReadyReplicas:        replicas,
			AvailableReplicas:    replicas,
			FullyLabeledReplicas: replicas,
		},
	}
}

// agentPod is a running pod of the named workload. cfgJSON is its agent config annotation, "" for none.
func agentPod(ns, name, workload string, kind k8sapi.Kind, cfgJSON string, ready bool) *core.Pod {
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       types.UID(name),
			Labels: map[string]string{
				"app":                         workload,
				agentconfig.WorkloadNameLabel: workload,
				agentconfig.WorkloadKindLabel: string(kind),
			},
			Annotations: map[string]string{},
		},
		Status: core.PodStatus{Phase: core.PodRunning},
	}
	if cfgJSON != "" {
		pod.Annotations[annotation.Config] = cfgJSON
	}
	if ready {
		pod.Status.Conditions = []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}
	}
	return pod
}

// agentConfigs returns a wanted config and the JSON of a stale one for the workload. The wanted
// config carries an agent image, which the annotation form strips.
func agentConfigs(t *testing.T, ns, name string, kind k8sapi.Kind) (wanted *agentconfig.Sidecar, wantedJSON, staleJSON string) {
	wanted = &agentconfig.Sidecar{
		AgentName:    name,
		Namespace:    ns,
		WorkloadName: name,
		WorkloadKind: kind,
		AgentImage:   "tel2:new",
		ManagerHost:  "traffic-manager.ambassador",
	}
	stale := wanted.Clone()
	stale.ManagerHost = "traffic-manager.old"
	var err error
	wantedJSON, err = agentconfig.MarshalTight(wanted)
	require.NoError(t, err)
	staleJSON, err = agentconfig.MarshalTight(stale)
	require.NoError(t, err)
	return wanted, wantedJSON, staleJSON
}

func countActions(actions []k8stesting.Action, verb, resource string) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestEvictPod(t *testing.T) {
	tests := []struct {
		name      string
		evictErr  error
		expectErr string
		budgetErr bool
	}{
		{
			name: "evicted",
		},
		{
			name:     "pod already gone",
			evictErr: k8sErrors.NewNotFound(core.Resource("pods"), "echo-1"),
		},
		{
			name:      "disruption budget",
			evictErr:  k8sErrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0),
			expectErr: "disruption budget",
			budgetErr: true,
		},
		{
			name:      "other error",
			evictErr:  k8sErrors.NewBadRequest("nope"),
			expectErr: "nope",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t, false)
			ci := fake.NewClientset()
			ci.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
				if a.GetSubresource() != "eviction" {
					return false, nil, nil
				}
				return true, nil, tt.evictErr
			})
			ctx = k8sapi.WithJoinedClientSetInterface(ctx, ci, argorolloutsfake.NewSimpleClientset())
			ctx = informer.WithFactory(ctx, "")
			_, err := evictPod(ctx, &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "echo-1", Namespace: "ns"}})
			if tt.expectErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.expectErr)
				require.Equal(t, tt.budgetErr, errors.As(err, &disruptionBudgetError{}))
			}
		})
	}
}

func TestWorkloadPodsOnlyReturnsPodsOwnedByWorkload(t *testing.T) {
	const namespace = "default"
	controller := true
	selector := map[string]string{"app": "example-service"}
	stable := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "example-service", Namespace: namespace},
		Spec: apps.DeploymentSpec{
			Selector: &meta.LabelSelector{MatchLabels: selector},
			Template: core.PodTemplateSpec{ObjectMeta: meta.ObjectMeta{Labels: map[string]string{
				"app":   "example-service",
				"track": "stable",
			}}},
		},
	}
	canary := stable.DeepCopy()
	canary.Name = "example-service-canary"
	canary.Spec.Template.Labels["track"] = "canary"

	stableRS := &apps.ReplicaSet{ObjectMeta: meta.ObjectMeta{
		Name:      "example-service-66dfc6c7cf",
		Namespace: namespace,
		OwnerReferences: []meta.OwnerReference{{
			Kind:       string(k8sapi.DeploymentKind),
			Name:       stable.Name,
			Controller: &controller,
		}},
	}}
	canaryRS := &apps.ReplicaSet{ObjectMeta: meta.ObjectMeta{
		Name:      "example-service-canary-6569fd8dc4",
		Namespace: namespace,
		OwnerReferences: []meta.OwnerReference{{
			Kind:       string(k8sapi.DeploymentKind),
			Name:       canary.Name,
			Controller: &controller,
		}},
	}}
	stablePod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service-66dfc6c7cf-stable",
			Namespace: namespace,
			Labels:    selector,
			OwnerReferences: []meta.OwnerReference{{
				Kind:       string(k8sapi.ReplicaSetKind),
				Name:       stableRS.Name,
				Controller: &controller,
			}},
		},
		Status: core.PodStatus{Phase: core.PodRunning},
	}
	canaryPod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service-canary-6569fd8dc4-canary",
			Namespace: namespace,
			Labels:    selector,
			OwnerReferences: []meta.OwnerReference{{
				Kind:       string(k8sapi.ReplicaSetKind),
				Name:       canaryRS.Name,
				Controller: &controller,
			}},
		},
		Status: core.PodStatus{Phase: core.PodRunning},
	}

	f := newEvictionFixture(t, context.Background(), k8sapi.Kinds{k8sapi.DeploymentKind}, stable, canary, stablePod, canaryPod)
	require.NoError(t, f.client.Tracker().Add(stableRS))
	require.NoError(t, f.client.Tracker().Add(canaryRS))

	pods, err := workloadPods(f.ctx, k8sapi.Deployment(stable))
	require.NoError(t, err)
	require.Len(t, pods, 1)
	assert.Equal(t, stablePod.Name, pods[0].Name)

	pods, err = workloadPods(f.ctx, k8sapi.Deployment(canary))
	require.NoError(t, err)
	require.Len(t, pods, 1)
	assert.Equal(t, canaryPod.Name, pods[0].Name)
}

func TestWorkloadPodsFindsRolloutPodsWhenReplicaSetsAreDisabled(t *testing.T) {
	const namespace = "default"
	controller := true
	selector := map[string]string{"app": "example-rollout"}
	rollout := &argorollouts.Rollout{
		ObjectMeta: meta.ObjectMeta{Name: "example-rollout", Namespace: namespace},
		Spec: argorollouts.RolloutSpec{
			Selector: &meta.LabelSelector{MatchLabels: selector},
			Template: core.PodTemplateSpec{ObjectMeta: meta.ObjectMeta{Labels: selector}},
		},
	}
	replicaSet := &apps.ReplicaSet{ObjectMeta: meta.ObjectMeta{
		Name:      "example-rollout-66dfc6c7cf",
		Namespace: namespace,
		OwnerReferences: []meta.OwnerReference{{
			Kind:       string(k8sapi.RolloutKind),
			Name:       rollout.Name,
			Controller: &controller,
		}},
	}}
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-rollout-66dfc6c7cf-pod",
			Namespace: namespace,
			Labels:    selector,
			OwnerReferences: []meta.OwnerReference{{
				Kind:       string(k8sapi.ReplicaSetKind),
				Name:       replicaSet.Name,
				Controller: &controller,
			}},
		},
		Status: core.PodStatus{Phase: core.PodRunning},
	}

	client := fake.NewSimpleClientset(replicaSet, pod)
	rolloutsClient := argorolloutsfake.NewSimpleClientset(rollout)
	ctx := k8sapi.WithJoinedClientSetInterface(context.Background(), client, rolloutsClient)
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.RolloutKind},
	})
	podStore := informer.GetK8sFactory(ctx, namespace).Core().V1().Pods().Informer().GetStore()
	rolloutStore := informer.GetArgoRolloutsFactory(ctx, namespace).Argoproj().V1alpha1().Rollouts().Informer().GetStore()
	require.NoError(t, podStore.Add(pod.DeepCopy()))
	require.NoError(t, rolloutStore.Add(rollout.DeepCopy()))

	pods, err := workloadPods(ctx, k8sapi.Rollout(rollout))
	require.NoError(t, err)
	require.Len(t, pods, 1)
	assert.Equal(t, pod.Name, pods[0].Name)
}

func TestWorkloadRolloutInProgress(t *testing.T) {
	replicas := int32(6)
	deployment := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Generation: 12},
		Spec:       apps.DeploymentSpec{Replicas: &replicas},
		Status: apps.DeploymentStatus{
			ObservedGeneration: 12,
			Replicas:           6,
			UpdatedReplicas:    6,
			AvailableReplicas:  5,
			Conditions: []apps.DeploymentCondition{{
				Type:   apps.DeploymentProgressing,
				Status: core.ConditionTrue,
				Reason: "NewReplicaSetAvailable",
			}},
		},
	}
	assert.False(t, workloadRolloutInProgress(k8sapi.Deployment(deployment)), "steady unavailable replica")

	deployment.Status.Conditions[0].Reason = "ReplicaSetUpdated"
	assert.True(t, workloadRolloutInProgress(k8sapi.Deployment(deployment)), "rollout awaiting availability")

	deployment.Status.Conditions[0].Reason = "NewReplicaSetAvailable"
	deployment.Status.ObservedGeneration = 11
	assert.True(t, workloadRolloutInProgress(k8sapi.Deployment(deployment)), "generation not yet observed")

	deployment.Status.ObservedGeneration = 12
	deployment.Status.Replicas = 8
	deployment.Status.UpdatedReplicas = 3
	assert.True(t, workloadRolloutInProgress(k8sapi.Deployment(deployment)), "replicas being replaced")

	replicaSet := &apps.ReplicaSet{
		ObjectMeta: meta.ObjectMeta{Generation: 4},
		Spec:       apps.ReplicaSetSpec{Replicas: &replicas},
		Status: apps.ReplicaSetStatus{
			ObservedGeneration: 4,
			Replicas:           6,
			AvailableReplicas:  5,
		},
	}
	assert.False(t, workloadRolloutInProgress(k8sapi.ReplicaSet(replicaSet)), "steady unavailable replica")
	replicaSet.Status.Replicas = 5
	assert.True(t, workloadRolloutInProgress(k8sapi.ReplicaSet(replicaSet)), "scale in progress")

	statefulSet := &apps.StatefulSet{
		ObjectMeta: meta.ObjectMeta{Generation: 8},
		Spec: apps.StatefulSetSpec{
			Replicas: &replicas,
			UpdateStrategy: apps.StatefulSetUpdateStrategy{
				Type: apps.OnDeleteStatefulSetStrategyType,
			},
		},
		Status: apps.StatefulSetStatus{
			ObservedGeneration: 8,
			Replicas:           6,
			UpdatedReplicas:    0,
		},
	}
	assert.False(t, workloadRolloutInProgress(k8sapi.StatefulSet(statefulSet)), "OnDelete revision mismatch")
	statefulSet.Status.Replicas = 5
	assert.True(t, workloadRolloutInProgress(k8sapi.StatefulSet(statefulSet)), "OnDelete scale in progress")

	partition := int32(3)
	statefulSet.Status.Replicas = 6
	statefulSet.Spec.UpdateStrategy = apps.StatefulSetUpdateStrategy{
		Type:          apps.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &apps.RollingUpdateStatefulSetStrategy{Partition: &partition},
	}
	statefulSet.Status.UpdatedReplicas = 3
	assert.False(t, workloadRolloutInProgress(k8sapi.StatefulSet(statefulSet)), "completed partitioned rollout")
	statefulSet.Status.UpdatedReplicas = 2
	assert.True(t, workloadRolloutInProgress(k8sapi.StatefulSet(statefulSet)), "partitioned rollout in progress")

	rollout := &argorollouts.Rollout{
		ObjectMeta: meta.ObjectMeta{Generation: 12},
		Spec:       argorollouts.RolloutSpec{Replicas: &replicas},
		Status: argorollouts.RolloutStatus{
			ObservedGeneration: "12",
			Replicas:           6,
			UpdatedReplicas:    6,
			ReadyReplicas:      6,
			AvailableReplicas:  6,
			Phase:              argorollouts.RolloutPhaseHealthy,
		},
	}
	assert.False(t, workloadRolloutInProgress(k8sapi.Rollout(rollout)), "healthy rollout")
	rollout.Status.Phase = argorollouts.RolloutPhaseProgressing
	assert.True(t, workloadRolloutInProgress(k8sapi.Rollout(rollout)), "analysis in progress")
	rollout.Status.Phase = argorollouts.RolloutPhaseHealthy
	rollout.Status.ReadyReplicas = 5
	assert.True(t, workloadRolloutInProgress(k8sapi.Rollout(rollout)), "rollout awaiting readiness")
}

func TestAllowedUnavailable(t *testing.T) {
	percent := func(s string) *intstr.IntOrString { v := intstr.FromString(s); return &v }
	number := func(n int32) *intstr.IntOrString { v := intstr.FromInt32(n); return &v }
	deployment := func(replicas int32, maxUnavailable *intstr.IntOrString) k8sapi.Workload {
		return k8sapi.Deployment(stableDeployment("ns", "echo", replicas, maxUnavailable))
	}
	rollout := func(replicas int32, strategy argorollouts.RolloutStrategy) k8sapi.Workload {
		return k8sapi.Rollout(&argorollouts.Rollout{Spec: argorollouts.RolloutSpec{Replicas: &replicas, Strategy: strategy}})
	}
	statefulSet := func(replicas int32, maxUnavailable *intstr.IntOrString) k8sapi.Workload {
		ss := &apps.StatefulSet{Spec: apps.StatefulSetSpec{Replicas: &replicas}}
		if maxUnavailable != nil {
			ss.Spec.UpdateStrategy.RollingUpdate = &apps.RollingUpdateStatefulSetStrategy{MaxUnavailable: maxUnavailable}
		}
		return k8sapi.StatefulSet(ss)
	}
	recreate := stableDeployment("ns", "echo", 10, nil)
	recreate.Spec.Strategy.Type = apps.RecreateDeploymentStrategyType

	for _, tc := range []struct {
		name string
		wl   k8sapi.Workload
		want int
	}{
		{"deployment default 25%", deployment(10, nil), 2},
		{"deployment absolute", deployment(10, number(3)), 3},
		{"deployment percent", deployment(10, percent("50%")), 5},
		{"deployment rounds down to at least one", deployment(3, percent("25%")), 1},
		{"deployment zero relies on surge", deployment(4, number(0)), 1},
		{"deployment recreate", k8sapi.Deployment(recreate), 10},
		{"rollout canary default", rollout(8, argorollouts.RolloutStrategy{Canary: &argorollouts.CanaryStrategy{}}), 2},
		{"rollout canary explicit", rollout(8, argorollouts.RolloutStrategy{Canary: &argorollouts.CanaryStrategy{MaxUnavailable: number(1)}}), 1},
		{"rollout blue-green", rollout(8, argorollouts.RolloutStrategy{BlueGreen: &argorollouts.BlueGreenStrategy{}}), 1},
		{"statefulset default", statefulSet(6, nil), 1},
		{"statefulset explicit", statefulSet(6, number(2)), 2},
		{"replicaset", k8sapi.ReplicaSet(stableReplicaSet("ns", "echo", 5)), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, allowedUnavailable(tc.wl))
		})
	}
}

func TestPodsWithAgentConfigMismatch(t *testing.T) {
	const ns, name = "default", "echo"
	wanted, wantedJSON, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	ctx := context.Background()

	manual := agentPod(ns, "manual", name, k8sapi.DeploymentKind, staleJSON, true)
	manual.Annotations[annotation.ManuallyInjected] = "true"
	terminating := agentPod(ns, "terminating", name, k8sapi.DeploymentKind, staleJSON, true)
	now := meta.Now()
	terminating.DeletionTimestamp = &now
	stale := agentPod(ns, "stale", name, k8sapi.DeploymentKind, staleJSON, true)
	current := agentPod(ns, "current", name, k8sapi.DeploymentKind, wantedJSON, true)
	bare := agentPod(ns, "bare", name, k8sapi.DeploymentKind, "", true)
	pods := []*core.Pod{manual, terminating, stale, current, bare}

	names := func(pods []*core.Pod) []string {
		ns := make([]string, len(pods))
		for i, pod := range pods {
			ns[i] = pod.Name
		}
		return ns
	}
	assert.Equal(t, []string{"stale", "bare"}, names(podsWithAgentConfigMismatch(ctx, pods, wanted)), "agent wanted")
	assert.Equal(t, []string{"stale", "current"}, names(podsWithAgentConfigMismatch(ctx, pods, nil)), "no agent wanted")
}

func TestEvictBatchStopsAtBudget(t *testing.T) {
	const ns, name = "default", "echo"
	_, _, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 3, nil)
	pods := []*core.Pod{
		agentPod(ns, "echo-0", name, k8sapi.DeploymentKind, staleJSON, true),
		agentPod(ns, "echo-1", name, k8sapi.DeploymentKind, staleJSON, true),
		agentPod(ns, "echo-2", name, k8sapi.DeploymentKind, staleJSON, true),
	}
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, pods[0], pods[1], pods[2])
	f.evictionsSucceed()

	evicted, refused, err := f.cw.evictBatch(f.ctx, pods, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, evicted)
	assert.Nil(t, refused)
	assert.Equal(t, int32(2), f.evictions.Load())
	assert.True(t, f.cw.isEvicted(pods[0].UID))
	assert.True(t, f.cw.isEvicted(pods[1].UID))
	assert.False(t, f.cw.isEvicted(pods[2].UID))
}

func TestEvictBatchAbsentPodDoesNotConsumeBudget(t *testing.T) {
	const ns, name = "default", "echo"
	_, _, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 2, nil)
	gone := agentPod(ns, "echo-gone", name, k8sapi.DeploymentKind, staleJSON, true)
	blocked := agentPod(ns, "echo-blocked", name, k8sapi.DeploymentKind, staleJSON, true)
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, blocked)
	f.evictionReactor(func(eviction *policy.Eviction) error {
		if eviction.Name == gone.Name {
			return k8sErrors.NewNotFound(core.Resource("pods"), eviction.Name)
		}
		return k8sErrors.NewTooManyRequests("eviction would violate the pod's disruption budget", 0)
	})

	evicted, refused, err := f.cw.evictBatch(f.ctx, []*core.Pod{gone, blocked}, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, evicted)
	assert.Same(t, blocked, refused, "the refused pod ends the batch")
	assert.Equal(t, int32(2), f.evictions.Load(), "the absent pod did not use up the budget")
	assert.False(t, f.cw.isEvicted(blocked.UID), "a refused pod is not marked as deleted")
	assert.Zero(t, f.patches("deployments"), "the batch leaves the restart decision to its caller")
}

func TestTeardownRestartsWorkloadWhenEvictionIsRefused(t *testing.T) {
	const ns, name = "default", "echo"
	_, _, staleJSON := agentConfigs(t, ns, name, k8sapi.DeploymentKind)
	deployment := stableDeployment(ns, name, 1, nil)
	pod := agentPod(ns, "echo-0", name, k8sapi.DeploymentKind, staleJSON, true)
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment, pod)
	f.evictionsRefused()

	require.NoError(t, f.cw.EvictAllPodsWithAgentConfig(f.ctx, ns))
	assert.Equal(t, int32(1), f.evictions.Load())
	assert.Equal(t, 1, f.patches("deployments"), "teardown does not wait for the budget")
}

func TestEvictPodConflictPreservesReplacementInStore(t *testing.T) {
	oldPod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default", UID: "old"}}
	replacement := oldPod.DeepCopy()
	replacement.UID = "new"
	client := fake.NewSimpleClientset(replacement.DeepCopy())
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "eviction" {
			return true, nil, k8sErrors.NewConflict(core.Resource("pods"), oldPod.Name, fmt.Errorf("UID precondition failed"))
		}
		return false, nil, nil
	})

	ctx := k8sapi.WithK8sInterface(context.Background(), client)
	ctx = informer.WithFactory(ctx, "")
	store := informer.GetK8sFactory(ctx, "default").Core().V1().Pods().Informer().GetStore()
	require.NoError(t, store.Add(replacement.DeepCopy()))
	evicted, err := evictPod(ctx, oldPod)
	require.NoError(t, err)
	assert.False(t, evicted)
	cached, exists, err := store.Get(oldPod)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, replacement.UID, cached.(*core.Pod).UID)
}

func TestTeardownEvictsEveryPodWithoutWaiting(t *testing.T) {
	const ns = "default"
	var objects []runtime.Object
	for _, name := range []string{"echo-one", "echo-two", "echo-three"} {
		objects = append(objects, stableReplicaSet(ns, name, 3))
		_, _, staleJSON := agentConfigs(t, ns, name, k8sapi.ReplicaSetKind)
		for i := range 3 {
			objects = append(objects, agentPod(ns, fmt.Sprintf("%s-%d", name, i), name, k8sapi.ReplicaSetKind, staleJSON, true))
		}
	}
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.ReplicaSetKind}, objects...)
	f.evictionsSucceed()
	f.cw.Store(&agentconfig.Sidecar{AgentName: "echo-one", Namespace: ns})

	start := time.Now()
	require.NoError(t, f.cw.EvictAllPodsWithAgentConfig(f.ctx, ns))
	assert.Less(t, time.Since(start), time.Second, "teardown must not wait for replacements")
	assert.Equal(t, int32(9), f.evictions.Load())
	assert.Zero(t, f.gets("replicasets"), "workloads are read from the cache")
	assert.Nil(t, f.cw.Get("echo-one", ns), "the namespace's agent configs are dropped")
}

func TestTeardownContinuesAfterWorkloadFailure(t *testing.T) {
	const ns = "default"
	var objects []runtime.Object
	for _, name := range []string{"echo-one", "echo-two"} {
		_, _, staleJSON := agentConfigs(t, ns, name, k8sapi.ReplicaSetKind)
		objects = append(objects, stableReplicaSet(ns, name, 1), agentPod(ns, name+"-0", name, k8sapi.ReplicaSetKind, staleJSON, true))
	}
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.ReplicaSetKind}, objects...)
	f.evictionReactor(func(eviction *policy.Eviction) error {
		if eviction.Name == "echo-one-0" {
			return k8sErrors.NewForbidden(core.Resource("pods"), eviction.Name, fmt.Errorf("denied"))
		}
		return nil
	})

	err := f.cw.EvictAllPodsWithAgentConfig(f.ctx, ns)
	require.ErrorContains(t, err, "echo-one")
	assert.Equal(t, int32(2), f.evictions.Load(), "the other workload is still evicted")
}

func TestEvictionStateIsReclaimed(t *testing.T) {
	deployment := stableDeployment("default", "echo", 1, nil)
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, deployment)
	cw, ctx := f.cw, f.ctx
	wl := k8sapi.Deployment(deployment)
	cw.requestEviction(ctx, wl, nil)
	require.Equal(t, 1, cw.evictionStates.Size())
	require.Equal(t, int32(1), cw.pendingRequests.Load())

	cw.deleteEvictionState(workloadKeyOf(wl))
	assert.Zero(t, cw.evictionStates.Size())
	assert.Zero(t, cw.pendingRequests.Load())
}
