package trafficmgr

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
)

type workloadsAndServicesWatcher struct {
	sync.Mutex
	nsWatchers  map[string]*namespacedWASWatcher
	nsListeners []func()
	cond        sync.Cond
}

const (
	deployments  = 0
	replicasets  = 1
	statefulsets = 2
)

// namespacedWASWatcher is watches Workloads And Services (WAS) for a namespace.
type namespacedWASWatcher struct {
	svcWatcher *k8sapi.Watcher[*core.Service]
	wlWatchers [3]*k8sapi.Watcher[runtime.Object]
}

// svcEquals compare only the Service fields that are of interest to Telepresence. They are
//
//   - UID
//   - Name
//   - Namespace
//   - Spec.Ports
//   - Spec.Type
func svcEquals(a, b *core.Service) bool {
	aPorts := a.Spec.Ports
	bPorts := b.Spec.Ports
	if len(aPorts) != len(bPorts) {
		return false
	}
	if a.UID != b.UID || a.Name != b.Name || a.Namespace != b.Namespace || a.Spec.Type != b.Spec.Type {
		return false
	}
nextMP:
	// order is not significant (nor can it be trusted) when comparing
	for _, mp := range aPorts {
		for _, op := range bPorts {
			if mp == op {
				continue nextMP
			}
		}
		return false
	}
	return true
}

// workloadEquals compare only the workload (Deployment, ResourceSet, or StatefulSet) fields that are of interest to Telepresence. They are
//
//   - UID
//   - Name
//   - Namespace
//   - Spec.Template:
//   - Labels
//   - Containers (must contain an equal number of equally named containers with equal ports)
func workloadEquals(oa, ob runtime.Object) bool {
	a, err := k8sapi.WrapWorkload(oa)
	if err != nil {
		// This should definitely never happen
		panic(err)
	}
	b, err := k8sapi.WrapWorkload(ob)
	if err != nil {
		// This should definitely never happen
		panic(err)
	}
	if a.GetUID() != b.GetUID() || a.GetName() != b.GetName() || a.GetNamespace() != b.GetNamespace() {
		return false
	}

	aSpec := a.GetPodTemplate()
	bSpec := b.GetPodTemplate()
	if !labels.Equals(aSpec.Labels, bSpec.Labels) {
		return false
	}
	aPod := aSpec.Spec
	bPod := bSpec.Spec
	if len(aPod.Containers) != len(bPod.Containers) {
		return false
	}
	makeContainerMap := func(cs []core.Container) map[string]*core.Container {
		m := make(map[string]*core.Container, len(cs))
		for i := range cs {
			c := &cs[i]
			m[c.Name] = c
		}
		return m
	}

	portsEqual := func(a, b []core.ContainerPort) bool {
		if len(a) != len(b) {
			return false
		}
	nextAP:
		for _, ap := range a {
			for _, bp := range b {
				if ap == bp {
					continue nextAP
				}
			}
			return false
		}
		return true
	}

	am := makeContainerMap(aPod.Containers)
	bm := makeContainerMap(bPod.Containers)
	for n, ac := range am {
		bc, ok := bm[n]
		if !ok {
			return false
		}
		if !portsEqual(ac.Ports, bc.Ports) {
			return false
		}
	}
	return true
}

func newNamespaceWatcher(c context.Context, namespace string, cond *sync.Cond) *namespacedWASWatcher {
	dlog.Debugf(c, "newNamespaceWatcher %s", namespace)
	ki := k8sapi.GetK8sInterface(c)
	appsGetter := ki.AppsV1().RESTClient()
	w := &namespacedWASWatcher{
		svcWatcher: k8sapi.NewWatcher("services", ki.CoreV1().RESTClient(), cond, k8sapi.WithEquals(svcEquals), k8sapi.WithNamespace[*core.Service](namespace)),
		wlWatchers: [3]*k8sapi.Watcher[runtime.Object]{
			k8sapi.NewWatcher("deployments", appsGetter, cond, k8sapi.WithEquals(workloadEquals), k8sapi.WithNamespace[runtime.Object](namespace)),
			k8sapi.NewWatcher("replicasets", appsGetter, cond, k8sapi.WithEquals(workloadEquals), k8sapi.WithNamespace[runtime.Object](namespace)),
			k8sapi.NewWatcher("statefulsets", appsGetter, cond, k8sapi.WithEquals(workloadEquals), k8sapi.WithNamespace[runtime.Object](namespace)),
		},
	}
	return w
}

func (nw *namespacedWASWatcher) cancel() {
	nw.svcWatcher.Cancel()
	for _, w := range nw.wlWatchers {
		w.Cancel()
	}
}

