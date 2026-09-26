// Package controller reconciles Kafka personal intercept resources.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/broker"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

const (
	transitionInterval = 2 * time.Second
	healthInterval     = 30 * time.Second
	brokerMaxAge       = 10 * time.Minute
	splitFinalizer     = runtimeconfig.SplitFinalizer
	routeFinalizer     = runtimeconfig.RouteFinalizer
)

type kafkaBroker interface {
	Prepare(context.Context, *api.KafkaSplit) (broker.Prepared, error)
	EnsureSession(context.Context, *api.KafkaSplit, *api.KafkaRoute) (broker.Session, error)
	GroupMemberless(context.Context, string) (bool, error)
	GroupMembers(context.Context, string) ([]string, error)
	Remaining(context.Context, string, map[string]string) (int64, error)
	DrainSession(context.Context, *api.KafkaSplit, string, string, map[string]string, map[string]string) error
	DeleteManaged(context.Context, []api.KafkaResourceStatus) error
	Close()
}

type brokerOpener func(context.Context, client.Reader, string, api.KafkaConnectionSpec) (kafkaBroker, error)

func defaultBrokerOpener(
	ctx context.Context,
	reader client.Reader,
	namespace string,
	connection api.KafkaConnectionSpec,
) (kafkaBroker, error) {
	return broker.Open(ctx, reader, namespace, connection)
}

// cachedBroker holds one open broker client shared across reconciles.
type cachedBroker struct {
	broker   kafkaBroker
	specHash string
	openedAt time.Time
	stale    atomic.Bool
}

// BrokerCache shares open broker clients between the split and route reconcilers.
type BrokerCache struct {
	mu      sync.Mutex
	entries map[types.NamespacedName]*cachedBroker
}

// NewBrokerCache returns an empty broker cache.
func NewBrokerCache() *BrokerCache {
	return &BrokerCache{entries: make(map[types.NamespacedName]*cachedBroker)}
}

// cachedBrokerHandle wraps a cached broker so Close is a no-op and a failed
// operation marks the entry stale, forcing the next open to rebuild it.
type cachedBrokerHandle struct {
	entry *cachedBroker
}

func (h *cachedBrokerHandle) fail(err error) error {
	if err != nil {
		h.entry.stale.Store(true)
	}
	return err
}

func (h *cachedBrokerHandle) Prepare(ctx context.Context, split *api.KafkaSplit) (broker.Prepared, error) {
	prepared, err := h.entry.broker.Prepare(ctx, split)
	return prepared, h.fail(err)
}

func (h *cachedBrokerHandle) EnsureSession(
	ctx context.Context, split *api.KafkaSplit, route *api.KafkaRoute,
) (broker.Session, error) {
	session, err := h.entry.broker.EnsureSession(ctx, split, route)
	return session, h.fail(err)
}

func (h *cachedBrokerHandle) GroupMemberless(ctx context.Context, group string) (bool, error) {
	memberless, err := h.entry.broker.GroupMemberless(ctx, group)
	return memberless, h.fail(err)
}

func (h *cachedBrokerHandle) GroupMembers(ctx context.Context, group string) ([]string, error) {
	members, err := h.entry.broker.GroupMembers(ctx, group)
	return members, h.fail(err)
}

func (h *cachedBrokerHandle) Remaining(ctx context.Context, group string, topics map[string]string) (int64, error) {
	remaining, err := h.entry.broker.Remaining(ctx, group, topics)
	return remaining, h.fail(err)
}

func (h *cachedBrokerHandle) DrainSession(
	ctx context.Context, split *api.KafkaSplit, route, group string, topics, appTopics map[string]string,
) error {
	return h.fail(h.entry.broker.DrainSession(ctx, split, route, group, topics, appTopics))
}

func (h *cachedBrokerHandle) DeleteManaged(ctx context.Context, resources []api.KafkaResourceStatus) error {
	return h.fail(h.entry.broker.DeleteManaged(ctx, resources))
}

