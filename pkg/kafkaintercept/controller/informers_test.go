package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	fakectrlclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

// newTestNamespaceInformers wires a NamespaceInformers over a fake Namespace
// informer and a fake joined clientset, and starts it in the background.
func newTestNamespaceInformers(
	t *testing.T,
	kube *kubefake.Clientset,
	apiReader ctrlclient.Reader,
) (*NamespaceInformers, *controllertest.FakeInformer) {
	t.Helper()
	// The joined clientset hides the fake's WatchList marker method, so the
	// gate must be off for informers over the fake clientset to sync.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	t.Helper()
	fakeCache := &informertest.FakeInformers{}
	nsInformer, err := fakeCache.FakeInformerFor(t.Context(), &corev1.Namespace{})
	require.NoError(t, err)

	ni := NewNamespaceInformers(fakeCache, apiReader, kube, argorolloutsfake.NewSimpleClientset())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = ni.Start(ctx) }()
	return ni, nsInformer
}

// syncNamespace labels namespace ns and waits for its informers to sync,
// retrying the Add event until the handler that Start registers picks it up.
func syncNamespace(t *testing.T, ni *NamespaceInformers, nsInformer *controllertest.FakeInformer, ns string) {
	t.Helper()
	labeled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: ns, Labels: map[string]string{runtimeconfig.NamespaceLabel: "true"},
	}}
	require.Eventually(t, func() bool {
		nsInformer.Add(labeled)
		return ni.syncedEntry(ns) != nil
	}, 2*time.Second, 5*time.Millisecond, "namespace %s never synced", ns)
}

func TestNamespaceInformersFallsBackBeforeLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	fallback := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: types.UID("api")}}
	apiReader := fakectrlclient.NewClientBuilder().WithScheme(scheme).WithObjects(fallback).Build()
	kube := kubefake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: types.UID("informer")},
	})
	ni, _ := newTestNamespaceInformers(t, kube, apiReader)

	got := new(appsv1.Deployment)
	require.NoError(t, ni.Get(t.Context(), ctrlclient.ObjectKey{Namespace: "shop", Name: "checkout"}, got))
	require.Equal(t, types.UID("api"), got.UID)
}

func TestNamespaceInformersServesFromListerOnceSynced(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	fallback := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: types.UID("api")}}
	apiReader := fakectrlclient.NewClientBuilder().WithScheme(scheme).WithObjects(fallback).Build()
	kube := kubefake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: types.UID("informer")},
	})
	ni, nsInformer := newTestNamespaceInformers(t, kube, apiReader)
	syncNamespace(t, ni, nsInformer, "shop")

	got := new(appsv1.Deployment)
	require.NoError(t, ni.Get(t.Context(), ctrlclient.ObjectKey{Namespace: "shop", Name: "checkout"}, got))
	require.Equal(t, types.UID("informer"), got.UID)

	list := new(appsv1.DeploymentList)
	require.NoError(t, ni.List(t.Context(), list, ctrlclient.InNamespace("shop")))
	require.Len(t, list.Items, 1)
	require.Equal(t, types.UID("informer"), list.Items[0].UID)
}

func TestNamespaceInformersDropsFactoryWhenLabelRemoved(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	fallback := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: types.UID("api")}}
	apiReader := fakectrlclient.NewClientBuilder().WithScheme(scheme).WithObjects(fallback).Build()
	kube := kubefake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: types.UID("informer")},
	})
	ni, nsInformer := newTestNamespaceInformers(t, kube, apiReader)
	syncNamespace(t, ni, nsInformer, "shop")

	labeled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "shop", Labels: map[string]string{runtimeconfig.NamespaceLabel: "true"},
	}}
	unlabeled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}
	nsInformer.Update(labeled, unlabeled)

	require.Eventually(t, func() bool {
		return ni.syncedEntry("shop") == nil
	}, 2*time.Second, 5*time.Millisecond, "namespace informers were not dropped")

	got := new(appsv1.Deployment)
	require.NoError(t, ni.Get(t.Context(), ctrlclient.ObjectKey{Namespace: "shop", Name: "checkout"}, got))
	require.Equal(t, types.UID("api"), got.UID)
}

