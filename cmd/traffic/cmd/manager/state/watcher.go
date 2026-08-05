package state

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	apps "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubectl/pkg/util/deployment"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

type EventType int

const (
	EventTypeAdd = iota
	EventTypeUpdate
	EventTypeDelete
)

type Event struct {
	Type     EventType
	Workload k8sapi.Workload
}

func (e EventType) String() string {
	switch e {
	case EventTypeAdd:
		return "add"
	case EventTypeUpdate:
		return "update"
	case EventTypeDelete:
		return "delete"
	default:
		return "unknown"
	}
}

type Watcher interface {
	Subscribe(ctx context.Context, namespace string) <-chan []Event
	// Close removes the watcher's informer event handlers and stops its
	// delivery timer. Subscribers are not signaled; they stop on their own
	// context.
	Close()
}

// handlerReg pairs an informer with the registration of the event handler
// this watcher added to it, so Close can remove exactly that handler.
type handlerReg struct {
	informer cache.SharedIndexInformer
	reg      cache.ResourceEventHandlerRegistration
}

// subscriptionBacklog is the event-channel buffer of one subscription. The
// receiver drains promptly, so more than one slot is rarely occupied; the
// depth exists so that deliveries racing an unsubscribe land in the buffer
// instead of parking a delivery goroutine, and so the end sentinel almost
// always fits.
const subscriptionBacklog = 8

// subscription is a single Subscribe call: the channel events are delivered
// on, and the namespace they're filtered to.
type subscription struct {
	ch        chan<- []Event
	namespace string
	done      <-chan struct{}
}

type watcher struct {
	sync.Mutex
	namespace            string
	subscriptions        map[uuid.UUID]subscription
	timer                *time.Timer
	events               []Event
	enabledWorkloadKinds k8sapi.Kinds
	// regs is populated by NewWatcher only, before the watcher is shared.
	regs []handlerReg
}

