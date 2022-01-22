package k8s

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
)

// resyncPeriod controls how often the controller goes through all items in the cache and fires an update func again.
// Resyncs are made to periodically check if updates were somehow missed (due to network glitches etc.). They consume
// a fair amount of resources on a large cluster and shouldn't run too frequently.
// TODO: Probably a good candidate to include in the cluster config
const resyncPeriod = 2 * time.Minute

// Watcher watches some resource and can be cancelled
type Watcher struct {
	sync.Mutex
	cancel     context.CancelFunc
	resource   string
	namespace  string
	getter     cache.Getter
	objType    runtime.Object
	cond       *sync.Cond
	controller cache.Controller
	store      cache.Store
	equals     func(runtime.Object, runtime.Object) bool
}

func newListerWatcher(c context.Context, getter cache.Getter, resource, namespace string) cache.ListerWatcher {
	// need to dig into how a ListerWatcher is created in order to pass the correct context
	listFunc := func(options meta.ListOptions) (runtime.Object, error) {
		return getter.Get().
			Namespace(namespace).
			Resource(resource).
			VersionedParams(&options, meta.ParameterCodec).
			Do(c).
			Get()
	}
	watchFunc := func(options meta.ListOptions) (watch.Interface, error) {
		options.Watch = true
		return getter.Get().
			Namespace(namespace).
			Resource(resource).
			VersionedParams(&options, meta.ParameterCodec).
			Watch(c)
	}
	return &cache.ListWatch{ListFunc: listFunc, WatchFunc: watchFunc}
}

func NewWatcher(resource, namespace string, getter cache.Getter, objType runtime.Object, cond *sync.Cond, equals func(runtime.Object, runtime.Object) bool) *Watcher {
	return &Watcher{
		resource:  resource,
		namespace: namespace,
		equals:    equals,
		getter:    getter,
		objType:   objType,
		cond:      cond,
	}
}

func (w *Watcher) Cancel() {
	w.Lock()
	defer w.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
}

// HasSynced returns true if this Watcher's controller has synced, or if this watcher hasn't started yet
func (w *Watcher) HasSynced() bool {
	w.Lock()
	defer w.Unlock()
	if w.controller != nil {
		w.controller.HasSynced()
	}
	return true
}

func (w *Watcher) Get(c context.Context, obj interface{}) (interface{}, bool, error) {
	w.Lock()
	defer w.Unlock()
	if w.store == nil {
		w.startOnDemand(c)
	}
	return w.store.Get(obj)
}

func (w *Watcher) List(c context.Context) []interface{} {
	w.Lock()
	defer w.Unlock()
	if w.store == nil {
		w.startOnDemand(c)
	}
	return w.store.List()
}

func (w *Watcher) Watch(c context.Context, ready *sync.WaitGroup) {
	func() {
		w.Lock()
		defer w.Unlock()
		w.startLocked(c, ready)
	}()
	w.run(c)
}

func (w *Watcher) startOnDemand(c context.Context) {
	rdy := sync.WaitGroup{}
	rdy.Add(1)
	w.startLocked(c, &rdy)
	rdy.Wait()
	go w.run(c)
	cache.WaitForCacheSync(c.Done(), w.controller.HasSynced)
}

func (w *Watcher) startLocked(c context.Context, ready *sync.WaitGroup) {
	defer ready.Done()

	c, w.cancel = context.WithCancel(c)
	eventCh := make(chan struct{}, 10)
	go w.handleEvents(c, eventCh)

	w.store = cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc)
	fifo := cache.NewDeltaFIFOWithOptions(cache.DeltaFIFOOptions{
		KnownObjects:          w.store,
		EmitDeltaTypeReplaced: true,
	})

	// Just creating an informer won't do, because then we cannot set the WatchErrorHandler of
	// its Config. So we create it from a Config instead, which actually plays out well because
	// we get immediate access to the Process function and can skip the ResourceEventHandlerFuncs
	config := cache.Config{
		Queue:         fifo,
		ListerWatcher: newListerWatcher(c, w.getter, w.resource, w.namespace),
		Process: func(obj interface{}) error {
			return w.process(c, obj.(cache.Deltas), eventCh)
		},
		ObjectType:       w.objType,
		FullResyncPeriod: resyncPeriod,
		WatchErrorHandler: func(_ *cache.Reflector, err error) {
			w.errorHandler(c, err)
		},
	}
	w.controller = cache.New(&config)
}

func (w *Watcher) run(c context.Context) {
	defer dlog.Debugf(c, "Watcher for %s in %s stopped", w.resource, w.namespace)
	dlog.Debugf(c, "Watcher for %s in %s started", w.resource, w.namespace)
	w.controller.Run(c.Done())
}

func (w *Watcher) process(c context.Context, ds cache.Deltas, eventCh chan<- struct{}) error {
	// from oldest to newest
	for _, d := range ds {
		var verb string
		switch d.Type {
		case cache.Deleted:
			if err := w.store.Delete(d.Object); err != nil {
				return err
			}
			verb = "delete"
		default:
			old, exists, err := w.store.Get(d.Object)
			if err != nil {
				return err
			}
			if exists {
				if err = w.store.Update(d.Object); err != nil {
					return err
				}
				if w.equals(old.(runtime.Object), d.Object.(runtime.Object)) {
					continue
				}
				verb = "update"
			} else {
				if err = w.store.Add(d.Object); err != nil {
					return err
				}
				verb = "add"
			}
		}
		dlog.Tracef(c, "%s %s in %s (%s)", verb, w.resource, w.namespace, d.Type)
		eventCh <- struct{}{}
	}
	return nil
}

const idleTriggerDuration = 50 * time.Millisecond

// handleEvents reads the channel and broadcasts on the condition once the channel has
// been quite for idleTriggerDuration
func (w *Watcher) handleEvents(c context.Context, eventCh <-chan struct{}) {
	idleTrigger := time.NewTimer(time.Duration(math.MaxInt64))
	idleTrigger.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-idleTrigger.C:
			idleTrigger.Stop()
			w.cond.Broadcast()
		case <-eventCh:
			idleTrigger.Reset(idleTriggerDuration)
		}
	}
}

func (w *Watcher) errorHandler(c context.Context, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		// Just ignore. This happens during a normal shutdown
	case apierrors.IsResourceExpired(err) || apierrors.IsGone(err):
		// Don't set LastSyncResourceVersionUnavailable - LIST call with ResourceVersion=RV already
		// has a semantic that it returns data at least as fresh as provided RV.
		// So first try to LIST with setting RV to resource version of last observed object.
		dlog.Errorf(c, "Watcher for %s in %s closed with: %v", w.resource, w.namespace, err)
	case errors.Is(err, io.EOF):
		// watch closed normally
	case errors.Is(err, io.ErrUnexpectedEOF):
		dlog.Errorf(c, "Watcher for %s in %s closed with unexpected EOF: %v", w.resource, w.namespace, err)
	default:
		se := &apierrors.StatusError{}
		if errors.As(err, &se) {
			st := se.Status()
			if st.Code == http.StatusForbidden {
				// User cannot get the resource from the current namespace, so
				// we might just as well cancel this watcher
				dlog.Errorf(c, "Watcher for %s in %s was denied access: %s", w.resource, w.namespace, st.Message)
				w.Cancel()
				return
			}
			_, i := se.DebugError()
			dlog.Errorf(c, "Watcher for %s in %s failed: %v", w.resource, w.namespace, i[0])
		} else {
			dlog.Errorf(c, "Watcher for %s in %s failed: %v", w.resource, w.namespace, err)
		}
		utilruntime.HandleError(err)
	}
}
