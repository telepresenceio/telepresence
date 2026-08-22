package controller

import (
	"encoding/json"
	"testing"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

func TestSplitReconcilerSnapshotsWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, argorollouts.AddToScheme(scheme))
	require.NoError(t, api.AddToScheme(scheme))
	split := validControllerSplit()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "checkout", Namespace: "shop", UID: types.UID("deployment-uid"), Labels: map[string]string{"app": "checkout"},
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(split).WithObjects(split, deployment).Build()
	reconciler := &SplitReconciler{Client: client}
	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "checkout", Namespace: "shop"}})
	require.NoError(t, err)

	got := new(api.KafkaSplit)
	require.NoError(t, client.Get(t.Context(), types.NamespacedName{Name: "checkout", Namespace: "shop"}, got))
	require.Equal(t, "Preparing", got.Status.Phase)
	require.Equal(t, []api.WorkloadReference{{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "checkout", UID: types.UID("deployment-uid"),
	}}, got.Status.Workloads)
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
			Controller: ptrTo(true),
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
				Controller: ptrTo(true),
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

func ptrTo[T any](value T) *T {
	return &value
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