func (h *cachedBrokerHandle) Close() {}

// base holds the broker-opening behavior shared by both reconcilers.
type base struct {
	client.Client
	ProviderNamespace string
	OpenBroker        brokerOpener
	Brokers           *BrokerCache
}

func (b *base) brokers() *BrokerCache {
	if b.Brokers == nil {
		b.Brokers = NewBrokerCache()
	}
	return b.Brokers
}

// connectionSpecHash hashes the fields a broker client is built from, so a
// spec change is detected without comparing structs field by field.
func connectionSpecHash(connection api.KafkaConnectionSpec) (string, error) {
	data, err := json.Marshal(connection)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// openBroker returns a cached broker client for split, opening and caching a
// new one when none is cached, the connection spec changed, the cached
// client was marked stale by a failed operation, or it has aged past
// brokerMaxAge.
func (b *base) openBroker(ctx context.Context, split *api.KafkaSplit) (kafkaBroker, error) {
	key := types.NamespacedName{Namespace: split.Namespace, Name: split.Name}
	hash, err := connectionSpecHash(split.Spec.Connection)
	if err != nil {
		return nil, err
	}
	cache := b.brokers()
	cache.mu.Lock()
	entry, ok := cache.entries[key]
	cache.mu.Unlock()
	if ok && entry.specHash == hash && !entry.stale.Load() && time.Since(entry.openedAt) < brokerMaxAge {
		return &cachedBrokerHandle{entry: entry}, nil
	}

	opener := b.OpenBroker
	if opener == nil {
		opener = defaultBrokerOpener
	}
	opened, err := opener(ctx, b.Client, split.Namespace, split.Spec.Connection)
	if err != nil {
		return nil, err
	}
	entry = &cachedBroker{broker: opened, specHash: hash, openedAt: time.Now()}

	cache.mu.Lock()
	if old, ok := cache.entries[key]; ok {
		old.broker.Close()
	}
	cache.entries[key] = entry
	cache.mu.Unlock()
	return &cachedBrokerHandle{entry: entry}, nil
}

// closeBroker closes and drops the cached broker client for key, if any.
func (b *base) closeBroker(key types.NamespacedName) {
	cache := b.brokers()
	cache.mu.Lock()
	entry, ok := cache.entries[key]
	delete(cache.entries, key)
	cache.mu.Unlock()
	if ok {
		entry.broker.Close()
	}
}

func (b *base) providerNamespace() string {
	if b.ProviderNamespace != "" {
		return b.ProviderNamespace
	}
	return "ambassador"
}

// SplitReconciler owns the workload cutover and splitter lifecycle.
type SplitReconciler struct {
	base
	ProviderImage  string
	ServiceAccount string
}

// SetupWithManager registers the KafkaSplit controller.
func (r *SplitReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&api.KafkaSplit{}).
		Watches(&api.KafkaRoute{}, handler.EnqueueRequestsFromMapFunc(routeToSplit)).
		Watches(&coordinationv1.Lease{}, handler.EnqueueRequestsFromMapFunc(labelsToSplit), builder.WithPredicates(memberLeaseChanged())).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(labelsToSplit)).
		Watches(&appsv1.StatefulSet{}, handler.EnqueueRequestsFromMapFunc(labelsToSplit)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToSplits), builder.WithPredicates(podChanged())).
		Complete(r)
}

