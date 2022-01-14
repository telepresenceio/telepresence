package k8s

import (
	"context"
	"sort"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// RunWatchers runs a set of Kubernetes watchers that provide information from the cluster which is
// then used for controlling the outbound connectivity of the cluster.
//
// Initially, a watcher that watches the namespaces is created. Once it produces its first
// snapshot, some filtering is done on the list in that snapshot and then one watcher for each
// resulting namespace is created that watches pods and services. When their snapshot arrives,
// these pods and services are used when creating IP-tables that are sent to the NAT-logic in the
// daemon.
//
// The filtered list of namespaces is also used for creating a DNS search path which is propagated to
// the DNS-resolver in the daemon.
//
// When an update arrives in the namespace watcher, it will refresh the DNS-search path and the current
// set of watchers so that new watchers are added for added namespaces and watchers for namespaces that
// have been will be cancelled.
//
// If a pods and services watcher receives an update, it will send an updated IP-table to the daemon.
func (kc *Cluster) RunWatchers(c context.Context) (err error) {
	defer func() {
		if r := derror.PanicToError(recover()); r != nil {
			err = r
		}
	}()

	acc := kc.client.Watch(c,
		kates.Query{
			Name: "Namespaces",
			Kind: "namespace",
		})

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	g.Go("namespaces", func(c context.Context) error {
		accWait := kc.accWait
		for {
			select {
			case <-c.Done():
				return nil
			case <-acc.Changed():
				if kc.onNamespacesChange(c, acc) {
					if accWait != nil {
						close(accWait)
						accWait = nil // accWait is one-shot
					}
				}
			}
		}
	})
	return g.Wait()
}

func (kc *Cluster) onNamespacesChange(c context.Context, acc *kates.Accumulator) bool {
	changed := func() bool {
		kc.accLock.Lock()
		defer kc.accLock.Unlock()
		return acc.Update(&kc.curSnapshot)
	}()
	if changed {
		changed = kc.refreshNamespaces(c)
	}
	return changed
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
	kc.accLock.Lock()
	if kc.ingressInfo == nil {
		kc.accLock.Unlock()
		ingressInfo, err := kc.detectIngressBehavior(c)
		if err != nil {
			// Don't fetch again unless namespaces change
			kc.ingressInfo = []*manager.IngressInfo{}
			return nil, err
		}
		kc.accLock.Lock()
		kc.ingressInfo = ingressInfo
	}
	is := make([]*manager.IngressInfo, len(kc.ingressInfo))
	copy(is, kc.ingressInfo)
	kc.accLock.Unlock()
	return is, nil
}

func (kc *Cluster) SetMappedNamespaces(c context.Context, namespaces []string) error {
	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	} else {
		sort.Strings(namespaces)
	}

	kc.accLock.Lock()
	changed := sortedStringSlicesEqual(namespaces, kc.mappedNamespaces)
	if changed {
		kc.mappedNamespaces = namespaces
	}
	kc.accLock.Unlock()

	if !changed {
		return nil
	}
	kc.refreshNamespaces(c)
	kc.ingressInfo = nil
	return nil
}

func (kc *Cluster) refreshNamespaces(c context.Context) bool {
	kc.accLock.Lock()
	namespaces := make([]string, 0, len(kc.curSnapshot.Namespaces))
	for _, ns := range kc.curSnapshot.Namespaces {
		if kc.shouldBeWatched(ns.Name) {
			namespaces = append(namespaces, ns.Name)
		}
	}
	sort.Strings(namespaces)
	equal := sortedStringSlicesEqual(namespaces, kc.lastNamespaces)
	if !equal {
		kc.lastNamespaces = namespaces
	}
	kc.accLock.Unlock()

	if equal {
		return false
	}
	kc.updateDaemonNamespaces(c)
	return true
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

func (kc *Cluster) SetInterceptedNamespaces(c context.Context, interceptedNamespaces map[string]struct{}) {
	kc.accLock.Lock()
	kc.interceptedNamespaces = interceptedNamespaces
	kc.accLock.Unlock()
	kc.updateDaemonNamespaces(c)
}

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (kc *Cluster) updateDaemonNamespaces(c context.Context) {
	if kc.callbacks.SetDNSSearchPath == nil {
		// NOTE! Some tests dont't set the callback
		return
	}

	kc.accLock.Lock()
	if len(kc.curSnapshot.Namespaces) == 0 {
		// daemon must not be updated until the namespace watcher has made its first delivery
		kc.accLock.Unlock()
		return
	}
	namespaces := make([]string, 0, len(kc.interceptedNamespaces)+len(kc.LocalIntercepts))
	for ns := range kc.interceptedNamespaces {
		namespaces = append(namespaces, ns)
	}
	for ns := range kc.localInterceptedNamespaces {
		if _, found := kc.interceptedNamespaces[ns]; !found {
			namespaces = append(namespaces, ns)
		}
	}

	// Pass current mapped namespaces as plain names (no ending dot). The DNS-resolver will
	// create special mapping for those, allowing names like myservice.mynamespace to be resolved
	paths := make([]string, len(kc.lastNamespaces), len(kc.lastNamespaces)+len(namespaces))
	copy(paths, kc.lastNamespaces)

	// Avoid being locked for the remainder of this function.
	kc.accLock.Unlock()

	sort.Strings(namespaces)
	dlog.Debugf(c, "posting search paths %v and namespaces %v", paths, namespaces)
	if _, err := kc.callbacks.SetDNSSearchPath(c, &daemon.Paths{Paths: paths, Namespaces: namespaces}); err != nil {
		dlog.Errorf(c, "error posting search paths %v and namespaces %v to root daemon: %v", paths, namespaces, err)
	}
	dlog.Debug(c, "search paths posted successfully")
}
