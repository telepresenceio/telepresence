package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/datawire/telepresence2/rpc/v2/daemon"
)

// k8sWatcher watcher of pods and services.
type k8sWatcher struct {
	Pods      []*kates.Pod
	Services  []*kates.Service
	cancel    context.CancelFunc
	namespace string
}

// startWatchers initializes a set of Kubernetes watchers that provide information from the cluster
// which is then used for controlling the outbound connectivity of the cluster.
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
func (kc *k8sCluster) startWatchers(c context.Context, accWait chan struct{}) (err error) {
	defer func() {
		if r := dutil.PanicToError(recover()); r != nil {
			err = r
		}
	}()

	acc := kc.client.Watch(c,
		kates.Query{
			Name: "Namespaces",
			Kind: "namespace",
		})

	g := dgroup.ParentGroup(c)
	g.Go("watch-k8s-namespaces", func(c context.Context) error {
		for {
			select {
			case <-c.Done():
				return nil
			case <-acc.Changed():
				if kc.onNamespacesChange(c, acc, accWait) {
					accWait = nil // accWait is one-shot
				}
			}
		}
	})

	g.Go("watch-k8s", func(c context.Context) error {
		for {
			select {
			case <-c.Done():
				return nil
			case <-kc.watcherChanged:
				kc.updateDaemonTable(c)
			}
		}
	})
	return nil
}

func (kc *k8sCluster) onNamespacesChange(c context.Context, acc *kates.Accumulator, accWait chan<- struct{}) bool {
	kc.accLock.Lock()
	if !acc.Update(kc) {
		kc.accLock.Unlock()
		return false
	}
	return kc.refreshNamespacesAndUnlock(c, accWait)
}

func (kc *k8sCluster) setMappedNamespaces(c context.Context, namespaces []string) {
	sort.Strings(namespaces)
	kc.accLock.Lock()
	kc.mappedNamespaces = namespaces
	kc.refreshNamespacesAndUnlock(c, nil)
}

func (kc *k8sCluster) refreshNamespacesAndUnlock(c context.Context, accWait chan<- struct{}) bool {
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
	kc.accLock.Unlock()

	if nsChange {
		kc.refreshWatchers(c, namespaces, accWait)
		kc.lastNamespaces = namespaces
	}
	return nsChange
}

func (kc *k8sCluster) shouldBeWatched(namespace string) bool {
	if len(kc.mappedNamespaces) == 0 {
		return !(namespace == managerNamespace || strings.HasPrefix(namespace, "kube-"))
	}
	for _, n := range kc.mappedNamespaces {
		if n == namespace {
			return true
		}
	}
	return false
}

// refreshWatchers ensures that the current set of namespaces are watched.
//
// The accWait channel should only be passed once to this function and will be closed once all watchers have
// received their initial snapshot.
func (kc *k8sCluster) refreshWatchers(c context.Context, namespaces []string, accWait chan<- struct{}) {
	kc.accLock.Lock()
	defer kc.accLock.Unlock()

	var onChange func(int)
	if accWait == nil {
		onChange = func(_ int) {}
	} else {
		// close accWait when all watchers have received an update
		initialReceived := make([]bool, len(namespaces))
		onChange = func(index int) {
			kc.accLock.Lock()
			defer kc.accLock.Unlock()
			if accWait == nil || initialReceived[index] {
				return
			}
			initialReceived[index] = true
			for _, r := range initialReceived {
				if !r {
					return
				}
			}
			al := accWait
			accWait = nil
			close(al)
		}
	}

	if kc.watchers == nil {
		kc.watchers = make(map[string]*k8sWatcher, len(namespaces))
	}

	keep := make(map[string]struct{})
	for i, namespace := range namespaces {
		keep[namespace] = struct{}{}
		if _, ok := kc.watchers[namespace]; ok {
			continue
		}
		watcher := &k8sWatcher{namespace: namespace}
		var wc context.Context
		wc, watcher.cancel = context.WithCancel(c)
		kc.watchers[namespace] = watcher

		go kc.watchPodsAndServices(wc, watcher, i, onChange)
	}

	// Cancel watchers of undesired namespaces
	for namespace, watcher := range kc.watchers {
		if _, ok := keep[namespace]; !ok {
			watcher.cancel()
		}
	}
}