// Reconcile advances one split through its requested lifecycle.
func (r *SplitReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	split := new(api.KafkaSplit)
	if err := r.Get(ctx, request.NamespacedName, split); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := split.Status.DeepCopy()
	split.Status.ObservedGeneration = split.Generation
	if split.DeletionTimestamp != nil {
		return r.reconcileSplitDeletion(ctx, split, before)
	}
	if err := r.ensureNamespaceLabel(ctx, split.Namespace); err != nil {
		return ctrl.Result{}, err
	}
	if split.Status.ActiveSpec != nil &&
		(split.Spec.DesiredState == api.DesiredStateDisabled || split.Status.ActiveGeneration != split.Generation) {
		return r.reconcileDeactivation(ctx, split, before, false)
	}
	if err := split.Validate(); err != nil {
		return r.transitionSplit(ctx, split, before, api.SplitPhaseInvalid, "InvalidSpec", err.Error(), false)
	}
	if !controllerutil.ContainsFinalizer(split, splitFinalizer) {
		controllerutil.AddFinalizer(split, splitFinalizer)
		if err := r.Update(ctx, split); err != nil {
			return ctrl.Result{}, err
		}
		return requeue(), nil
	}
	if split.Spec.DesiredState == api.DesiredStateDisabled {
		return r.reconcileDeactivation(ctx, split, before, false)
	}
	return r.reconcileEnabled(ctx, split, before)
}

func (r *SplitReconciler) updateSplitStatus(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	requeueAfter bool,
) (ctrl.Result, error) {
	if !reflect.DeepEqual(*before, split.Status) {
		if err := r.Status().Update(ctx, split); err != nil {
			return ctrl.Result{}, err
		}
	}
	if requeueAfter {
		return requeueFor(split.Status.Phase), nil
	}
	return ctrl.Result{}, nil
}

// requeueFor returns the requeue delay for a split that just entered phase.
func requeueFor(phase api.SplitPhase) ctrl.Result {
	switch phase {
	case api.SplitPhaseEnabled:
		return ctrl.Result{RequeueAfter: healthInterval}
	case api.SplitPhaseDisabled, api.SplitPhaseInvalid:
		return ctrl.Result{}
	default:
		return ctrl.Result{RequeueAfter: transitionInterval}
	}
}

// transitionSplit sets the split's phase and Ready condition, then persists the status.
func (r *SplitReconciler) transitionSplit(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	phase api.SplitPhase,
	reason, message string,
	requeueAfter bool,
) (ctrl.Result, error) {
	split.Status.Phase = phase
	status := metav1.ConditionFalse
	if phase == api.SplitPhaseEnabled {
		status = metav1.ConditionTrue
	}
	setReadyCondition(&split.Status.Conditions, status, reason, message, split.Generation)
	return r.updateSplitStatus(ctx, split, before, requeueAfter)
}

// ensureNamespaceLabel marks the namespace as holding a KafkaSplit, so the
// provider's Pod-mutating webhook is scoped to it.
func (r *SplitReconciler) ensureNamespaceLabel(ctx context.Context, name string) error {
	ns := new(corev1.Namespace)
	if err := r.Get(ctx, client.ObjectKey{Name: name}, ns); err != nil {
		return err
	}
	if ns.Labels[runtimeconfig.NamespaceLabel] == "true" {
		return nil
	}
	patch := client.MergeFrom(ns.DeepCopy())
	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}
	ns.Labels[runtimeconfig.NamespaceLabel] = "true"
	return r.Patch(ctx, ns, patch)
}

// releaseNamespaceLabel removes the namespace label once excludeSplit is the
// last KafkaSplit in the namespace.
func (r *SplitReconciler) releaseNamespaceLabel(ctx context.Context, namespace, excludeSplit string) error {
	splits := new(api.KafkaSplitList)
	if err := r.List(ctx, splits, client.InNamespace(namespace)); err != nil {
		return err
	}
	for i := range splits.Items {
		if splits.Items[i].Name != excludeSplit {
			return nil
		}
	}
	ns := new(corev1.Namespace)
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return client.IgnoreNotFound(err)
	}
	if ns.Labels[runtimeconfig.NamespaceLabel] == "" {
		return nil
	}
	patch := client.MergeFrom(ns.DeepCopy())
	delete(ns.Labels, runtimeconfig.NamespaceLabel)
	return r.Patch(ctx, ns, patch)
}

