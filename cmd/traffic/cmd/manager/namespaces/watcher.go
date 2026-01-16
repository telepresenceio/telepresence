package namespaces

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	listersCore "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

type nsKey struct{}

type namesspacesHandle struct {
	sync.Mutex
	selectorChan      <-chan *labels.Selector
	selector          *labels.Selector
	namespaces        []string
	interestedParties map[uuid.UUID]chan<- struct{}
	lister            listersCore.NamespaceLister
}

func (h *namesspacesHandle) set(namespaces []string) {
	sort.Strings(namespaces)
	var ips map[uuid.UUID]chan<- struct{}
	h.Lock()
	if !slices.Equal(namespaces, h.namespaces) {
		h.namespaces = namespaces
		ips = h.interestedParties
	}
	h.Unlock()
	for _, ip := range ips {
		// Send the notification but don't wait around for anyone to read it
		select {
		case ip <- struct{}{}:
		default:
		}
	}
}

func (h *namesspacesHandle) get() []string {
	h.Lock()
	ns := h.namespaces
	h.Unlock()
	return ns
}

func (h *namesspacesHandle) listen(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case sel, ok := <-h.selectorChan:
			if !ok {
				return nil
			}
			h.selector = sel
			switch {
			case h.selector == nil:
				h.set(nil)
			case h.lister == nil:
				dynamic, err := h.computeNames(ctx)
				if err != nil {
					return err
				}
				if dynamic {
					err = h.newWatcher(ctx)
					if err != nil {
						return err
					}
				}
			default:
				if err := h.updateSelection(); err != nil {
					clog.Error(ctx, err)
				}
			}
		}
	}
}

func (h *namesspacesHandle) newWatcher(ctx context.Context) error {
	informerFactory := informers.NewSharedInformerFactory(k8sapi.GetK8sInterface(ctx), 0)
	nsApi := informerFactory.Core().V1().Namespaces()
	ix := nsApi.Informer()
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		clog.Errorf(ctx, "Watcher for namespaces: %v", err)
	})

	h.lister = nsApi.Lister()
	kickTheBucket := time.AfterFunc(time.Duration(math.MaxInt64), func() {
		if err := h.updateSelection(); err != nil {
			clog.Error(ctx, err)
		}
	})
	_, err := ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				kickTheBucket.Reset(100 * time.Millisecond)
			},
			DeleteFunc: func(obj any) {
				kickTheBucket.Reset(100 * time.Millisecond)
			},
			UpdateFunc: func(oldObj, newObj any) {
				kickTheBucket.Reset(100 * time.Millisecond)
			},
		})

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())
	return err
}

func (h *namesspacesHandle) addSubscriber() (uuid.UUID, <-chan struct{}) {
	id := uuid.New()
	ch := make(chan struct{}, 1)
	h.Lock()
	h.interestedParties[id] = ch
	h.Unlock()
	// Send an initial notification
	ch <- struct{}{}
	return id, ch
}

func (h *namesspacesHandle) removeSubscriber(id uuid.UUID) {
	h.Lock()
	ch, ok := h.interestedParties[id]
	if ok {
		delete(h.interestedParties, id)
	}
	h.Unlock()
	if ok {
		close(ch)
	}
}

func (h *namesspacesHandle) static() bool {
	return h.selector.Static()
}

func (h *namesspacesHandle) updateSelection() error {
	sel, err := h.selector.LabelsSelector()
	if err != nil {
		return err
	}
	nis, err := h.lister.List(sel)
	if err != nil {
		return fmt.Errorf("error listing namespaces: %v", err)
	}
	names := make([]string, len(nis))
	for i, n := range nis {
		names[i] = n.Name
	}
	h.set(names)
	return nil
}

func (h *namesspacesHandle) computeNamesStatic(ctx context.Context) error {
	// The traffic-manager is not necessarily permitted to list namespaces. We do the best we
	// can of this, by instead using the names we know of.
	names := h.selector.StaticNames()
	if len(names) > 0 {
		clog.Debugf(ctx, "Using fixed set of namespaces %v", names)
		h.set(names)
		return nil
	}
	return errors.New("unable to determine a static list of namespaces from given namespace selector")
}

