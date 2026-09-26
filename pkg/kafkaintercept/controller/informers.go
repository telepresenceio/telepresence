package controller

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	argorolloutsclientset "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"
	argorolloutslisters "github.com/datawire/argo-rollouts-go-client/pkg/client/listers/rollouts/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

// factoryInitNamespace is passed to informer.WithFactory only to install its
// namespace map without building the cluster-wide factory it builds eagerly
// when called with an empty namespace.
const factoryInitNamespace = "-"

// PodSource lets a leader-only controller receive Pod events observed by the
// namespace-scoped informers, in place of a cluster-wide Pod watch.
type PodSource interface {
	// SetPodSink registers sink to receive every Pod add, delete, and
	// consequential update across the managed namespaces. A nil sink
	// unregisters it.
	SetPodSink(sink func(pod *corev1.Pod))
}

// namespaceEntry holds the listers and lifecycle for one namespace's
// per-namespace informers.
type namespaceEntry struct {
	cancel context.CancelFunc
	synced atomic.Bool

	deployments  appslisters.DeploymentLister
	replicaSets  appslisters.ReplicaSetLister
	statefulSets appslisters.StatefulSetLister
	pods         corelisters.PodLister
	rollouts     argorolloutslisters.RolloutLister
}

// NamespaceInformers watches Namespaces labeled with
// runtimeconfig.NamespaceLabel and, for each one, starts Deployment,
// ReplicaSet, StatefulSet, Pod, and (when the CRD is installed) Argo Rollout
// informers scoped to that namespace. It implements client.Reader, serving
// Deployment, ReplicaSet, StatefulSet, Rollout, and Pod reads from those
// informers once a namespace has synced and falling back to a direct API
// read otherwise, and it implements PodSource so the split reconciler can
// receive Pod events without a cluster-wide watch. It runs on every replica,
// leader and standby alike, because the admission webhook needs it on both.
type NamespaceInformers struct {
	cache     ctrlcache.Cache
	apiReader client.Reader
	kube      kubernetes.Interface
	argo      argorolloutsclientset.Interface

	mu       sync.Mutex
	ctx      context.Context
	entries  map[string]*namespaceEntry
	rollouts bool

	podSinkMu sync.RWMutex
	podSink   func(*corev1.Pod)
}

var (
	_ manager.Runnable               = (*NamespaceInformers)(nil)
	_ manager.LeaderElectionRunnable = (*NamespaceInformers)(nil)
	_ client.Reader                  = (*NamespaceInformers)(nil)
	_ PodSource                      = (*NamespaceInformers)(nil)
)

// NewNamespaceInformers returns a NamespaceInformers that reads the
// Namespace informer from cache, builds per-namespace informers from kube
// and argo, and falls back to apiReader for namespaces that are not (yet)
// labeled or synced.
func NewNamespaceInformers(
	cache ctrlcache.Cache, apiReader client.Reader, kube kubernetes.Interface, argo argorolloutsclientset.Interface,
) *NamespaceInformers {
	return &NamespaceInformers{
		cache:     cache,
		apiReader: apiReader,
		kube:      kube,
		argo:      argo,
		entries:   make(map[string]*namespaceEntry),
	}
}

// NeedLeaderElection reports that NamespaceInformers must run on every
// replica, not just the leader.
func (n *NamespaceInformers) NeedLeaderElection() bool {
	return false
}

// Start watches Namespaces and starts or stops per-namespace informers as
// the runtimeconfig.NamespaceLabel is added to or removed from them. It
// blocks until ctx is done.
func (n *NamespaceInformers) Start(ctx context.Context) error {
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, n.kube, n.argo)
	ctx = informer.WithFactory(ctx, factoryInitNamespace)
	n.mu.Lock()
	n.ctx = ctx
	n.mu.Unlock()
	n.rollouts = n.probeRollouts(ctx)

	nsInformer, err := n.cache.GetInformer(ctx, &corev1.Namespace{})
	if err != nil {
		return fmt.Errorf("get Namespace informer: %w", err)
	}
	_, err = nsInformer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { n.handleNamespace(ctx, obj) },
		UpdateFunc: func(_, obj any) { n.handleNamespace(ctx, obj) },
		DeleteFunc: func(obj any) { n.handleNamespaceGone(obj) },
	})
	if err != nil {
		return fmt.Errorf("watch Namespaces: %w", err)
	}
	<-ctx.Done()
	return nil
}

// probeRollouts reports whether the Argo Rollouts CRD is installed, so
// namespace informers only watch Rollouts when the cluster supports it.
func (n *NamespaceInformers) probeRollouts(ctx context.Context) bool {
	_, err := n.argo.ArgoprojV1alpha1().Rollouts("").List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).Error(err, "checking Argo Rollouts availability")
		}
		return false
	}
	return true
}

