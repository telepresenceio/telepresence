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
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

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

	client := fake.NewSimpleClientset(stable, canary, stableRS, canaryRS, stablePod, canaryPod)
	ctx := k8sapi.WithJoinedClientSetInterface(context.Background(), client, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind},
	})
	factory := informer.GetK8sFactory(ctx, namespace)
	deploymentStore := factory.Apps().V1().Deployments().Informer().GetStore()
	podStore := factory.Core().V1().Pods().Informer().GetStore()
	for _, deployment := range []*apps.Deployment{stable, canary} {
		require.NoError(t, deploymentStore.Add(deployment.DeepCopy()))
	}
	for _, pod := range []*core.Pod{stablePod, canaryPod} {
		require.NoError(t, podStore.Add(pod.DeepCopy()))
	}

	pods, err := workloadPods(ctx, k8sapi.Deployment(stable))
	require.NoError(t, err)
	require.Len(t, pods, 1)
	assert.Equal(t, stablePod.Name, pods[0].Name)

	pods, err = workloadPods(ctx, k8sapi.Deployment(canary))
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

func TestWorkloadUpdateInProgress(t *testing.T) {
	replicas := int32(6)
	deployment := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Generation: 12},
		Spec:       apps.DeploymentSpec{Replicas: &replicas},
		Status: apps.DeploymentStatus{
			ObservedGeneration: 12,
			Replicas:           6,
			UpdatedReplicas:    6,
			AvailableReplicas:  6,
		},
	}

	assert.False(t, workloadUpdateInProgress(k8sapi.Deployment(deployment)))

	deployment.Status.Replicas = 8
	deployment.Status.UpdatedReplicas = 3
	deployment.Status.AvailableReplicas = 5
	assert.True(t, workloadUpdateInProgress(k8sapi.Deployment(deployment)))

	deployment.Status.ObservedGeneration = 11
	deployment.Status.Replicas = 6
	deployment.Status.UpdatedReplicas = 6
	deployment.Status.AvailableReplicas = 6
	assert.True(t, workloadUpdateInProgress(k8sapi.Deployment(deployment)))
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

func TestEvictOrRolloutDoesNotRestartUpdatingWorkload(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            apps.DeploymentStatus
		wantEvictionCount int
		wantPatchCount    int
		wantResult        podEvictionResult
	}{
		{
			name: "stable workload",
			status: apps.DeploymentStatus{
				ObservedGeneration: 12,
				Replicas:           6,
				UpdatedReplicas:    6,
				AvailableReplicas:  6,
			},
			wantEvictionCount: 1,
			wantPatchCount:    1,
			wantResult:        podEvictionStarted,
		},
		{
			name: "stable workload with unavailable replica",
			status: apps.DeploymentStatus{
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
			wantEvictionCount: 1,
			wantPatchCount:    1,
			wantResult:        podEvictionStarted,
		},
		{
			name: "update in progress",
			status: apps.DeploymentStatus{
				ObservedGeneration: 12,
				Replicas:           8,
				UpdatedReplicas:    3,
				AvailableReplicas:  5,
			},
			wantResult: podEvictionDeferred,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replicas := int32(6)
			deployment := &apps.Deployment{
				ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default", Generation: 12},
				Spec:       apps.DeploymentSpec{Replicas: &replicas},
				Status:     tc.status,
			}
			pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "echo-old", Namespace: "default", UID: types.UID("echo-old")}}
			client := fake.NewSimpleClientset(deployment.DeepCopy(), pod.DeepCopy())
			evictionCount := 0
			client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "eviction" {
					return false, nil, nil
				}
				evictionCount++
				return true, nil, k8sErrors.NewTooManyRequests("eviction would violate the pod's disruption budget", 0)
			})

			ctx := k8sapi.WithK8sInterface(context.Background(), client)
			ctx = managerutil.WithEnv(ctx, &managerutil.Env{})
			result, err := evictOrRollout(ctx, k8sapi.Deployment(deployment), pod)
			require.NoError(t, err)
			assert.Equal(t, tc.wantResult, result)
			assert.Equal(t, tc.wantEvictionCount, evictionCount)

			patchCount := 0
			for _, action := range client.Actions() {
				if action.GetVerb() == "patch" && action.GetResource().Resource == "deployments" {
					patchCount++
				}
			}
			assert.Equal(t, tc.wantPatchCount, patchCount)
		})
	}
}

