package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/broker"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

func TestSplitReconcilerSnapshotsWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, argorollouts.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	split := validControllerSplit()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: split.Namespace}}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout", Namespace: "shop", UID: types.UID("deployment-uid"), Labels: map[string]string{"app": "checkout"},
		},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "app", Env: []corev1.EnvVar{
					{Name: "ORDERS_TOPIC", Value: "orders"},
					{Name: "KAFKA_GROUP", Value: "checkout"},
					{Name: "KAFKA_ISOLATION_LEVEL", Value: "read_uncommitted"},
				},
			}},
		}}},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(split).
		WithObjects(split, deployment, namespace).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}
	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "checkout", Namespace: "shop"}})
	require.NoError(t, err)
	_, err = reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "checkout", Namespace: "shop"}})
	require.NoError(t, err)

	got := new(api.KafkaSplit)
	require.NoError(t, client.Get(t.Context(), types.NamespacedName{Name: "checkout", Namespace: "shop"}, got))
	require.Equal(t, api.SplitPhasePreparing, got.Status.Phase)
	require.Equal(t, []api.WorkloadReference{{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "checkout", UID: types.UID("deployment-uid"),
	}}, got.Status.Workloads)
}

func TestReconcileLabelsSplitNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	split := validControllerSplit()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: split.Namespace}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(split).WithObjects(split, namespace).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}

	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: split.Name, Namespace: split.Namespace}})
	require.NoError(t, err)

	got := new(corev1.Namespace)
	require.NoError(t, client.Get(t.Context(), types.NamespacedName{Name: split.Namespace}, got))
	require.Equal(t, "true", got.Labels[runtimeconfig.NamespaceLabel])
}

func TestSplitDeletionReleasesNamespaceLabelWhenLastSplit(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	split := validControllerSplit()
	split.Finalizers = []string{splitFinalizer}
	split.DeletionTimestamp = ptr.To(metav1.Now())
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: split.Namespace, Labels: map[string]string{runtimeconfig.NamespaceLabel: "true"},
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(split).WithObjects(split, namespace).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}

	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: split.Name, Namespace: split.Namespace}})
	require.NoError(t, err)

	got := new(corev1.Namespace)
	require.NoError(t, client.Get(t.Context(), types.NamespacedName{Name: split.Namespace}, got))
	require.NotContains(t, got.Labels, runtimeconfig.NamespaceLabel)
}

func TestSplitDeletionKeepsNamespaceLabelWhenSplitRemains(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	deleted := validControllerSplit()
	deleted.Name = "checkout"
	deleted.Finalizers = []string{splitFinalizer}
	deleted.DeletionTimestamp = ptr.To(metav1.Now())
	remaining := validControllerSplit()
	remaining.Name = "billing"
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: deleted.Namespace, Labels: map[string]string{runtimeconfig.NamespaceLabel: "true"},
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(deleted).
		WithObjects(deleted, remaining, namespace).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}

	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: deleted.Name, Namespace: deleted.Namespace}})
	require.NoError(t, err)

	got := new(corev1.Namespace)
	require.NoError(t, client.Get(t.Context(), types.NamespacedName{Name: deleted.Namespace}, got))
	require.Equal(t, "true", got.Labels[runtimeconfig.NamespaceLabel])
}

func TestWorkloadTemplateAdapters(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, argorollouts.AddToScheme(scheme))
	objects := []ctrlclient.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "shop"},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(2)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "deployment"}},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "deployment"}},
				}},
			},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "statefulset", Namespace: "shop"},
			Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(3)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "statefulset"}},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "statefulset"}},
				}},
			},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "replicaset", Namespace: "shop"},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: ptr.To(int32(4)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "replicaset"}},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "replicaset"}},
				}},
			},
		},
		&argorollouts.Rollout{
			ObjectMeta: metav1.ObjectMeta{Name: "rollout", Namespace: "shop"},
			Spec: argorollouts.RolloutSpec{
				Replicas: ptr.To(int32(5)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rollout"}},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "rollout"}},
				}},
			},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}
	for i, kind := range []string{"Deployment", "StatefulSet", "ReplicaSet", "Rollout"} {
		t.Run(kind, func(t *testing.T) {
			name := strings.ToLower(kind)
			template, selector, replicas, err := reconciler.workloadTemplate(t.Context(), "shop", api.WorkloadReference{
				Kind: kind, Name: name,
			})
			require.NoError(t, err)
			require.Equal(t, int32(i+2), replicas)
			require.Equal(t, name, template.Spec.Containers[0].Name)
			require.Equal(t, map[string]string{"app": name}, selector.MatchLabels)
		})
	}
}