func (n *NamespaceInformers) handleNamespace(ctx context.Context, obj any) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return
	}
	if ns.Labels[runtimeconfig.NamespaceLabel] != "true" {
		n.stopNamespace(ns.Name)
		return
	}
	n.startNamespace(ctx, ns.Name)
}

func (n *NamespaceInformers) handleNamespaceGone(obj any) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		tombstone, ok2 := obj.(toolscache.DeletedFinalStateUnknown)
		if !ok2 {
			return
		}
		if ns, ok = tombstone.Obj.(*corev1.Namespace); !ok {
			return
		}
	}
	n.stopNamespace(ns.Name)
}

// startNamespace starts the namespace's informers unless they are already
// running.
func (n *NamespaceInformers) startNamespace(ctx context.Context, ns string) {
	n.mu.Lock()
	if _, ok := n.entries[ns]; ok {
		n.mu.Unlock()
		return
	}
	nsCtx, cancel := context.WithCancel(ctx)
	entry := &namespaceEntry{cancel: cancel}
	n.entries[ns] = entry
	n.mu.Unlock()

	k8sFactory := informer.GetK8sFactory(nsCtx, ns)
	workload.StartDeployments(nsCtx, ns)
	workload.StartReplicaSets(nsCtx, ns)
	workload.StartStatefulSets(nsCtx, ns)
	pods := k8sFactory.Core().V1().Pods().Informer()
	_ = pods.SetWatchErrorHandler(func(_ *toolscache.Reflector, err error) {
		ctrl.LoggerFrom(ctx).Error(err, "watch Pods", "namespace", ns)
	})
	if _, err := pods.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    n.dispatchPod,
		UpdateFunc: n.dispatchPodUpdate,
		DeleteFunc: n.dispatchPod,
	}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "add Pod event handler", "namespace", ns)
	}

	entry.deployments = k8sFactory.Apps().V1().Deployments().Lister()
	entry.replicaSets = k8sFactory.Apps().V1().ReplicaSets().Lister()
	entry.statefulSets = k8sFactory.Apps().V1().StatefulSets().Lister()
	entry.pods = k8sFactory.Core().V1().Pods().Lister()

	var waitArgoSynced func() bool
	if n.rollouts {
		workload.StartRollouts(nsCtx, ns)
		argoFactory := informer.GetArgoRolloutsFactory(nsCtx, ns)
		entry.rollouts = argoFactory.Argoproj().V1alpha1().Rollouts().Lister()
		argoFactory.Start(nsCtx.Done())
		waitArgoSynced = func() bool { return allSynced(argoFactory.WaitForCacheSync(nsCtx.Done())) }
	}
	k8sFactory.Start(nsCtx.Done())

	go func() {
		synced := allSynced(k8sFactory.WaitForCacheSync(nsCtx.Done()))
		if synced && waitArgoSynced != nil {
			synced = waitArgoSynced()
		}
		if synced {
			entry.synced.Store(true)
		} else if nsCtx.Err() == nil {
			ctrl.LoggerFrom(ctx).Info("namespace informers did not sync", "namespace", ns)
		}
	}()
}

// stopNamespace cancels the namespace's informers and drops its factory, if
// it has one running.
func (n *NamespaceInformers) stopNamespace(ns string) {
	n.mu.Lock()
	entry, ok := n.entries[ns]
	if ok {
		delete(n.entries, ns)
	}
	baseCtx := n.ctx
	n.mu.Unlock()
	if !ok {
		return
	}
	entry.cancel()
	if baseCtx != nil {
		informer.DropFactory(baseCtx, ns)
	}
}

func allSynced(synced map[reflect.Type]bool) bool {
	for _, ok := range synced {
		if !ok {
			return false
		}
	}
	return true
}

// syncedEntry returns the namespace's entry, or nil if it has none or its
// informers have not synced yet.
func (n *NamespaceInformers) syncedEntry(namespace string) *namespaceEntry {
	n.mu.Lock()
	entry := n.entries[namespace]
	n.mu.Unlock()
	if entry == nil || !entry.synced.Load() {
		return nil
	}
	return entry
}

// Get serves obj from the namespace's informers once they have synced, and
// falls back to a direct API read otherwise.
func (n *NamespaceInformers) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	entry := n.syncedEntry(key.Namespace)
	if entry == nil {
		return n.apiReader.Get(ctx, key, obj, opts...)
	}
	switch o := obj.(type) {
	case *appsv1.Deployment:
		got, err := entry.deployments.Deployments(key.Namespace).Get(key.Name)
		if err != nil {
			return err
		}
		got.DeepCopyInto(o)
	case *appsv1.ReplicaSet:
		got, err := entry.replicaSets.ReplicaSets(key.Namespace).Get(key.Name)
		if err != nil {
			return err
		}
		got.DeepCopyInto(o)
	case *appsv1.StatefulSet:
		got, err := entry.statefulSets.StatefulSets(key.Namespace).Get(key.Name)
		if err != nil {
			return err
		}
		got.DeepCopyInto(o)
	case *corev1.Pod:
		got, err := entry.pods.Pods(key.Namespace).Get(key.Name)
		if err != nil {
			return err
		}
		got.DeepCopyInto(o)
	case *argorollouts.Rollout:
		if entry.rollouts == nil {
			return n.apiReader.Get(ctx, key, obj, opts...)
		}
		got, err := entry.rollouts.Rollouts(key.Namespace).Get(key.Name)
		if err != nil {
			return err
		}
		got.DeepCopyInto(o)
	default:
		return n.apiReader.Get(ctx, key, obj, opts...)
	}
	return nil
}

