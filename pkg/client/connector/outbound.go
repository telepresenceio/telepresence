package connector

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

// k8sWatcher watcher of pods and services.
type k8sWatcher struct {
	Pods      []*kates.Pod
	Services  []*kates.Service
	Endpoints []*kates.Endpoints
	cancel    context.CancelFunc
	namespace string
}

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
					accWait = nil // accWait is one-shot
				}
			}
		}
	})

	g.Go("services-and-pods", func(c context.Context) error {
		// Don't call kc.updateDaemonTable until we have a complete snapshot, signaled by
		// accWait getting closed.
		needsUpdate := false
		for {
			select {
			case <-c.Done():
				return nil
			case <-kc.watcherChanged:
				needsUpdate = true
				continue
			case <-kc.accWait:
				if needsUpdate {
					kc.updateDaemonTable(c)
				}
			}
			break
		}
		// OK, accWait is closed, we can loop normally now.
		for {
			select {
			case <-c.Done():
				return nil
			case <-kc.watcherChanged:
				kc.updateDaemonTable(c)
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
		kc.refreshWatchers(c, namespaces, accWait)
		kc.updateDaemonNamespaces(c)
	}
	return nsChange
}

func (kc *k8sCluster) shouldBeWatched(namespace string) bool {
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

// refreshWatchersLocked ensures that the current set of namespaces are watched.
//
// The accWait channel should only be passed once to this function and will be closed once all watchers have
// received their initial snapshot.
func (kc *k8sCluster) refreshWatchers(c context.Context, namespaces []string, accWait chan<- struct{}) {
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

	kc.accLock.Lock()
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
	kc.accLock.Unlock()

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
			Name:      "Endpoints",
			Namespace: watcher.namespace,
			Kind:      "endpoints",
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
			updateTableFromService(c, svc, watcher.Endpoints, table)
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

func updateTableFromService(c context.Context, svc *kates.Service, endpoints []*kates.Endpoints, table *daemon.Table) {
	spec := &svc.Spec
	clusterIP := spec.ClusterIP
	var ips []string
	var err error
	// Handle headless services separately since they don't have cluster IPs
	if clusterIP == "" || clusterIP == "None" {
		ips, err = getIPsFromHeadlessService(svc, spec.ExternalName, endpoints)
		if err != nil {
			dlog.Errorf(c, "Error finding IPs for service %s: %s", svc.Name, err)
			return
		}
		if len(ips) == 0 {
			if svc.Name != "traffic-manager" {
				dlog.Errorf(c, "Found no IPs for service %s", svc.Name)
			}
			return
		}
	} else {
		ips = append(ips, clusterIP)
	}
	qName := svc.Name + "." + svc.Namespace + clusterServerSuffix

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
				Name:  fmt.Sprintf("_%v._%v.%v", port.Name, proto, qName),
				Ips:   ips,
				Port:  ports,
				Proto: proto,
			})
		}
	}

	table.Routes = append(table.Routes, &daemon.Route{
		Name:  qName,
		Ips:   ips,
		Port:  ports,
		Proto: "tcp",
	})
}

// Headless Services don't have a cluster IP so we need to get their IP(s) separately.
// Docs on headless services can be found here
// https://kubernetes.io/docs/concepts/services-networking/service/#headless-services
func getIPsFromHeadlessService(svc *kates.Service, externalName string, endpoints []*kates.Endpoints) ([]string, error) {
	serviceName := svc.Name
	var ips []string
	// ExternalName services don't have endpoints so we handle them separately.
	if externalName != "" {
		domainIPs, err := net.LookupIP(externalName)
		if err != nil {
			return nil, err
		}
		if len(domainIPs) == 0 {
			return nil, nil
		}
		for _, ip := range domainIPs {
			ips = append(ips, ip.String())
		}
	} else {
		// see if there's an associated endpoint with the headless service
		for i := range endpoints {
			if endpoints[i].ObjectMeta.Name == serviceName {
				matchingEndpoint := endpoints[i]
				if len(matchingEndpoint.Subsets) > 0 {
					endpointSubset := matchingEndpoint.Subsets[0]

					// Endpoints have two sets of addresses: Addresses and NotReadyAddresses
					// We only want to forward traffic to pods that are ready one so we only
					// loop through the ready addresses
					for _, address := range endpointSubset.Addresses {
						ips = append(ips, address.IP)
					}
					if len(ips) == 0 {
						return nil, nil
					}
				} else {
					return nil, nil
				}
				break
			}
		}
	}
	return ips, nil
}

func updateTableFromPod(pod *kates.Pod, table *daemon.Table) {
	ips := []string{pod.Status.PodIP}
	if len(ips) == 0 {
		return
	}
	qname := ""

	if hostname := pod.Spec.Hostname; hostname != "" {
		qname = hostname
	}

	if subdomain := pod.Spec.Subdomain; subdomain != "" {
		if qname == "" {
			// Can't have a subdomain without a hostname, so default to using pod's name as hostname
			qname = pod.Name
		}
		qname += "." + subdomain
	}

	if qname == "" {
		// Note: this is a departure from kubernetes, kubernetes will
		// simply not publish a dns name in this case.
		qname = pod.Name + "." + pod.Namespace + ".pod.cluster.local"
	} else {
		qname += "." + pod.Namespace + clusterServerSuffix
	}

	table.Routes = append(table.Routes, &daemon.Route{
		Name:  qname,
		Ips:   ips,
		Proto: "tcp",
	})
}