func TestPodsWithAgentConfigMismatchIgnoresManualInjection(t *testing.T) {
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{
		Name: "echo",
		Annotations: map[string]string{
			annotation.Config:           "stale",
			annotation.ManuallyInjected: "true",
		},
	}}
	assert.Empty(t, podsWithAgentConfigMismatch(context.Background(), []*core.Pod{pod}, ""))
}

func TestDeferredEvictionIsNotMarkedDeleted(t *testing.T) {
	replicas := int32(2)
	stable := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default", Generation: 3},
		Spec:       apps.DeploymentSpec{Replicas: &replicas},
		Status: apps.DeploymentStatus{
			ObservedGeneration: 3,
			Replicas:           2,
			UpdatedReplicas:    2,
			AvailableReplicas:  2,
		},
	}
	updating := stable.DeepCopy()
	updating.Status.Replicas = 3
	updating.Status.UpdatedReplicas = 1
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "echo-old", Namespace: "default", UID: "old"}}
	client := fake.NewSimpleClientset(stable.DeepCopy(), pod.DeepCopy())
	getCount := 0
	client.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount == 1 {
			return true, stable.DeepCopy(), nil
		}
		return true, updating.DeepCopy(), nil
	})

	ctx := k8sapi.WithK8sInterface(context.Background(), client)
	cw := NewWatcher().(*configWatcher)
	require.NoError(t, cw.evictPods(ctx, k8sapi.Deployment(stable), []*core.Pod{pod}))
	assert.False(t, cw.isEvicted(pod.UID))
}

func TestAbsentPodDoesNotConsumeSuccessfulEvictionCount(t *testing.T) {
	replicas := int32(2)
	deployment := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default", Generation: 3},
		Spec: apps.DeploymentSpec{
			Replicas: &replicas,
			Template: core.PodTemplateSpec{ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{}}},
		},
		Status: apps.DeploymentStatus{
			ObservedGeneration: 3,
			Replicas:           2,
			UpdatedReplicas:    2,
			AvailableReplicas:  2,
		},
	}
	oldPods := []*core.Pod{
		{ObjectMeta: meta.ObjectMeta{Name: "echo-gone", Namespace: "default", UID: "gone"}},
		{ObjectMeta: meta.ObjectMeta{Name: "echo-blocked", Namespace: "default", UID: "blocked"}},
	}
	client := fake.NewSimpleClientset(deployment.DeepCopy())
	evictionCount := 0
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		evictionCount++
		if evictionCount == 1 {
			return true, nil, k8sErrors.NewNotFound(core.Resource("pods"), oldPods[0].Name)
		}
		return true, nil, k8sErrors.NewTooManyRequests("eviction would violate the pod's disruption budget", 0)
	})

	ctx := k8sapi.WithK8sInterface(context.Background(), client)
	ctx = informer.WithFactory(ctx, "")
	store := informer.GetK8sFactory(ctx, "default").Core().V1().Pods().Informer().GetStore()
	for _, pod := range oldPods {
		require.NoError(t, store.Add(pod.DeepCopy()))
	}
	cw := NewWatcher().(*configWatcher)
	require.NoError(t, cw.evictPods(ctx, k8sapi.Deployment(deployment), oldPods))
	assert.Equal(t, 2, evictionCount)
	assert.Equal(t, 1, countActions(client.Actions(), "patch", "deployments"))
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

func TestWaitForWorkloadRecoveryFollowsDesiredReplicaChanges(t *testing.T) {
	oldReplicas := int32(3)
	newReplicas := int32(4)
	original := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default", Generation: 1},
		Spec:       apps.DeploymentSpec{Replicas: &oldReplicas},
	}
	rescaled := original.DeepCopy()
	rescaled.Spec.Replicas = &newReplicas
	rescaled.Status = apps.DeploymentStatus{
		ObservedGeneration: 1,
		Replicas:           4,
		UpdatedReplicas:    4,
		ReadyReplicas:      4,
		AvailableReplicas:  4,
	}
	client := fake.NewSimpleClientset(rescaled)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = k8sapi.WithK8sInterface(ctx, client)
	require.NoError(t, waitForWorkloadRecovery(ctx, k8sapi.Deployment(original)))
}