// List serves list from the namespace's informers once they have synced,
// and falls back to a direct API read otherwise. Every call site lists a
// single namespace; a namespace-less list always falls back.
func (n *NamespaceInformers) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	listOpts := &client.ListOptions{}
	listOpts.ApplyOptions(opts)
	entry := n.syncedEntry(listOpts.Namespace)
	if entry == nil {
		return n.apiReader.List(ctx, list, opts...)
	}
	selector := listOpts.LabelSelector
	if selector == nil {
		selector = labels.Everything()
	}
	switch l := list.(type) {
	case *appsv1.DeploymentList:
		items, err := entry.deployments.Deployments(listOpts.Namespace).List(selector)
		if err != nil {
			return err
		}
		l.Items = copyDeployments(items)
	case *appsv1.ReplicaSetList:
		items, err := entry.replicaSets.ReplicaSets(listOpts.Namespace).List(selector)
		if err != nil {
			return err
		}
		l.Items = copyReplicaSets(items)
	case *appsv1.StatefulSetList:
		items, err := entry.statefulSets.StatefulSets(listOpts.Namespace).List(selector)
		if err != nil {
			return err
		}
		l.Items = copyStatefulSets(items)
	case *corev1.PodList:
		items, err := entry.pods.Pods(listOpts.Namespace).List(selector)
		if err != nil {
			return err
		}
		l.Items = copyPods(items)
	case *argorollouts.RolloutList:
		if entry.rollouts == nil {
			return n.apiReader.List(ctx, list, opts...)
		}
		items, err := entry.rollouts.Rollouts(listOpts.Namespace).List(selector)
		if err != nil {
			return err
		}
		l.Items = copyRollouts(items)
	default:
		return n.apiReader.List(ctx, list, opts...)
	}
	return nil
}

func copyDeployments(items []*appsv1.Deployment) []appsv1.Deployment {
	out := make([]appsv1.Deployment, len(items))
	for i, item := range items {
		out[i] = *item.DeepCopy()
	}
	return out
}

func copyReplicaSets(items []*appsv1.ReplicaSet) []appsv1.ReplicaSet {
	out := make([]appsv1.ReplicaSet, len(items))
	for i, item := range items {
		out[i] = *item.DeepCopy()
	}
	return out
}

func copyStatefulSets(items []*appsv1.StatefulSet) []appsv1.StatefulSet {
	out := make([]appsv1.StatefulSet, len(items))
	for i, item := range items {
		out[i] = *item.DeepCopy()
	}
	return out
}

func copyPods(items []*corev1.Pod) []corev1.Pod {
	out := make([]corev1.Pod, len(items))
	for i, item := range items {
		out[i] = *item.DeepCopy()
	}
	return out
}

func copyRollouts(items []*argorollouts.Rollout) []argorollouts.Rollout {
	out := make([]argorollouts.Rollout, len(items))
	for i, item := range items {
		out[i] = *item.DeepCopy()
	}
	return out
}

func (n *NamespaceInformers) dispatchPod(obj any) {
	pod, ok := podFromEvent(obj)
	if !ok {
		return
	}
	n.podSinkMu.RLock()
	sink := n.podSink
	n.podSinkMu.RUnlock()
	if sink != nil {
		sink(pod)
	}
}

func (n *NamespaceInformers) dispatchPodUpdate(oldObj, newObj any) {
	oldPod, ok := oldObj.(*corev1.Pod)
	if !ok {
		return
	}
	newPod, ok := newObj.(*corev1.Pod)
	if !ok {
		return
	}
	if podActivityChanged(oldPod, newPod) {
		n.dispatchPod(newObj)
	}
}

func podFromEvent(obj any) (*corev1.Pod, bool) {
	if pod, ok := obj.(*corev1.Pod); ok {
		return pod, true
	}
	if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		pod, ok := tombstone.Obj.(*corev1.Pod)
		return pod, ok
	}
	return nil, false
}

// SetPodSink implements PodSource.
func (n *NamespaceInformers) SetPodSink(sink func(pod *corev1.Pod)) {
	n.podSinkMu.Lock()
	n.podSink = sink
	n.podSinkMu.Unlock()
}