// removeSplitFinalizer persists finalizer removal and releases the
// namespace label when no other KafkaSplit remains.
func (r *SplitReconciler) removeSplitFinalizer(ctx context.Context, split *api.KafkaSplit) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(split, splitFinalizer)
	if err := r.Update(ctx, split); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.releaseNamespaceLabel(ctx, split.Namespace, split.Name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// RouteReconciler owns one personal shadow and its transactional drain.
type RouteReconciler struct {
	base
}

// routeSplitRefIndexField indexes KafkaRoutes by their spec.splitRef.name.
const routeSplitRefIndexField = "spec.splitRef.name"

func routeSplitRefIndexer(obj client.Object) []string {
	route, ok := obj.(*api.KafkaRoute)
	if !ok {
		return nil
	}
	return []string{route.Spec.SplitRef.Name}
}

// SetupWithManager registers the KafkaRoute controller.
func (r *RouteReconciler) SetupWithManager(manager ctrl.Manager) error {
	if err := manager.GetFieldIndexer().IndexField(
		context.Background(), &api.KafkaRoute{}, routeSplitRefIndexField, routeSplitRefIndexer,
	); err != nil {
		return err
	}
	// routeForSessionSlot allocates preprovisioned slots by read-then-write,
	// which is only safe with a single worker.
	return ctrl.NewControllerManagedBy(manager).For(&api.KafkaRoute{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).Complete(r)
}

// Reconcile advances one personal route.
func (r *RouteReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	route := new(api.KafkaRoute)
	if err := r.Get(ctx, request.NamespacedName, route); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := route.Status.DeepCopy()
	route.Status.ObservedGeneration = route.Generation
	if err := route.Validate(); err != nil {
		return r.transitionRoute(ctx, route, before, api.RoutePhaseInvalid, "InvalidSpec", err.Error(), false)
	}
	if route.Status.Phase != api.RoutePhaseClosed && route.DeletionTimestamp == nil &&
		!controllerutil.ContainsFinalizer(route, routeFinalizer) {
		controllerutil.AddFinalizer(route, routeFinalizer)
		if err := r.Update(ctx, route); err != nil {
			return ctrl.Result{}, err
		}
		return requeue(), nil
	}
	return r.reconcileRoute(ctx, route, before)
}

func (r *RouteReconciler) updateRouteStatus(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	requeueAfter bool,
) (ctrl.Result, error) {
	if !reflect.DeepEqual(*before, route.Status) {
		if err := r.Status().Update(ctx, route); err != nil {
			return ctrl.Result{}, err
		}
	}
	if requeueAfter {
		return requeueForRoute(route.Status.Phase), nil
	}
	return ctrl.Result{}, nil
}

// requeueForRoute returns the requeue delay for a route that just entered phase.
func requeueForRoute(phase api.RoutePhase) ctrl.Result {
	switch phase {
	case api.RoutePhaseReady:
		return ctrl.Result{RequeueAfter: healthInterval}
	case api.RoutePhaseClosed, api.RoutePhaseInvalid:
		return ctrl.Result{}
	default:
		return ctrl.Result{RequeueAfter: transitionInterval}
	}
}

// transitionRoute sets the route's phase and Ready condition, then persists the status.
func (r *RouteReconciler) transitionRoute(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	phase api.RoutePhase,
	reason, message string,
	requeueAfter bool,
) (ctrl.Result, error) {
	route.Status.Phase = phase
	status := metav1.ConditionFalse
	if phase == api.RoutePhaseReady {
		status = metav1.ConditionTrue
	}
	setReadyCondition(&route.Status.Conditions, status, reason, message, route.Generation)
	return r.updateRouteStatus(ctx, route, before, requeueAfter)
}

func setReadyCondition(
	conditions *[]metav1.Condition,
	status metav1.ConditionStatus,
	reason, message string,
	generation int64,
) {
	apiMeta.SetStatusCondition(conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: generation,
	})
}

func requeue() ctrl.Result {
	return ctrl.Result{RequeueAfter: transitionInterval}
}