func (kc *k8sCluster) watchPodsAndServices(c context.Context, watcher *k8sWatcher, i int, onChange func(int)) {
	nAcc := kc.client.Watch(c,
		kates.Query{
			Name:      "Services",
			Namespace: watcher.namespace,
			Kind:      "service",
		},
		kates.Query{
			Name:      "Pods",
			Namespace: watcher.namespace,
			Kind:      "pod",
		})

	dlog.Infof(c, "Watching namespace %q", watcher.namespace)

	for {
		select {
		case <-c.Done():
			dlog.Infof(c, "Watch of namespace %q cancelled", watcher.namespace)
			return
		case x := <-nAcc.Changed():
			if func() bool {
				kc.accLock.Lock()
				defer kc.accLock.Unlock()
				return nAcc.Update(watcher)
			}() {
				kc.watcherChanged <- x
			}
			onChange(i)
		}
	}
}

// updateDaemonNamespaces will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (kc *k8sCluster) updateDaemonNamespaces(c context.Context, nsMap map[string]struct{}) {
	if kc.daemon == nil {
		// NOTE! Some tests dont't set the daemon
		return
	}

	namespaces := make([]string, len(nsMap))
	i := 0
	for ns := range nsMap {
		namespaces[i] = ns
		i++
	}
	sort.Strings(namespaces)

	paths := make([]string, 0, len(namespaces)+3)
	for _, ns := range namespaces {
		paths = append(paths, ns+".svc.cluster.local.")
	}
	dlog.Debugf(c, "posting search paths to %s", strings.Join(paths, " "))
	if _, err := kc.daemon.SetDnsSearchPath(c, &daemon.Paths{Paths: paths}); err != nil {
		dlog.Errorf(c, "error posting search paths to %s: %v", strings.Join(paths, " "), err)
	}
}

// updateDaemonTable will create IP-tables based on the current snapshots of services and pods
// that the watchers have produced.
func (kc *k8sCluster) updateDaemonTable(c context.Context) {
	if kc.daemon == nil {
		// NOTE! Some tests dont't set the daemon
		return
	}
	table := &daemon.Table{Name: "kubernetes"}
	kc.accLock.Lock()
	for _, watcher := range kc.watchers {
		for _, svc := range watcher.Services {
			updateTableFromService(svc, table)
		}
		for _, pod := range watcher.Pods {
			updateTableFromPod(pod, table)
		}
	}
	kc.accLock.Unlock()

	// Send updated table to daemon
	if _, err := kc.daemon.Update(c, table); err != nil {
		dlog.Errorf(c, "error posting update to %s: %v", table.Name, err)
	}
}

func updateTableFromService(svc *kates.Service, table *daemon.Table) {
	spec := &svc.Spec
	ip := spec.ClusterIP
	// for headless services the IP is None, we
	// should properly handle these by listening
	// for endpoints and returning multiple A
	// records at some point
	if ip == "" || ip == "None" {
		return
	}
	qName := svc.Name + "." + svc.Namespace + ".svc.cluster.local"

	ports := ""
	for _, port := range spec.Ports {
		if ports == "" {
			ports = fmt.Sprintf("%d", port.Port)
		} else {
			ports = fmt.Sprintf("%s,%d", ports, port.Port)
		}

		// Kubernetes creates records for all named ports, of the form
		// _my-port-name._my-port-protocol.my-svc.my-namespace.svc.cluster-domain.example
		// https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/#srv-records
		if port.Name != "" {
			proto := strings.ToLower(string(port.Protocol))
			table.Routes = append(table.Routes, &daemon.Route{
				Name:   fmt.Sprintf("_%v._%v.%v", port.Name, proto, qName),
				Ip:     ip,
				Port:   ports,
				Proto:  proto,
				Target: ProxyRedirPort,
			})
		}
	}

	table.Routes = append(table.Routes, &daemon.Route{
		Name:   qName,
		Ip:     ip,
		Port:   ports,
		Proto:  "tcp",
		Target: ProxyRedirPort,
	})
}

func updateTableFromPod(pod *kates.Pod, table *daemon.Table) {
	qname := ""

	hostname := pod.Spec.Hostname
	if hostname != "" {
		qname += hostname
	}

	subdomain := pod.Spec.Subdomain
	if subdomain != "" {
		qname += "." + subdomain
	}

	if qname == "" {
		// Note: this is a departure from kubernetes, kubernetes will
		// simply not publish a dns name in this case.
		qname = pod.Name + "." + pod.Namespace + ".pod.cluster.local"
	} else {
		qname += ".svc.cluster.local"
	}

	ip := pod.Status.PodIP
	if ip != "" {
		table.Routes = append(table.Routes, &daemon.Route{
			Name:   qname,
			Ip:     ip,
			Proto:  "tcp",
			Target: ProxyRedirPort,
		})
	}
}
