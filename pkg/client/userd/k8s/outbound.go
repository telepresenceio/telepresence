package k8s

import (
	"context"
	"sort"

	core "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// nsWatcher runs a Kubernetes watcher that provide information about the cluster's namespaces'.
//
// A filtered list of namespaces is used for creating a DNS search path which is propagated to
// the DNS-resolver in the root daemon each time an update arrives.
//
// The first update will close the firstSnapshotArrived channel.
func (kc *Cluster) startNamespaceWatcher(c context.Context) {
	informerFactory := informers.NewSharedInformerFactory(kc.ki, 0)
	nsc := informerFactory.Core().V1().Namespaces()
	informer := nsc.Informer()
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(ns interface{}) {
			kc.nsLock.Lock()
			kc.currentNamespaces[ns.(*core.Namespace).Name] = struct{}{}
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
			kc.nsLock.Lock()
			delete(kc.currentNamespaces, oldNs.(*core.Namespace).Name)
			kc.currentNamespaces[newNs.(*core.Namespace).Name] = struct{}{}
			kc.nsLock.Unlock()
			kc.refreshNamespaces(c)
		},
	})
	informerFactory.Start(c.Done())
	informerFactory.WaitForCacheSync(c.Done())
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

func (kc *Cluster) IngressInfos(c context.Context) ([]*manager.IngressInfo, error) {
	kc.nsLock.Lock()
	defer kc.nsLock.Unlock()

	ingressInfo := kc.ingressInfo
	if ingressInfo == nil {
		kc.nsLock.Unlock()
		ingressInfo, err := kc.detectIngressBehavior(c)
		kc.nsLock.Lock()
		if err != nil {
			kc.ingressInfo = nil
			return nil, err
		}
		kc.ingressInfo = ingressInfo
	}
	is := make([]*manager.IngressInfo, len(kc.ingressInfo))
	copy(is, kc.ingressInfo)
	return is, nil
}

func (kc *Cluster) SetMappedNamespaces(c context.Context, namespaces []string) error {
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
		return nil
	}
	kc.refreshNamespaces(c)
	kc.ingressInfo = nil
	return nil
}

func (kc *Cluster) SetNamespaceListener(nsListener func(context.Context)) {
	kc.nsLock.Lock()
	kc.namespaceListener = nsListener
	kc.nsLock.Unlock()
}

func (kc *Cluster) refreshNamespaces(c context.Context) {
	kc.nsLock.Lock()
	namespaces := make([]string, 0, len(kc.currentNamespaces))
	for ns := range kc.currentNamespaces {
		if kc.shouldBeWatched(ns) {
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
