package k8s

import (
	"context"
	"sort"
	"sync"

	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	typedAuth "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// nsWatcher runs a Kubernetes Watcher that provide information about the cluster's namespaces'.
//
// A filtered list of namespaces is used for creating a DNS search path which is propagated to
// the DNS-resolver in the root daemon each time an update arrives.
//
// The first update will close the firstSnapshotArrived channel.
func (kc *Cluster) startNamespaceWatcher(c context.Context) {
	cond := sync.Cond{}
	cond.L = &kc.nsLock
	kc.nsWatcher = k8sapi.NewWatcher("namespaces", "", kc.ki.CoreV1().RESTClient(), &core.Namespace{}, &cond, func(a, b runtime.Object) bool {
		return a.(*core.Namespace).Name == b.(*core.Namespace).Name
	})

	ready := sync.WaitGroup{}
	ready.Add(1)
	go kc.nsWatcher.Watch(c, &ready)
	ready.Wait()
	cache.WaitForCacheSync(c.Done(), kc.nsWatcher.HasSynced)

	kc.nsLock.Lock()
	go func() {
		defer kc.nsLock.Unlock()
		for {
			select {
			case <-c.Done():
				return
			default:
			}
			cond.Wait()
			kc.refreshNamespacesLocked(c)
		}
	}()

	// A Lock here forces us to wait until the above goroutine
	// has entered cond.Wait()
	kc.nsLock.Lock()
	cond.Broadcast() // force initial call to refreshNamespacesLocked
	kc.nsLock.Unlock()
}

func (kc *Cluster) WaitForNSSync(c context.Context) {
	if !kc.nsWatcher.HasSynced() {
		cache.WaitForCacheSync(c.Done(), kc.nsWatcher.HasSynced)
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

	kc.nsLock.Lock()
	defer kc.nsLock.Unlock()
	equal := sortedStringSlicesEqual(namespaces, kc.mappedNamespaces)
	if !equal {
		kc.mappedNamespaces = namespaces
		kc.refreshNamespacesLocked(c)
	}
	return !equal
}

func (kc *Cluster) AddNamespaceListener(nsListener NamespaceListener) {
	kc.nsLock.Lock()
	kc.namespaceListeners = append(kc.namespaceListeners, nsListener)
	kc.nsLock.Unlock()
}

func (kc *Cluster) refreshNamespacesLocked(c context.Context) {
	authHandler := kc.ki.AuthorizationV1().SelfSubjectAccessReviews()
	cns := kc.nsWatcher.List(c)
	namespaces := make(map[string]bool, len(cns))
	for _, o := range cns {
		ns := o.(*core.Namespace).Name
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
	// The "kube-system" namespace must be mapped when hijacking the IP of the
	// kube-dns service in the daemon.
	if len(kc.mappedNamespaces) == 0 || namespace == "kube-system" {
		return true
	}
	for _, n := range kc.mappedNamespaces {
		if n == namespace {
			return true
		}
	}
	return false
}
