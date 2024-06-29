package state

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	apps "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubectl/pkg/util/deployment"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
)

type EventType int

const (
	EventTypeAdd = iota
	EventTypeUpdate
	EventTypeDelete
)

type WorkloadEvent struct {
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

type WorkloadWatcher interface {
	Subscribe(ctx context.Context) <-chan []WorkloadEvent
}

type wlWatcher struct {
	sync.Mutex
	subscriptions map[uuid.UUID]chan<- []WorkloadEvent
	timer         *time.Timer
	events        []WorkloadEvent
}

func NewWorkloadWatcher(ctx context.Context, ns string) (WorkloadWatcher, error) {
	w := new(wlWatcher)
	w.subscriptions = make(map[uuid.UUID]chan<- []WorkloadEvent)
	w.timer = time.AfterFunc(time.Duration(math.MaxInt64), func() {
		w.Lock()
		ss := make([]chan<- []WorkloadEvent, len(w.subscriptions))
		i := 0
		for _, sub := range w.subscriptions {
			ss[i] = sub
			i++
		}
		events := w.events
		w.events = nil
		w.Unlock()
		for _, s := range ss {
			select {
			case <-ctx.Done():
				return
			case s <- events:
			}
		}
	})

	err := w.addEventHandler(ctx, ns)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (w *wlWatcher) Subscribe(ctx context.Context) <-chan []WorkloadEvent {
	ch := make(chan []WorkloadEvent)
	id := uuid.New()
	w.Lock()
	w.subscriptions[id] = ch
	w.Unlock()
	go func() {
		<-ctx.Done()
		close(ch)
		w.Lock()
		delete(w.subscriptions, id)
		w.Unlock()
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
			return cmp.Equal(a.Conditions, b.Conditions, cmp.Comparer(func(c1, c2 apps.DeploymentCondition) bool {
				return c1.Type == c2.Type && c1.Status == c2.Status
			}))
		}),

		// Treat a nil map or slice as empty.
		cmpopts.EquateEmpty(),

		// Ignore frequently changing annotations of no interest.
		cmpopts.IgnoreMapEntries(func(k, _ string) bool {
			return k == mutator.AnnRestartedAt || k == deployment.RevisionAnnotation
		}),
	}
}

func (w *wlWatcher) watchWorkloads(ix cache.SharedIndexInformer, ns string) error {
	_, err := ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if wl, ok := mutator.WorkloadFromAny(obj); ok && ns == wl.GetNamespace() && len(wl.GetOwnerReferences()) == 0 {
					w.handleEvent(WorkloadEvent{Type: EventTypeAdd, Workload: wl})
				}
			},
			DeleteFunc: func(obj any) {
				if wl, ok := mutator.WorkloadFromAny(obj); ok {
					if ns == wl.GetNamespace() && len(wl.GetOwnerReferences()) == 0 {
						w.handleEvent(WorkloadEvent{Type: EventTypeDelete, Workload: wl})
					}
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if wl, ok = mutator.WorkloadFromAny(dfsu.Obj); ok && ns == wl.GetNamespace() && len(wl.GetOwnerReferences()) == 0 {
						w.handleEvent(WorkloadEvent{Type: EventTypeDelete, Workload: wl})
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if wl, ok := mutator.WorkloadFromAny(newObj); ok && ns == wl.GetNamespace() && len(wl.GetOwnerReferences()) == 0 {
					if oldWl, ok := mutator.WorkloadFromAny(oldObj); ok {
						if cmp.Equal(wl, oldWl, compareOptions()...) {
							return
						}
						// Replace the cmp.Equal above with this to view the changes that trigger an update:
						//
						// diff := cmp.Diff(wl, oldWl, compareOptions()...)
						// if diff == "" {
						//   return
						// }
						// dlog.Debugf(ctx, "DIFF:\n%s", diff)
						w.handleEvent(WorkloadEvent{Type: EventTypeUpdate, Workload: wl})
					}
				}
			},
		})
	return err
}

func (w *wlWatcher) addEventHandler(ctx context.Context, ns string) error {
	ai := informer.GetFactory(ctx, ns).Apps().V1()
	if err := w.watchWorkloads(ai.Deployments().Informer(), ns); err != nil {
		return err
	}
	if err := w.watchWorkloads(ai.ReplicaSets().Informer(), ns); err != nil {
		return err
	}
	if err := w.watchWorkloads(ai.StatefulSets().Informer(), ns); err != nil {
		return err
	}
	return nil
}

func (w *wlWatcher) handleEvent(we WorkloadEvent) {
	w.Lock()
	w.events = append(w.events, we)
	w.Unlock()

	// Defers sending until things been quiet for a while
	w.timer.Reset(50 * time.Millisecond)
}
