package watcher

import (
	"context"
	"slices"
	"sync"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
)

type Watcher[T runtime.Object] interface {
	Watch(ctx context.Context) error
	AddEventHandler(eh EventHandler[T])
}

type EventHandler[T runtime.Object] interface {
	HandleEvent(eventType watch.EventType, obj T)
}

type EventProducer interface {
	Watch(context.Context, meta.ListOptions) (watch.Interface, error)
}

type EventProducerInfo struct {
	Resource  string
	Namespace string
	Producer  EventProducer
}

type watcher[T runtime.Object] struct {
	sync.RWMutex
	eventProducers []EventProducerInfo
	eventHandlers  []EventHandler[T]
}

func NewWatcher[T runtime.Object](eventProducers ...EventProducerInfo) Watcher[T] {
	return &watcher[T]{eventProducers: eventProducers}
}

func (w *watcher[T]) AddEventHandler(eh EventHandler[T]) {
	w.Lock()
	w.eventHandlers = append(w.eventHandlers, eh)
	w.Unlock()
}

func (w *watcher[T]) Watch(ctx context.Context) error {
	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	for _, ep := range w.eventProducers {
		g.Go(ep.Namespace, func(ctx context.Context) error { return w.watch(ctx, ep) })
	}
	return g.Wait()
}

func (w *watcher[T]) watch(ctx context.Context, ev EventProducerInfo) error {
	ww := whereWeWatch(ev.Namespace)
	dlog.Infof(ctx, "Started watcher for %s %s", ev.Resource, ww)
	defer dlog.Infof(ctx, "Ended watcher for %s %s", ev.Resource, ww)

	// The Watch will perform a http GET call to the kubernetes API server, and that connection will not remain open forever
	// so when it closes, the watch must start over. This goes on until the context is cancelled.
	var resourceVersion string
	var rvMatch meta.ResourceVersionMatch
	for ctx.Err() == nil {
		wr, err := ev.Producer.Watch(ctx, meta.ListOptions{
			Watch:                true,
			AllowWatchBookmarks:  true,
			ResourceVersion:      resourceVersion,
			ResourceVersionMatch: rvMatch,
		})
		if err != nil {
			dlog.Errorf(ctx, "unable to create watcher for %s %s: %v", ev.Resource, ww, err)
			return err
		}
		if resourceVersion = w.eventHandler(ctx, wr.ResultChan()); resourceVersion != "" {
			rvMatch = meta.ResourceVersionMatchExact
		} else {
			rvMatch = ""
		}
	}
	return nil
}

// resourceVersionGetter is an interface used to get resource version from events.
type resourceVersionGetter interface {
	GetResourceVersion() string
}

func (w *watcher[T]) eventHandler(ctx context.Context, evCh <-chan watch.Event) (resourceVersion string) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-evCh:
			obj := event.Object
			if !ok {
				return // restart watcher
			}
			switch event.Type {
			case watch.Bookmark:
				if obj, ok := obj.(resourceVersionGetter); ok {
					resourceVersion = obj.GetResourceVersion()
				}
			case watch.Deleted, watch.Added, watch.Modified:
				if obj, ok := obj.(T); ok {
					w.RLock()
					ehs := slices.Clone(w.eventHandlers)
					w.RUnlock()
					for _, eh := range ehs {
						eh.HandleEvent(event.Type, obj)
					}
				}
			}
		}
	}
}

func whereWeWatch(ns string) string {
	if ns == "" {
		return "cluster wide"
	}
	return "in namespace " + ns
}