func TestPodMutatorComposesActiveSplits(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "checkout", Namespace: "shop", UID: types.UID("deployment-uid"),
	}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "checkout-123", Namespace: "shop", UID: types.UID("replicaset-uid"),
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID,
			Controller: ptr.To(true),
		}},
	}}
	split := validControllerSplit()
	split.Status = api.KafkaSplitStatus{
		ActiveGeneration: 2,
		AdmissionMode:    api.KafkaAdmissionShadow,
		ApplicationEnv:   map[string]string{"ORDERS_TOPIC": "tp.orders.app", "KAFKA_GROUP": "tp.checkout.app"},
		Workloads: []api.WorkloadReference{{
			APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID,
		}},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, replicaSet, split).Build()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-123-abc", Namespace: "shop",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSet.Name, UID: replicaSet.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Env: []corev1.EnvVar{{Name: "ORDERS_TOPIC", Value: "orders"}},
		}}},
	}
	raw, err := json.Marshal(pod)
	require.NoError(t, err)
	response := (podMutator{reader: reader}).Handle(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Namespace: "shop", Object: runtime.RawExtension{Raw: raw},
	}})
	require.True(t, response.Allowed, response.Result)
	require.NotEmpty(t, response.Patches)
}

func TestPodMutatorExcludesProviderNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "tp-kafka-0", Namespace: "ambassador"}}
	raw, err := json.Marshal(pod)
	require.NoError(t, err)
	response := (podMutator{reader: reader, providerNamespace: "ambassador"}).Handle(
		t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
			Namespace: "ambassador", Object: runtime.RawExtension{Raw: raw},
		}},
	)
	require.True(t, response.Allowed, response.Result)
	require.Empty(t, response.Patches)
}

func TestGenerationZeroAcceptsOtherActiveSplits(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		runtimeconfig.ActiveAnnotation: `{"other":"3"}`,
	}}}
	require.True(t, podHasGeneration(pod, "ours", 0))
	require.False(t, podHasGeneration(pod, "other", 0))
}

func TestInvalidReplacementSpecStillDeactivatesSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, coordinationv1.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	split := validControllerSplit()
	split.Finalizers = []string{splitFinalizer}
	split.Status.ActiveGeneration = split.Generation
	split.Status.ActiveSpec = activeSpec(split)
	split.Status.Phase = api.SplitPhaseCleaningApplication
	split.Spec.DesiredState = api.DesiredStateDisabled
	split.Spec.Container = ""
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: split.Namespace}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(split).
		WithIndex(&api.KafkaRoute{}, routeSplitRefIndexField, routeSplitRefIndexer).WithObjects(split, namespace).Build()
	reconciler := &SplitReconciler{
		base: base{
			Client: client,
			OpenBroker: func(context.Context, ctrlclient.Reader, string, api.KafkaConnectionSpec) (kafkaBroker, error) {
				return fakeKafkaBroker{}, nil
			},
		},
	}
	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: split.Name, Namespace: split.Namespace}})
	require.NoError(t, err)
	got := new(api.KafkaSplit)
	require.NoError(t, client.Get(t.Context(), types.NamespacedName{Name: split.Name, Namespace: split.Namespace}, got))
	require.Equal(t, api.SplitPhaseDisabled, got.Status.Phase)
	require.Nil(t, got.Status.ActiveSpec)
}