// NewWatcher creates a watcher backed by the informer factory for ns: a
// namespace-scoped factory for a scoped watcher, or the cluster-wide factory
// when ns is "". Namespace selection for a subscriber is not decided here;
// see Subscribe.
func NewWatcher(ctx context.Context, ns string, enabledWorkloadKinds k8sapi.Kinds) (Watcher, error) {
	w := new(watcher)
	w.namespace = ns
	w.enabledWorkloadKinds = enabledWorkloadKinds
	w.subscriptions = make(map[uuid.UUID]subscription)
	w.timer = time.AfterFunc(time.Duration(math.MaxInt64), func() {
		w.dispatch(ctx)
	})

	err := w.addEventHandler(ctx)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (w *watcher) dispatch(ctx context.Context) {
	w.Lock()
	ss := make([]subscription, 0, len(w.subscriptions))
	for _, sub := range w.subscriptions {
		ss = append(ss, sub)
	}
	events := w.events
	w.events = nil
	w.Unlock()
	for _, s := range ss {
		filtered := make([]Event, 0, len(events))
		for _, event := range events {
			if event.Workload.GetNamespace() == s.namespace {
				filtered = append(filtered, event)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.done:
		case s.ch <- filtered:
		}
	}
}

func hasValidReplicasetOwner(wl k8sapi.Workload, enabledKinds k8sapi.Kinds) bool {
	for _, ref := range wl.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller {
			kind := k8sapi.Kind(ref.Kind)
			switch kind {
			case k8sapi.DeploymentKind, k8sapi.RolloutKind:
				return enabledKinds.Contains(kind)
			}
		}
	}
	return false
}

// Subscribe registers a subscriber scoped to namespace and returns its event
// channel, seeded with an initial snapshot of the currently known workloads
// in that namespace. The watcher's own factory (w.namespace) may be the
// cluster-wide informer; namespace is only used to scope this subscription's
// listing and delivery, and is never empty.
func (w *watcher) Subscribe(ctx context.Context, namespace string) <-chan []Event {
	ch := make(chan []Event, subscriptionBacklog)
	// Deliberately non-nil even when empty: nil on this channel is the
	// end-of-subscription sentinel.
	initialEvents := make([]Event, 0, 100)
	id := uuid.New()
	kf := informer.GetFactory(ctx, w.namespace)
	ai := kf.GetK8sInformerFactory().Apps().V1()
	clog.Debugf(ctx, "workload.Watcher producing initial events for namespace %s", namespace)
	if w.enabledWorkloadKinds.Contains(k8sapi.DeploymentKind) {
		if dps, err := ai.Deployments().Lister().Deployments(namespace).List(labels.Everything()); err == nil {
			for _, obj := range dps {
				if wl, ok := workload.FromAny(obj); ok && !hasValidReplicasetOwner(wl, w.enabledWorkloadKinds) && !agentmap.TrafficManagerSelector.Matches(labels.Set(obj.Labels)) {
					initialEvents = append(initialEvents, Event{
						Type:     EventTypeAdd,
						Workload: wl,
					})
				}
			}
		}
	}
	if w.enabledWorkloadKinds.Contains(k8sapi.ReplicaSetKind) {
		if rps, err := ai.ReplicaSets().Lister().ReplicaSets(namespace).List(labels.Everything()); err == nil {
			for _, obj := range rps {
				if wl, ok := workload.FromAny(obj); ok && !hasValidReplicasetOwner(wl, w.enabledWorkloadKinds) {
					initialEvents = append(initialEvents, Event{
						Type:     EventTypeAdd,
						Workload: wl,
					})
				}
			}
		}
	}
	if w.enabledWorkloadKinds.Contains(k8sapi.StatefulSetKind) {
		if sps, err := ai.StatefulSets().Lister().StatefulSets(namespace).List(labels.Everything()); err == nil {
			for _, obj := range sps {
				if wl, ok := workload.FromAny(obj); ok && !hasValidReplicasetOwner(wl, w.enabledWorkloadKinds) {
					initialEvents = append(initialEvents, Event{
						Type:     EventTypeAdd,
						Workload: wl,
					})
				}
			}
		}
	}
	if w.enabledWorkloadKinds.Contains(k8sapi.RolloutKind) {
		ri := kf.GetArgoRolloutsInformerFactory().Argoproj().V1alpha1()
		if sps, err := ri.Rollouts().Lister().Rollouts(namespace).List(labels.Everything()); err == nil {
			for _, obj := range sps {
				if wl, ok := workload.FromAny(obj); ok && !hasValidReplicasetOwner(wl, w.enabledWorkloadKinds) {
					initialEvents = append(initialEvents, Event{
						Type:     EventTypeAdd,
						Workload: wl,
					})
				}
			}
		}
	}
	ch <- initialEvents

	w.Lock()
	w.subscriptions[id] = subscription{ch: ch, namespace: namespace, done: ctx.Done()}
	w.Unlock()
	go func() {
		<-ctx.Done()
		w.Lock()
		delete(w.subscriptions, id)
		w.Unlock()
		// A nil batch tells the receiver the subscription has ended. The
		// send is a best-effort courtesy -- the receiver's own context is
		// already done -- so a full backlog just skips it; the channel is
		// never closed, keeping an in-flight delivery free of any
		// send-on-closed hazard.
		select {
		case ch <- nil:
		default:
		}
	}()
	return ch
}

func compareOptions() []cmp.Option {
	return []cmp.Option{
		// Ignore frequently changing fields of no interest
		cmpopts.IgnoreFields(meta.ObjectMeta{}, "Namespace", "ResourceVersion", "Generation", "ManagedFields"),

		// Only the Conditions are of interest in the DeploymentStatus.
		cmp.Comparer(func(a, b apps.DeploymentStatus) bool {
			// Only compare the DeploymentCondition's type and status
			return slices.EqualFunc(a.Conditions, b.Conditions, func(c1, c2 apps.DeploymentCondition) bool {
				return c1.Type == c2.Type && c1.Status == c2.Status
			})
		}),

		// Treat a nil map or slice as empty.
		cmpopts.EquateEmpty(),

		// Ignore frequently changing annotations of no interest.
		cmpopts.IgnoreMapEntries(func(k, _ string) bool {
			return k == annotation.RestartedAt || k == deployment.RevisionAnnotation
		}),
	}
}

// Close stops the flow of events into the watcher: the informer event
// handlers are removed first, so nothing rearms the timer, and the timer is
// stopped last to kill a pending delivery.
func (w *watcher) Close() {
	for _, hr := range w.regs {
		_ = hr.informer.RemoveEventHandler(hr.reg)
	}
	w.timer.Stop()
}

// watch registers an event handler on ix that forwards every add/update/
// delete of a workload without a valid replicaset owner. The informer is
// already scoped to whatever namespace this watcher covers (a single
// namespace, or the whole cluster), so no namespace check is needed here;
// per-subscription namespace filtering happens at delivery time.
func (w *watcher) watch(ix cache.SharedIndexInformer, hasValidController func(k8sapi.Workload) bool) error {
	reg, err := ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if wl, ok := workload.FromAny(obj); ok && !hasValidController(wl) {
					w.handleEvent(Event{Type: EventTypeAdd, Workload: wl})
				}
			},
			DeleteFunc: func(obj any) {
				if wl, ok := workload.FromAny(obj); ok {
					if !hasValidController(wl) {
						w.handleEvent(Event{Type: EventTypeDelete, Workload: wl})
					}
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if wl, ok = workload.FromAny(dfsu.Obj); ok && !hasValidController(wl) {
						w.handleEvent(Event{Type: EventTypeDelete, Workload: wl})
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if wl, ok := workload.FromAny(newObj); ok && !hasValidController(wl) {
					if oldWl, ok := workload.FromAny(oldObj); ok {
						if cmp.Equal(wl, oldWl, compareOptions()...) {
							return
						}
						// Replace the cmp.Equal above with this to view the changes that trigger an update:
						//
						// diff := cmp.Diff(wl, oldWl, compareOptions()...)
						// if diff == "" {
						//   return
						// }
						// clog.Debugf(ctx, "DIFF:\n%s", diff)
						w.handleEvent(Event{Type: EventTypeUpdate, Workload: wl})
					}
				}
			},
		})
	if err == nil {
		w.regs = append(w.regs, handlerReg{informer: ix, reg: reg})
	}
	return err
}