func TestTeardownContinuesAfterWorkloadFailure(t *testing.T) {
	const namespace = "default"
	replicas := int32(1)
	objects := make([]runtime.Object, 0, 4)
	for _, name := range []string{"echo-one", "echo-two"} {
		selector := map[string]string{"app": name}
		objects = append(objects,
			&apps.ReplicaSet{
				ObjectMeta: meta.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
				Spec: apps.ReplicaSetSpec{
					Replicas: &replicas,
					Selector: &meta.LabelSelector{MatchLabels: selector},
				},
				Status: apps.ReplicaSetStatus{
					ObservedGeneration:   1,
					Replicas:             1,
					ReadyReplicas:        1,
					AvailableReplicas:    1,
					FullyLabeledReplicas: 1,
				},
			},
			&core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Name:      name + "-old",
					Namespace: namespace,
					UID:       types.UID(name + "-old"),
					Labels: map[string]string{
						"app":                         name,
						agentconfig.WorkloadNameLabel: name,
						agentconfig.WorkloadKindLabel: string(k8sapi.ReplicaSetKind),
					},
					Annotations: map[string]string{annotation.Config: "stale"},
				},
				Status: core.PodStatus{Phase: core.PodRunning},
			},
		)
	}

	client := fake.NewSimpleClientset(objects...)
	attempted := make(map[string]bool)
	client.PrependReactor("get", "replicasets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		name := action.(k8stesting.GetAction).GetName()
		attempted[name] = true
		return true, nil, k8sErrors.NewForbidden(apps.Resource("replicasets"), name, fmt.Errorf("denied"))
	})

	ctx := k8sapi.WithJoinedClientSetInterface(context.Background(), client, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.ReplicaSetKind}})
	factory := informer.GetK8sFactory(ctx, namespace)
	podStore := factory.Core().V1().Pods().Informer().GetStore()
	replicaSetStore := factory.Apps().V1().ReplicaSets().Informer().GetStore()
	for _, object := range objects {
		switch object := object.(type) {
		case *core.Pod:
			require.NoError(t, podStore.Add(object.DeepCopy()))
		case *apps.ReplicaSet:
			require.NoError(t, replicaSetStore.Add(object.DeepCopy()))
		}
	}

	cw := NewWatcher().(*configWatcher)
	require.Error(t, cw.evictAllPodsWithAgentConfigAndWait(ctx, namespace))
	assert.Equal(t, map[string]bool{"echo-one": true, "echo-two": true}, attempted)
}

func TestEvictionStateIsReclaimed(t *testing.T) {
	cw := NewWatcher().(*configWatcher)
	key := WorkloadKey{Name: "echo", Namespace: "default", Kind: k8sapi.DeploymentKind}
	state := cw.lockEvictionState(key)
	state.Unlock()
	require.Equal(t, 1, cw.evictionStates.Size())
	cw.deleteEvictionState(key)
	assert.Zero(t, cw.evictionStates.Size())
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

func TestRefreshWorkloadRetriesTransientError(t *testing.T) {
	replicas := int32(1)
	deployment := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default"},
		Spec:       apps.DeploymentSpec{Replicas: &replicas},
	}
	client := fake.NewSimpleClientset(deployment.DeepCopy())
	getCount := 0
	client.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount == 1 {
			return true, nil, k8sErrors.NewServiceUnavailable("temporary API server failure")
		}
		return false, nil, nil
	})

	ctx := k8sapi.WithK8sInterface(context.Background(), client)
	refreshed, err := refreshWorkload(ctx, k8sapi.Deployment(deployment))
	require.NoError(t, err)
	assert.Equal(t, "echo", refreshed.GetName())
	assert.Equal(t, 2, getCount)
}