func (nw *namespacedWASWatcher) hasSynced() bool {
	return nw.svcWatcher.HasSynced() &&
		nw.wlWatchers[0].HasSynced() &&
		nw.wlWatchers[1].HasSynced() &&
		nw.wlWatchers[2].HasSynced()
}

func newWASWatcher() *workloadsAndServicesWatcher {
	w := &workloadsAndServicesWatcher{
		nsWatchers: make(map[string]*namespacedWASWatcher),
	}
	w.cond.L = &w.Mutex
	return w
}

// eachService iterates over the services in the current snapshot. Unless namespace
// is the empty string, the iteration is limited to the services matching that namespace.
// The traffic-manager service is excluded.
func (w *workloadsAndServicesWatcher) eachService(c context.Context, tmns string, namespaces []string, f func(*core.Service)) {
	if len(namespaces) != 1 {
		// Produce workloads in a predictable order
		nss := make([]string, len(namespaces))
		copy(nss, namespaces)
		sort.Strings(nss)
		for _, n := range nss {
			w.eachService(c, tmns, []string{n}, f)
		}
	} else {
		ns := namespaces[0]
		w.Lock()
		nw, ok := w.nsWatchers[ns]
		w.Unlock()
		if ok {
			svcs, err := nw.svcWatcher.List(c)
			if err != nil {
				dlog.Errorf(c, "error listing services: %s", err)
				return
			}
			for _, svc := range svcs {
				// If this is our traffic-manager namespace, then exclude the traffic-manager service.
				if !(ns == tmns && svc.Labels["app"] == "traffic-manager" && svc.Labels["telepresence"] == "manager") {
					f(svc)
				}
			}
		}
	}
}

func (w *workloadsAndServicesWatcher) waitForSync(c context.Context) {
	hss := make([]cache.InformerSynced, len(w.nsWatchers))
	w.Lock()
	i := 0
	for _, nw := range w.nsWatchers {
		hss[i] = nw.hasSynced
		i++
	}
	w.Unlock()

	hasSynced := true
	for _, hs := range hss {
		if !hs() {
			hasSynced = false
			break
		}
	}
	if !hasSynced {
		// Waiting for cache sync will sometimes block, so a timeout is necessary here
		c, cancel := context.WithTimeout(c, 5*time.Second)
		defer cancel()
		cache.WaitForCacheSync(c.Done(), hss...)
	}
}

// subscribe writes to the given channel whenever relevant information has changed
// in the current snapshot.
func (w *workloadsAndServicesWatcher) subscribe(c context.Context) <-chan struct{} {
	return k8sapi.Subscribe(c, &w.cond)
}

// setNamespacesToWatch starts new watchers or kills old ones to make the current
// set of watchers reflect the nss argument.
func (w *workloadsAndServicesWatcher) setNamespacesToWatch(c context.Context, nss []string) {
	var adds []string
	desired := make(map[string]struct{})

	w.Lock()
	for _, ns := range nss {
		desired[ns] = struct{}{}
		if _, ok := w.nsWatchers[ns]; !ok {
			adds = append(adds, ns)
		}
	}
	for ns, nw := range w.nsWatchers {
		if _, ok := desired[ns]; !ok {
			delete(w.nsWatchers, ns)
			nw.cancel()
		}
	}
	for _, ns := range adds {
		w.addNSLocked(c, ns)
	}
	w.Unlock()
}

func (w *workloadsAndServicesWatcher) addNSLocked(c context.Context, ns string) *namespacedWASWatcher {
	nw := newNamespaceWatcher(c, ns, &w.cond)
	w.nsWatchers[ns] = nw
	for _, l := range w.nsListeners {
		nw.svcWatcher.AddStateListener(&k8sapi.StateListener{Cb: l})
	}
	return nw
}

func (w *workloadsAndServicesWatcher) ensureStarted(c context.Context, ns string, cb func(bool)) {
	w.Lock()
	defer w.Unlock()
	nw, ok := w.nsWatchers[ns]
	if !ok {
		nw = w.addNSLocked(c, ns)
	}
	// Starting the svcWatcher will set it to active and also trigger its state listener
	// which means a) that the set of active namespaces will change, and b) that the
	// WatchAgentsNS will restart with that namespace included.
	err := nw.svcWatcher.EnsureStarted(c, cb)
	if err != nil {
		dlog.Errorf(c, "error starting service watchers: %s", err)
	}
}

func (w *workloadsAndServicesWatcher) findMatchingWorkloads(c context.Context, svc *core.Service) ([]k8sapi.Workload, error) {
	w.Lock()
	nw := w.nsWatchers[svc.Namespace]
	w.Unlock()
	if nw == nil {
		// Extremely odd, given that the service originated from a namespace watcher
		return nil, fmt.Errorf("no watcher found for namespace %q", svc.Namespace)
	}
	return nw.findMatchingWorkloads(c, svc)
}