func (w *watcher) addEventHandler(ctx context.Context) error {
	kf := informer.GetFactory(ctx, w.namespace)
	hvc := func(wl k8sapi.Workload) bool {
		return hasValidReplicasetOwner(wl, w.enabledWorkloadKinds)
	}

	ai := kf.GetK8sInformerFactory().Apps().V1()
	for _, wlKind := range w.enabledWorkloadKinds {
		var ssi cache.SharedIndexInformer
		switch wlKind {
		case k8sapi.DeploymentKind:
			ssi = ai.Deployments().Informer()
		case k8sapi.ReplicaSetKind:
			ssi = ai.ReplicaSets().Informer()
		case k8sapi.StatefulSetKind:
			ssi = ai.StatefulSets().Informer()
		case k8sapi.RolloutKind:
			ri := kf.GetArgoRolloutsInformerFactory().Argoproj().V1alpha1()
			ssi = ri.Rollouts().Informer()
		default:
			continue
		}

		if err := w.watch(ssi, hvc); err != nil {
			return err
		}
	}
	return nil
}

func (w *watcher) handleEvent(we Event) {
	// Always exclude the traffic-manager
	if we.Workload.GetKind() == "Deployment" && agentmap.TrafficManagerSelector.Matches(labels.Set(we.Workload.GetLabels())) {
		return
	}
	w.Lock()
	w.events = append(w.events, we)
	w.Unlock()

	// Defers sending until things been quiet for a while
	w.timer.Reset(5 * time.Millisecond)
}