func TestDeleteMapsAndRolloutNamespaceWaitsForAllEvictions(t *testing.T) {
	const (
		namespace = "default"
		name      = "echo"
	)
	replicas := int32(3)
	selector := map[string]string{"app": name}
	replicaSet := &apps.ReplicaSet{
		ObjectMeta: meta.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: apps.ReplicaSetSpec{
			Replicas: &replicas,
			Selector: &meta.LabelSelector{MatchLabels: selector},
		},
		Status: apps.ReplicaSetStatus{
			ObservedGeneration:   1,
			Replicas:             replicas,
			ReadyReplicas:        replicas,
			AvailableReplicas:    replicas,
			FullyLabeledReplicas: replicas,
		},
	}
	objects := []runtime.Object{replicaSet}
	for i := range int(replicas) {
		objects = append(objects, &core.Pod{
			ObjectMeta: meta.ObjectMeta{
				Name:      fmt.Sprintf("echo-old-%d", i),
				Namespace: namespace,
				UID:       types.UID(fmt.Sprintf("echo-old-%d", i)),
				Labels: map[string]string{
					"app":                         name,
					agentconfig.WorkloadNameLabel: name,
					agentconfig.WorkloadKindLabel: string(k8sapi.ReplicaSetKind),
				},
				Annotations: map[string]string{annotation.Config: "stale"},
			},
			Status: core.PodStatus{Phase: core.PodRunning},
		})
	}

	client := fake.NewSimpleClientset(objects...)
	tracker := client.Tracker()
	podsResource := core.SchemeGroupVersion.WithResource("pods")
	replicaSetsResource := apps.SchemeGroupVersion.WithResource("replicasets")
	replacementName := ""
	evictionCount := 0
	var informerCancelled atomic.Bool
	var cancelledBeforeEviction atomic.Bool
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		if informerCancelled.Load() {
			cancelledBeforeEviction.Store(true)
		}
		eviction := action.(k8stesting.CreateAction).GetObject().(*policy.Eviction)
		require.NoError(t, tracker.Delete(podsResource, namespace, eviction.Name))
		evictionCount++
		replacementName = fmt.Sprintf("echo-new-%d", evictionCount)
		obj, err := tracker.Get(replicaSetsResource, namespace, name)
		require.NoError(t, err)
		updated := obj.(*apps.ReplicaSet).DeepCopy()
		updated.Status.Replicas = replicas - 1
		updated.Status.ReadyReplicas = replicas - 1
		updated.Status.AvailableReplicas = replicas - 1
		updated.Status.FullyLabeledReplicas = replicas - 1
		require.NoError(t, tracker.Update(replicaSetsResource, updated, namespace))
		return true, nil, nil
	})
	client.PrependReactor("get", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		if replacementName == "" {
			return false, nil, nil
		}
		obj, err := tracker.Get(replicaSetsResource, namespace, name)
		require.NoError(t, err)
		updating := obj.(*apps.ReplicaSet).DeepCopy()
		replacement := &core.Pod{
			ObjectMeta: meta.ObjectMeta{
				Name:      replacementName,
				Namespace: namespace,
				UID:       types.UID(replacementName),
				Labels: map[string]string{
					"app":                         name,
					agentconfig.WorkloadNameLabel: name,
					agentconfig.WorkloadKindLabel: string(k8sapi.ReplicaSetKind),
				},
			},
			Status: core.PodStatus{Phase: core.PodRunning},
		}
		require.NoError(t, tracker.Create(podsResource, replacement, namespace))
		recovered := updating.DeepCopy()
		recovered.Status.Replicas = replicas
		recovered.Status.ReadyReplicas = replicas
		recovered.Status.AvailableReplicas = replicas
		recovered.Status.FullyLabeledReplicas = replicas
		require.NoError(t, tracker.Update(replicaSetsResource, recovered, namespace))
		replacementName = ""
		return true, updating, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, client, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		AgentArrivalTimeout:  2 * time.Second,
		EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.ReplicaSetKind},
	})
	factory := informer.GetK8sFactory(ctx, namespace)
	podStore := factory.Core().V1().Pods().Informer().GetStore()
	replicaSetStore := factory.Apps().V1().ReplicaSets().Informer().GetStore()
	for _, object := range objects {
		switch object := object.(type) {
		case *core.Pod:
			require.NoError(t, podStore.Add(object.DeepCopy()))
		case *apps.ReplicaSet:
			require.NoError(t, replicaSetStore.Add(object.DeepCopy()))
		}
	}
	evictMap, err := podList(ctx, namespace)
	require.NoError(t, err)
	require.Len(t, evictMap, 1)

	cw := NewWatcher().(*configWatcher)
	iwc := &informersWithCancel{cancel: func() { informerCancelled.Store(true) }}
	cw.informers.Store(namespace, iwc)
	cw.deleteMapsAndRolloutNS(ctx, namespace, iwc)

	assert.Equal(t, int(replicas), evictionCount)
	assert.True(t, informerCancelled.Load())
	assert.False(t, cancelledBeforeEviction.Load())
}