func TestRouteOverlapHasDeterministicWinner(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	created := metav1.NewTime(time.Now())
	older := validControllerRoute("a", created)
	newer := validControllerRoute("b", metav1.NewTime(created.Add(time.Second)))
	client := fake.NewClientBuilder().WithScheme(scheme).WithIndex(&api.KafkaRoute{}, routeSplitRefIndexField, routeSplitRefIndexer).
		WithObjects(older, newer).Build()
	reconciler := &RouteReconciler{base: base{Client: client}}
	require.NoError(t, reconciler.rejectOverlap(t.Context(), older))
	require.ErrorContains(t, reconciler.rejectOverlap(t.Context(), newer), "KafkaRoute a")
}

func TestSplitBindingCollisionHasDeterministicWinner(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	created := metav1.NewTime(time.Now())
	first := validControllerSplit()
	first.Name = "a"
	first.UID = types.UID("a")
	first.CreationTimestamp = created
	first.Status.Workloads = []api.WorkloadReference{{UID: types.UID("workload")}}
	second := first.DeepCopy()
	second.Name = "b"
	second.UID = types.UID("b")
	second.CreationTimestamp = metav1.NewTime(created.Add(time.Second))
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(first, second).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}

	require.NoError(t, reconciler.validateSplitComposition(t.Context(), first, first.Status.Workloads))
	require.ErrorContains(t, reconciler.validateSplitComposition(t.Context(), second, second.Status.Workloads), "KafkaSplit a")
}

func TestSplitCompositionResolvesFreshWorkloadsWhenStatusEmpty(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, argorollouts.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "checkout", Namespace: "shop", UID: types.UID("deployment-uid"), Labels: map[string]string{"app": "checkout"},
	}}
	created := metav1.NewTime(time.Now())
	first := validControllerSplit()
	first.Name = "a"
	first.UID = types.UID("a")
	first.CreationTimestamp = created
	second := first.DeepCopy()
	second.Name = "b"
	second.UID = types.UID("b")
	second.CreationTimestamp = metav1.NewTime(created.Add(time.Second))
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, first, second).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}

	firstWorkloads, err := reconciler.resolveWorkloads(t.Context(), first)
	require.NoError(t, err)
	require.NoError(t, reconciler.validateSplitComposition(t.Context(), first, firstWorkloads))

	secondWorkloads, err := reconciler.resolveWorkloads(t.Context(), second)
	require.NoError(t, err)
	require.ErrorContains(t, reconciler.validateSplitComposition(t.Context(), second, secondWorkloads), "KafkaSplit a")
}

func TestRoutingUpdatesDoNotRollSplitter(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	split := validControllerSplit()
	split.UID = types.UID("split-uid")
	prepared := broker.Prepared{
		Names:             broker.ResourceNames(split, split.Spec.Source.Group),
		ApplicationTopics: map[string]string{"orders": "tp.orders.app"},
		ApplicationGroup:  "tp.orders.group",
	}
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &SplitReconciler{base: base{Client: client, ProviderNamespace: "ambassador"}}
	generation, err := reconciler.ensureProviderResources(t.Context(), split, prepared, kafkaintercept.RoutingTable{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), generation)
	configMap := new(corev1.ConfigMap)
	key := types.NamespacedName{Namespace: "ambassador", Name: prepared.Names.KubernetesName}
	require.NoError(t, client.Get(t.Context(), key, configMap))
	config := configMap.Data[runtimeconfig.ConfigDataKey]
	service := new(corev1.Service)
	require.NoError(t, client.Get(t.Context(), key, service))
	require.Equal(t, corev1.ClusterIPNone, service.Spec.ClusterIP)
	require.Equal(t, prepared.Names.KubernetesName, service.Spec.Selector[runtimeconfig.SplitLabel])

	table := kafkaintercept.RoutingTable{Routes: []kafkaintercept.Route{{
		ID: "alice", Predicate: kafkaintercept.Predicate{Headers: map[string][]byte{"tenant": []byte("blue")}},
		Topics: map[string]string{"orders": "tp.orders.alice"},
	}}}
	generation, err = reconciler.ensureProviderResources(t.Context(), split, prepared, table)
	require.NoError(t, err)
	require.Equal(t, uint64(2), generation)
	require.NoError(t, client.Get(t.Context(), key, configMap))
	require.Equal(t, config, configMap.Data[runtimeconfig.ConfigDataKey])
}