func (h *namesspacesHandle) computeNames(ctx context.Context) (bool, error) {
	sel, err := h.selector.LabelsSelector()
	if err != nil {
		return false, err
	}
	clog.Debugf(ctx, "Selecting namespaces based on %s", sel)
	nl, err := k8sapi.GetK8sInterface(ctx).CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		if k8serrors.IsForbidden(err) {
			clog.Debug(ctx, "Listing namespaces is not permitted")
			staticErr := h.computeNamesStatic(ctx)
			if staticErr == nil {
				return false, nil
			}
			clog.Error(ctx, staticErr)
		}
		return false, fmt.Errorf("error listing namespaces: %v", err)
	}

	nis := nl.Items
	names := make([]string, len(nis))
	for i, n := range nis {
		names[i] = n.Name
	}
	if len(names) == 0 {
		clog.Warnf(ctx, "no namespaces were found that matches the namespace selector %v", sel)
	} else {
		sort.Strings(names)
		clog.Debugf(ctx, "Using dynamic set of namespaces %v", names)
	}
	h.set(names)
	return true, nil
}

// InitContext returns a context that can be used with Get to retrieve namespaces that match the namespace
// selector found in the environment. The value returned by Get may change over time when the traffic-manager
// is allowed to watch the cluster's namespaces. It may also be nil in case there's no namespace selector
// present, which means that the scope is cluster global.
func InitContext(ctx context.Context, selectorChan <-chan *labels.Selector) (nsCtx context.Context, err error) {
	sel, ok := <-selectorChan
	if !ok {
		return ctx, fmt.Errorf("watcher channel closed")
	}
	if sel == nil {
		return ctx, nil
	}
	h := &namesspacesHandle{selectorChan: selectorChan, selector: sel, interestedParties: make(map[uuid.UUID]chan<- struct{})}
	dynamic, err := h.computeNames(ctx)
	if err != nil {
		return ctx, err
	}
	nsCtx = context.WithValue(ctx, nsKey{}, h)
	if dynamic {
		err = h.newWatcher(nsCtx)
		if err != nil {
			// Return error with parent context
			return ctx, err
		}
	}
	return nsCtx, nil
}

// Get will return the currently mapped namespaces or nil if the scope is cluster global.
func Get(ctx context.Context) []string {
	if h, ok := ctx.Value(nsKey{}).(*namesspacesHandle); ok {
		return h.get()
	}
	return nil
}

// GetOrGlobal checks if the size of the currently mapped namespace list exceeds MaxNamespaceSpecificWatchers, and
// if so, if Static returns false. If that is the case, a one-element slice containing an empty string
// is returned that represents a cluster-wide lister or watcher; otherwise, the current list of namespaces
// is returned.
func GetOrGlobal(ctx context.Context) (nss []string) {
	if h, ok := ctx.Value(nsKey{}).(*namesspacesHandle); ok {
		nss = h.get()
		if len(nss) > managerutil.GetEnv(ctx).MaxNamespaceSpecificWatchers && !h.static() {
			nss = []string{""}
		}
	}
	return nss
}

func Listen(ctx context.Context) error {
	if h, ok := ctx.Value(nsKey{}).(*namesspacesHandle); ok {
		return h.listen(ctx)
	}
	return nil
}

// Static returns true when the namespace selector consist of only one element using a match expression
// "kubernetes.io/metadata.name in <set of names>".
func Static(ctx context.Context) bool {
	if h, ok := ctx.Value(nsKey{}).(*namesspacesHandle); ok {
		return h.static()
	}
	return false
}

// Subscribe to be notified when the set of namespaces change.
func Subscribe(ctx context.Context) (uuid.UUID, <-chan struct{}) {
	if h, ok := ctx.Value(nsKey{}).(*namesspacesHandle); ok {
		return h.addSubscriber()
	}
	return uuid.Nil, nil
}

func Unsubscribe(ctx context.Context, id uuid.UUID) {
	if h, ok := ctx.Value(nsKey{}).(*namesspacesHandle); ok {
		h.removeSubscriber(id)
	}
}
