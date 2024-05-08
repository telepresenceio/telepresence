package state

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	apps "k8s.io/api/apps/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/k8sapi/pkg/k8sapi"
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

func (w *wlWatcher) addEventHandler(ctx context.Context, ns string) error {
	// TODO: Potentially watch Replicasets and Statefulsets too, perhaps configurable since it's fairly uncommon to not have a Deployment.
	ix := informer.GetFactory(ctx, ns).Apps().V1().Deployments().Informer()
	_, err := ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if d, ok := obj.(*apps.Deployment); ok {
					w.handleEvent(WorkloadEvent{Type: EventTypeAdd, Workload: k8sapi.Deployment(d)})
				}
			},
			DeleteFunc: func(obj any) {
				if d, ok := obj.(*apps.Deployment); ok {
					w.handleEvent(WorkloadEvent{Type: EventTypeDelete, Workload: k8sapi.Deployment(d)})
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if d, ok := dfsu.Obj.(*apps.Deployment); ok {
						w.handleEvent(WorkloadEvent{Type: EventTypeDelete, Workload: k8sapi.Deployment(d)})
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if d, ok := newObj.(*apps.Deployment); ok {
					w.handleEvent(WorkloadEvent{Type: EventTypeUpdate, Workload: k8sapi.Deployment(d)})
				}
			},
		})
	return err
}

func (w *wlWatcher) handleEvent(we WorkloadEvent) {
	w.Lock()
	w.events = append(w.events, we)
	w.Unlock()

	// Defer sending until things been quiet for a while
	w.timer.Reset(50 * time.Millisecond)
}