func (nw *namespacedWASWatcher) findMatchingWorkloads(c context.Context, svc *core.Service) ([]k8sapi.Workload, error) {
	ps := svc.Spec.Ports
	targetPortNames := make([]string, 0, len(ps))
	for i := range ps {
		tp := ps[i].TargetPort
		if tp.Type == intstr.String {
			targetPortNames = append(targetPortNames, tp.StrVal)
		} else {
			if tp.IntVal == 0 {
				// targetPort is not specified, so it defaults to the port name
				targetPortNames = append(targetPortNames, ps[i].Name)
			} else {
				// Unless all target ports are named, we cannot really use this as a filter.
				// A numeric target port will map to any container, and containers don't
				// have to expose numbered ports in order to use them.
				targetPortNames = nil
				break
			}
		}
	}

	var selector labels.Selector
	if sm := svc.Spec.Selector; len(sm) > 0 {
		selector = labels.SelectorFromSet(sm)
	} else {
		// There will be no matching workloads for this service
		return nil, nil
	}

	var allWls []k8sapi.Workload
	for i, wlw := range nw.wlWatchers {
		wls, err := wlw.List(c)
		if err != nil {
			return nil, err
		}
		for _, o := range wls {
			var wl k8sapi.Workload
			switch i {
			case deployments:
				wl = k8sapi.Deployment(o.(*apps.Deployment))
			case replicasets:
				wl = k8sapi.ReplicaSet(o.(*apps.ReplicaSet))
			case statefulsets:
				wl = k8sapi.StatefulSet(o.(*apps.StatefulSet))
			}
			if selector.Matches(labels.Set(wl.GetPodTemplate().Labels)) {
				owl, err := nw.maybeReplaceWithOwner(c, wl)
				if err != nil {
					return nil, err
				}
				allWls = append(allWls, owl)
			}
		}
	}

	// Prefer entries with matching ports. I.e. strip all non-matching if matching entries
	// are found.
	if pfWls := filterByNamedTargetPort(c, targetPortNames, allWls); len(pfWls) > 0 {
		allWls = pfWls
	}
	return allWls, nil
}

func (nw *namespacedWASWatcher) maybeReplaceWithOwner(c context.Context, wl k8sapi.Workload) (k8sapi.Workload, error) {
	var err error
	for _, or := range wl.GetOwnerReferences() {
		if or.Controller != nil && *or.Controller && or.Kind == "Deployment" {
			// Chances are that the owner's labels doesn't match, but we really want the owner anyway.
			wl, err = nw.replaceWithOwner(c, wl, or.Kind, or.Name)
			break
		}
	}
	return wl, err
}

func (nw *namespacedWASWatcher) replaceWithOwner(c context.Context, wl k8sapi.Workload, kind, name string) (k8sapi.Workload, error) {
	od, found, err := nw.wlWatchers[deployments].Get(c, &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{
			Name:      name,
			Namespace: wl.GetNamespace(),
		},
	})
	switch {
	case err != nil:
		return nil, fmt.Errorf("get %s owner %s for %s %s.%s: %v",
			kind, name, wl.GetKind(), wl.GetName(), wl.GetNamespace(), err)
	case found:
		dlog.Debugf(c, "replacing %s %s.%s, with owner %s %s", wl.GetKind(), wl.GetName(), wl.GetNamespace(), kind, name)
		return k8sapi.Deployment(od.(*apps.Deployment)), nil
	default:
		return nil, fmt.Errorf("get %s owner %s for %s %s.%s: not found", kind, name, wl.GetKind(), wl.GetName(), wl.GetNamespace())
	}
}

func filterByNamedTargetPort(c context.Context, targetPortNames []string, wls []k8sapi.Workload) []k8sapi.Workload {
	if len(targetPortNames) == 0 {
		// service ports are not all named
		return wls
	}
	var filtered []k8sapi.Workload
nextWL:
	for _, wl := range wls {
		cs := wl.GetPodTemplate().Spec.Containers
		for ci := range cs {
			ps := cs[ci].Ports
			for pi := range ps {
				name := ps[pi].Name
				for _, tpn := range targetPortNames {
					if name == tpn {
						filtered = append(filtered, wl)
						continue nextWL
					}
				}
			}
		}
		dlog.Debugf(c, "skipping %s %s.%s, it has no matching ports", wl.GetKind(), wl.GetName(), wl.GetNamespace())
	}
	return filtered
}