func TestNamespaceInformersFallsBackWhenObjectKindHasNoLister(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	fallback := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "shop"}}
	apiReader := fakectrlclient.NewClientBuilder().WithScheme(scheme).WithObjects(fallback).Build()
	kube := kubefake.NewClientset()
	ni, nsInformer := newTestNamespaceInformers(t, kube, apiReader)
	syncNamespace(t, ni, nsInformer, "shop")

	got := new(corev1.ConfigMap)
	require.NoError(t, ni.Get(t.Context(), ctrlclient.ObjectKey{Namespace: "shop", Name: "cm"}, got))
	require.Equal(t, "cm", got.Name)
}

func TestNamespaceInformersGetNotFoundFromListerIsNotAFallbackTrigger(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	// The API reader has a Deployment that the informer cache does not, so a
	// NotFound from the synced lister must not be papered over by falling
	// back to the API reader.
	fallback := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop"}}
	apiReader := fakectrlclient.NewClientBuilder().WithScheme(scheme).WithObjects(fallback).Build()
	kube := kubefake.NewClientset()
	ni, nsInformer := newTestNamespaceInformers(t, kube, apiReader)
	syncNamespace(t, ni, nsInformer, "shop")

	got := new(appsv1.Deployment)
	err := ni.Get(t.Context(), ctrlclient.ObjectKey{Namespace: "shop", Name: "checkout"}, got)
	require.True(t, apierrors.IsNotFound(err), "expected NotFound, got %v", err)
}

func TestNamespaceInformersPodSinkOnlyOnMeaningfulChange(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	apiReader := fakectrlclient.NewClientBuilder().WithScheme(scheme).Build()
	kube := kubefake.NewClientset()
	ni, nsInformer := newTestNamespaceInformers(t, kube, apiReader)
	syncNamespace(t, ni, nsInformer, "shop")

	var mu sync.Mutex
	var seen []string
	ni.SetPodSink(func(pod *corev1.Pod) {
		mu.Lock()
		seen = append(seen, pod.Name)
		mu.Unlock()
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(seen)
	}

	created, err := kube.CoreV1().Pods("shop").Create(
		t.Context(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "checkout-1", Namespace: "shop"}}, metav1.CreateOptions{},
	)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return count() == 1 }, 2*time.Second, 5*time.Millisecond, "add did not reach the sink")

	// An update that changes neither readiness, deletion, nor the active
	// annotation must not reach the sink.
	unchanged := created.DeepCopy()
	unchanged.Labels = map[string]string{"unrelated": "true"}
	unchanged, err = kube.CoreV1().Pods("shop").Update(t.Context(), unchanged, metav1.UpdateOptions{})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, count(), "an update that does not affect activity must not reach the sink")

	ready := unchanged.DeepCopy()
	ready.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	_, err = kube.CoreV1().Pods("shop").UpdateStatus(t.Context(), ready, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return count() == 2 }, 2*time.Second, 5*time.Millisecond, "readiness change did not reach the sink")

	require.NoError(t, kube.CoreV1().Pods("shop").Delete(t.Context(), created.Name, metav1.DeleteOptions{}))
	require.Eventually(t, func() bool { return count() == 3 }, 2*time.Second, 5*time.Millisecond, "delete did not reach the sink")

	ni.SetPodSink(nil)
	_, err = kube.CoreV1().Pods("shop").Create(t.Context(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-2", Namespace: "shop"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, 3, count(), "unregistered sink must not be called")
}

func TestPodActivityChanged(t *testing.T) {
	base := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	require.False(t, podActivityChanged(base, base.DeepCopy()))

	readyChange := base.DeepCopy()
	readyChange.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	require.True(t, podActivityChanged(base, readyChange))

	deletionChange := base.DeepCopy()
	now := metav1.Now()
	deletionChange.DeletionTimestamp = &now
	require.True(t, podActivityChanged(base, deletionChange))

	annotationChange := base.DeepCopy()
	annotationChange.Annotations = map[string]string{runtimeconfig.ActiveAnnotation: `{"a":"1"}`}
	require.True(t, podActivityChanged(base, annotationChange))
}
