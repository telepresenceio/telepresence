package k8s

import (
	"context"
	"math"
	"sort"
	"time"

	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	typedAuth "k8s.io/client-go/kubernetes/typed/authorization/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
)

// nsWatcher runs a Kubernetes Watcher that provide information about the cluster's namespaces'.
//
// A filtered list of namespaces is used for creating a DNS search path which is propagated to
// the DNS-resolver in the root daemon each time an update arrives.
//
// The first update will close the firstSnapshotArrived channel.
func (kc *Cluster) StartNamespaceWatcher(ctx context.Context) {
	kc.namespaceWatcherSnapshot = make(map[string]struct{})
	nsSynced := make(chan struct{})
	go func() {
		api := kc.ki.CoreV1()
		for ctx.Err() == nil {
			w, err := api.Namespaces().Watch(ctx, meta.ListOptions{})
			if err != nil {
				dlog.Errorf(ctx, "unable to create service watcher: %v", err)
				return
			}
			kc.namespacesEventHandler(ctx, w.ResultChan(), nsSynced)
		}
	}()
	select {
	case <-ctx.Done():
	case <-nsSynced:
	}
}

func (kc *Cluster) namespacesEventHandler(ctx context.Context, evCh <-chan watch.Event, nsSynced chan struct{}) {
	// The delay timer will initially sleep forever. It's reset to a very short
	// delay when the file is modified.
	var delay *time.Timer
	delay = time.AfterFunc(time.Duration(math.MaxInt64), func() {
		kc.refreshNamespaces(ctx)
		select {
		case <-nsSynced:
		default:
			close(nsSynced)
		}
	})
	defer delay.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-evCh:
			if !ok {
				return // restart watcher
			}
			ns, ok := event.Object.(*core.Namespace)
			if !ok {
				continue
			}
			kc.nsLock.Lock()
			switch event.Type {
			case watch.Deleted:
				delete(kc.namespaceWatcherSnapshot, ns.Name)
			case watch.Added, watch.Modified:
				kc.namespaceWatcherSnapshot[ns.Name] = struct{}{}
			}
			kc.nsLock.Unlock()

			// We consider the watcher synced after 10 ms of inactivity. It's not a big deal
			// if more namespaces arrive after that.
			delay.Reset(10 * time.Millisecond)
		}
	}
}

func (kc *Cluster) canAccessNS(c context.Context, authHandler typedAuth.SelfSubjectAccessReviewInterface, namespace string) bool {
	// Doing multiple checks here to really ensure that the current user can watch, list, and get services and workloads would
	// take too long, so this check will have to do. In case other restrictions are encountered later on, they will generate
	// errors and invalidate the namespace.
	ra := auth.ResourceAttributes{
		Namespace: namespace,
		Verb:      "get",
		Resource:  "deployments",
		Group:     "apps",
	}
	ar, err := authHandler.Create(c, &auth.SelfSubjectAccessReview{
		Spec: auth.SelfSubjectAccessReviewSpec{ResourceAttributes: &ra},
	}, meta.CreateOptions{})
	if err != nil {
		if c.Err() == nil {
			dlog.Errorf(c, `unable to do "can-i" check verb %q, kind %q, in namespace %q: %v`, ra.Verb, ra.Resource, ra.Namespace, err)
		}
		return false
	}
	if !ar.Status.Allowed {
		dlog.Infof(c, "Namespace %q is not accessible. Doing %q on %q is not allowed", namespace, ra.Verb, ra.Resource)
		return false
	}
	return true
}

func sortedStringSlicesEqual(as, bs []string) bool {
	if len(as) != len(bs) {
		return false
	}
	for i, a := range as {
		if a != bs[i] {
			return false
		}
	}
	return true
}

func (kc *Cluster) SetMappedNamespaces(c context.Context, namespaces []string) bool {
	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	} else {
		sort.Strings(namespaces)
	}

	equal := sortedStringSlicesEqual(namespaces, kc.mappedNamespaces)
	if !equal {
		kc.mappedNamespaces = namespaces
		kc.refreshNamespaces(c)
	}
	return !equal
}

func (kc *Cluster) AddNamespaceListener(c context.Context, nsListener userd.NamespaceListener) {
	kc.nsLock.Lock()
	kc.namespaceListeners = append(kc.namespaceListeners, nsListener)
	kc.nsLock.Unlock()
	nsListener(c)
}

func (kc *Cluster) refreshNamespaces(c context.Context) {
	kc.nsLock.Lock()
	defer kc.nsLock.Unlock()
	authHandler := kc.ki.AuthorizationV1().SelfSubjectAccessReviews()
	var nss []string
	if kc.namespaceWatcherSnapshot == nil {
		nss = kc.mappedNamespaces
	} else {
		nss = make([]string, len(kc.namespaceWatcherSnapshot))
		i := 0
		for ns := range kc.namespaceWatcherSnapshot {
			nss[i] = ns
			i++
		}
	}
	namespaces := make(map[string]bool, len(nss))
	for _, ns := range nss {
		if kc.shouldBeWatched(ns) {
			accessOk, ok := kc.currentMappedNamespaces[ns]
			if !ok {
				accessOk = kc.canAccessNS(c, authHandler, ns)
			}
			namespaces[ns] = accessOk
		}
	}
	equal := len(namespaces) == len(kc.currentMappedNamespaces)
	if equal {
		for k, ov := range kc.currentMappedNamespaces {
			if nv, ok := namespaces[k]; !ok || nv != ov {
				equal = false
				break
			}
		}
	}
	if equal {
		return
	}
	kc.currentMappedNamespaces = namespaces
	for _, nsListener := range kc.namespaceListeners {
		func() {
			kc.nsLock.Unlock()
			defer kc.nsLock.Lock()
			nsListener(c)
		}()
	}
}

func (kc *Cluster) shouldBeWatched(namespace string) bool {
	if len(kc.mappedNamespaces) == 0 {
		return true
	}
	for _, n := range kc.mappedNamespaces {
		if n == namespace {
			return true
		}
	}
	return false
}
