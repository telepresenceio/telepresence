package mutator

import (
	"context"
	"errors"
	"testing"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
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
			err := evictPod(ctx, &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "echo-1", Namespace: "ns"}})
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