func TestReplacePodsReportsBlockedEviction(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, policyv1.AddToScheme(scheme))
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: types.UID("deployment-uid")},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-abc", Namespace: "shop", Labels: map[string]string{"app": "checkout"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID, Controller: ptr.To(true),
			}},
		},
	}
	blocked := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, pod.Name, fmt.Errorf("disruption budget"))
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, pod).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceCreate: func(
				context.Context, ctrlclient.Client, string, ctrlclient.Object, ctrlclient.Object, ...ctrlclient.SubResourceCreateOption,
			) error {
				return blocked
			},
		}).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}
	split := validControllerSplit()
	split.Status.Workloads = []api.WorkloadReference{{
		APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID,
	}}

	restored, message, err := reconciler.replacePods(t.Context(), split, 1, 1)
	require.NoError(t, err)
	require.False(t, restored)
	require.Contains(t, message, "blocked")

	got := new(corev1.Pod)
	require.NoError(t, client.Get(t.Context(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, got))
	require.Nil(t, got.DeletionTimestamp)
}

func TestMembersAcknowledgedPublishesPersistedStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, coordinationv1.AddToScheme(scheme))
	now := metav1.NewMicroTime(time.Now())
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name: "provider-0", Namespace: "ambassador",
			Labels:      map[string]string{runtimeconfig.SplitLabel: "provider"},
			Annotations: map[string]string{runtimeconfig.GenerationAnnotation: "3", runtimeconfig.HealthyAnnotation: "true"},
		},
		Spec: coordinationv1.LeaseSpec{RenewTime: &now},
	}
	reconciler := &SplitReconciler{base: base{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(lease).Build()}}
	split := validControllerSplit()

	acknowledged, err := reconciler.membersAcknowledged(t.Context(), split, "provider", 3, 1)
	require.NoError(t, err)
	require.True(t, acknowledged)
	require.Len(t, split.Status.Members, 1)
	require.Equal(t, "provider-0", split.Status.Members[0].Name)
	require.Equal(t, int64(3), split.Status.Members[0].Generation)
	require.True(t, split.Status.Members[0].Healthy)
	require.WithinDuration(t, now.Time, split.Status.Members[0].LastSeen.Time, time.Millisecond)
}

func TestMembersAcknowledgedDeletesLeasesRetiredByReplicaReduction(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, coordinationv1.AddToScheme(scheme))
	now := metav1.NewMicroTime(time.Now())
	var leases []ctrlclient.Object
	for ordinal := range 3 {
		leases = append(leases, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("provider-%d", ordinal), Namespace: "ambassador",
				Labels:      map[string]string{runtimeconfig.SplitLabel: "provider"},
				Annotations: map[string]string{runtimeconfig.GenerationAnnotation: "3", runtimeconfig.HealthyAnnotation: "true"},
			},
			Spec: coordinationv1.LeaseSpec{RenewTime: &now},
		})
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(leases...).Build()
	reconciler := &SplitReconciler{base: base{Client: client}}
	split := validControllerSplit()

	acknowledged, err := reconciler.membersAcknowledged(t.Context(), split, "provider", 3, 2)
	require.NoError(t, err)
	require.True(t, acknowledged)
	require.Len(t, split.Status.Members, 2)
	require.Equal(t, "provider-0", split.Status.Members[0].Name)
	require.Equal(t, "provider-1", split.Status.Members[1].Name)

	retired := new(coordinationv1.Lease)
	err = client.Get(t.Context(), types.NamespacedName{Name: "provider-2", Namespace: "ambassador"}, retired)
	require.True(t, apierrors.IsNotFound(err))
}

