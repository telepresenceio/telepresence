package k8s

import (
	"context"
	"sort"

	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	typedAuth "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
)

// nsWatcher runs a Kubernetes watcher that provide information about the cluster's namespaces'.
//
// A filtered list of namespaces is used for creating a DNS search path which is propagated to
// the DNS-resolver in the root daemon each time an update arrives.
//
// The first update will close the firstSnapshotArrived channel.
func (kc *Cluster) startNamespaceWatcher(c context.Context) {
	authHandler := kc.ki.AuthorizationV1().SelfSubjectAccessReviews()
	informerFactory := informers.NewSharedInformerFactory(kc.ki, 0)
	nsc := informerFactory.Core().V1().Namespaces()
	informer := nsc.Informer()
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(ns interface{}) {
			name := ns.(*core.Namespace).Name
			ok := kc.canAccessNS(c, authHandler, name)
			kc.nsLock.Lock()
			kc.currentNamespaces[name] = ok
			kc.nsLock.Unlock()
			kc.refreshNamespaces(c)
		},
		DeleteFunc: func(ns interface{}) {
			kc.nsLock.Lock()
			delete(kc.currentNamespaces, ns.(*core.Namespace).Name)
			kc.nsLock.Unlock()
			kc.refreshNamespaces(c)
		},
		UpdateFunc: func(oldNs, newNs interface{}) {
			oldName := oldNs.(*core.Namespace).Name
			newName := newNs.(*core.Namespace).Name
			if oldName == newName {
				return
			}
			ok := kc.canAccessNS(c, authHandler, newName)
			kc.nsLock.Lock()
			delete(kc.currentNamespaces, oldName)
			kc.currentNamespaces[newName] = ok
			kc.nsLock.Unlock()
			kc.refreshNamespaces(c)
		},
	})
	informerFactory.Start(c.Done())
	informerFactory.WaitForCacheSync(c.Done())
}

func (kc *Cluster) canAccessNS(c context.Context, authHandler typedAuth.SelfSubjectAccessReviewInterface, namespace string) bool {
	// The access rights lister here is the bare minimum needed when using the webhook agent injector
	for _, ra := range []*auth.ResourceAttributes{
		{
			Namespace: namespace,
			Verb:      "watch",
			Resource:  "services",
		},
		{
			Namespace: namespace,
			Verb:      "get",
			Resource:  "services",
		},
		{
			Namespace: namespace,
			Verb:      "get",
			Resource:  "deployments",
		},
	} {
		ar, err := authHandler.Create(c, &auth.SelfSubjectAccessReview{
			Spec: auth.SelfSubjectAccessReviewSpec{ResourceAttributes: ra},
		}, meta.CreateOptions{})
		if err != nil {
			if c.Err() == nil {
				dlog.Errorf(c, `unable to do "can-i" check verb %q, kind %q, in namespace %q: %v`, ra.Verb, ra.Resource, ra.Namespace, err)
			}
			return false
		}
		if !ar.Status.Allowed {
			dlog.Debugf(c, "namespace %q is not accessible", namespace)
			return false
		}
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
	equal := sortedStringSlicesEqual(namespaces, kc.mappedNamespaces)
	if !equal {
		kc.mappedNamespaces = namespaces
	}
	kc.nsLock.Unlock()

	if equal {
		return false
	}
	kc.refreshNamespaces(c)
	return true
}

func (kc *Cluster) SetNamespaceListener(nsListener func(context.Context)) {
	kc.nsLock.Lock()
	kc.namespaceListener = nsListener
	kc.nsLock.Unlock()
}

func (kc *Cluster) refreshNamespaces(c context.Context) {
	kc.nsLock.Lock()
	namespaces := make([]string, 0, len(kc.currentNamespaces))
	for ns, ok := range kc.currentNamespaces {
		if ok && kc.shouldBeWatched(ns) {
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)
	equal := sortedStringSlicesEqual(namespaces, kc.currentMappedNamespaces)
	if !equal {
		kc.currentMappedNamespaces = namespaces
	}
	nsListener := kc.namespaceListener
	kc.nsLock.Unlock()
	if !equal && nsListener != nil {
		nsListener(c)
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
