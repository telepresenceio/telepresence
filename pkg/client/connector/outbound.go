package connector

import (
	"context"
	"sort"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

// runWatchers runs a set of Kubernetes watchers that provide information from the cluster which is
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
func (kc *k8sCluster) runWatchers(c context.Context) (err error) {
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
				if kc.onNamespacesChange(c, acc, accWait) {
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

func (kc *k8sCluster) onNamespacesChange(c context.Context, acc *kates.Accumulator, accWait chan<- struct{}) bool {
	changed := func() bool {
		kc.accLock.Lock()
		defer kc.accLock.Unlock()
		return acc.Update(kc)
	}()
	if changed {
		changed = kc.refreshNamespaces(c, accWait)
	}
	return changed
}

func (kc *k8sCluster) setMappedNamespaces(c context.Context, namespaces []string) {
	sort.Strings(namespaces)
	kc.accLock.Lock()
	kc.mappedNamespaces = namespaces
	kc.accLock.Unlock()
	kc.refreshNamespaces(c, nil)
}

func (kc *k8sCluster) refreshNamespaces(c context.Context, accWait chan<- struct{}) bool {
	kc.accLock.Lock()
	namespaces := make([]string, 0, len(kc.Namespaces))
	for _, ns := range kc.Namespaces {
		if kc.shouldBeWatched(ns.Name) {
			namespaces = append(namespaces, ns.Name)
		}
	}
	sort.Strings(namespaces)

	nsChange := len(namespaces) != len(kc.lastNamespaces)
	if !nsChange {
		for i, ns := range namespaces {
			if ns != kc.lastNamespaces[i] {
				nsChange = true
				break
			}
		}
	}
	if nsChange {
		kc.lastNamespaces = namespaces
	}
	kc.accLock.Unlock()

	if nsChange {
		kc.updateDaemonNamespaces(c)
	}
	return nsChange
}

func (kc *k8sCluster) shouldBeWatched(namespace string) bool {
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

func (kc *k8sCluster) setInterceptedNamespaces(c context.Context, interceptedNamespaces map[string]struct{}) {
	kc.accLock.Lock()
	kc.interceptedNamespaces = interceptedNamespaces
	kc.accLock.Unlock()
	kc.updateDaemonNamespaces(c)
}

const clusterServerSuffix = ".svc.cluster.local"

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (kc *k8sCluster) updateDaemonNamespaces(c context.Context) {
	if kc.daemon == nil {
		// NOTE! Some tests dont't set the daemon
		return
	}

	kc.accLock.Lock()
	if len(kc.Namespaces) == 0 {
		// daemon must not be updated until the namespace watcher has made its first delivery
		kc.accLock.Unlock()
		return
	}
	namespaces := make([]string, 0, len(kc.interceptedNamespaces)+len(kc.localIntercepts))
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

	// Provide direct access to intercepted namespaces
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		paths = append(paths, ns+clusterServerSuffix+".")
	}
	dlog.Debugf(c, "posting search paths %v", paths)
	if _, err := kc.daemon.SetDnsSearchPath(c, &daemon.Paths{Paths: paths}); err != nil {
		dlog.Errorf(c, "error posting search paths to %v: %v", paths, err)
	}
}