func TestRouteProvisioningContinuesDuringAcknowledgement(t *testing.T) {
	require.True(t, api.SplitPhaseEnabled.AcceptsRoutes())
	require.True(t, api.SplitPhaseStarting.AcceptsRoutes())
	require.False(t, api.SplitPhasePreparing.AcceptsRoutes())
	require.False(t, api.SplitPhaseClosingRoutes.AcceptsRoutes())
}

func TestFinishClosingRouteRequiresFreshMemberAcknowledgement(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, coordinationv1.AddToScheme(scheme))
	routing, err := json.Marshal(kafkaintercept.RoutingTable{Generation: 2})
	require.NoError(t, err)
	now := metav1.NewMicroTime(time.Now())
	split := validControllerSplit()
	split.Status.SplitterName = "provider"
	route := validControllerRoute("alice", metav1.Now())
	route.Generation = 1
	route.Status.Phase = api.RoutePhaseCleaning
	route.Status.Resources = []api.KafkaResourceStatus{{Kind: "SessionTopic", Name: "alice", Managed: true}}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "provider", Namespace: "ambassador"},
		Data:       map[string]string{runtimeconfig.RoutingDataKey: string(routing)},
	}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name: "provider-0", Namespace: "ambassador",
			Labels:      map[string]string{runtimeconfig.SplitLabel: "provider"},
			Annotations: map[string]string{runtimeconfig.GenerationAnnotation: "1", runtimeconfig.HealthyAnnotation: "true"},
		},
		Spec: coordinationv1.LeaseSpec{RenewTime: &now},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(route).
		WithObjects(route, configMap, lease).Build()
	tracker := new(deleteTrackingBroker)
	reconciler := &RouteReconciler{
		base: base{
			Client: client, ProviderNamespace: "ambassador",
			OpenBroker: func(context.Context, ctrlclient.Reader, string, api.KafkaConnectionSpec) (kafkaBroker, error) {
				return tracker, nil
			},
		},
	}

	before := route.Status.DeepCopy()
	_, err = reconciler.finishClosingRoute(t.Context(), route, before, split)
	require.NoError(t, err)
	require.False(t, tracker.deleted)
	require.Equal(t, "AwaitingAcknowledgement", route.Status.Conditions[len(route.Status.Conditions)-1].Reason)
}

func TestFinishClosingRouteDrainsResidueAfterAcknowledgement(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, coordinationv1.AddToScheme(scheme))
	routing, err := json.Marshal(kafkaintercept.RoutingTable{Generation: 2})
	require.NoError(t, err)
	now := metav1.NewMicroTime(time.Now())
	split := validControllerSplit()
	split.Status.SplitterName = "provider"
	route := validControllerRoute("alice", metav1.Now())
	route.Generation = 1
	route.Status.Phase = api.RoutePhaseCleaning
	route.Status.Group = "alice-group"
	route.Status.Topics = map[string]string{"orders": "alice-topic"}
	route.Status.Resources = []api.KafkaResourceStatus{{Kind: "SessionTopic", Name: "alice-topic", Managed: true}}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "provider", Namespace: "ambassador"},
		Data:       map[string]string{runtimeconfig.RoutingDataKey: string(routing)},
	}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name: "provider-0", Namespace: "ambassador",
			Labels:      map[string]string{runtimeconfig.SplitLabel: "provider"},
			Annotations: map[string]string{runtimeconfig.GenerationAnnotation: "2", runtimeconfig.HealthyAnnotation: "true"},
		},
		Spec: coordinationv1.LeaseSpec{RenewTime: &now},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(route).
		WithObjects(route, configMap, lease).Build()
	tracker := new(deleteTrackingBroker)
	reconciler := &RouteReconciler{
		base: base{
			Client: client, ProviderNamespace: "ambassador",
			OpenBroker: func(context.Context, ctrlclient.Reader, string, api.KafkaConnectionSpec) (kafkaBroker, error) {
				return tracker, nil
			},
		},
	}

	before := route.Status.DeepCopy()
	_, err = reconciler.finishClosingRoute(t.Context(), route, before, split)
	require.NoError(t, err)
	require.Equal(t, 1, tracker.drains)
	require.True(t, tracker.deleted)
}

func TestExpiredClosedRouteIsDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	route := validControllerRoute("expired", metav1.Now())
	route.Finalizers = []string{routeFinalizer}
	route.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(-time.Minute))
	route.Status.Phase = api.RoutePhaseClosed
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(route).WithObjects(route).Build()
	reconciler := &RouteReconciler{base: base{Client: client}}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name}}

	result, err := reconciler.Reconcile(t.Context(), request)
	require.NoError(t, err)
	require.Positive(t, result.RequeueAfter)
	_, err = reconciler.Reconcile(t.Context(), request)
	require.NoError(t, err)

	got := new(api.KafkaRoute)
	err = client.Get(t.Context(), request.NamespacedName, got)
	require.True(t, apierrors.IsNotFound(err))
}

func validControllerRoute(name string, created metav1.Time) *api.KafkaRoute {
	return &api.KafkaRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", CreationTimestamp: created},
		Spec: api.KafkaRouteSpec{
			SplitRef: corev1.LocalObjectReference{Name: "checkout"}, AttachmentID: name, SessionID: "session",
			ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)), DesiredState: api.RouteStateActive,
			Predicate: api.KafkaRoutePredicate{Headers: []api.KafkaHeaderMatch{{Name: "tenant", Value: []byte("blue")}}},
		},
	}
}

type fakeKafkaBroker struct{}

func (fakeKafkaBroker) Prepare(context.Context, *api.KafkaSplit) (broker.Prepared, error) {
	return broker.Prepared{}, nil
}

func (fakeKafkaBroker) EnsureSession(context.Context, *api.KafkaSplit, *api.KafkaRoute) (broker.Session, error) {
	return broker.Session{}, nil
}

func (fakeKafkaBroker) GroupMemberless(context.Context, string) (bool, error)  { return true, nil }
func (fakeKafkaBroker) GroupMembers(context.Context, string) ([]string, error) { return nil, nil }
func (fakeKafkaBroker) Remaining(context.Context, string, map[string]string) (int64, error) {
	return 0, nil
}

func (fakeKafkaBroker) DrainSession(context.Context, *api.KafkaSplit, string, string, map[string]string, map[string]string) error {
	return nil
}

func (fakeKafkaBroker) DeleteManaged(context.Context, []api.KafkaResourceStatus) error { return nil }
func (fakeKafkaBroker) Close()                                                         {}

type deleteTrackingBroker struct {
	fakeKafkaBroker
	deleted bool
	drains  int
}

func (b *deleteTrackingBroker) DrainSession(context.Context, *api.KafkaSplit, string, string, map[string]string, map[string]string) error {
	b.drains++
	return nil
}

func (b *deleteTrackingBroker) DeleteManaged(context.Context, []api.KafkaResourceStatus) error {
	b.deleted = true
	return nil
}

func validControllerSplit() *api.KafkaSplit {
	return &api.KafkaSplit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", Generation: 1},
		Spec: api.KafkaSplitSpec{
			DesiredState:     api.DesiredStateEnabled,
			WorkloadSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			Container:        "app",
			Connection:       api.KafkaConnectionSpec{BootstrapServers: []string{"kafka:9092"}},
			Source:           api.KafkaSourceSpec{Group: "checkout", Topics: []string{"orders"}, OffsetReset: "earliest"},
			Application: api.KafkaApplicationSpec{
				TopicEnv: "ORDERS_TOPIC", TopicSeparator: ",", GroupEnv: "KAFKA_GROUP", IsolationLevelEnv: "KAFKA_ISOLATION_LEVEL",
			},
			Shadows: api.KafkaShadowSpec{Mode: api.ShadowModeManaged, Managed: &api.KafkaManagedShadows{}},
		},
	}
}
