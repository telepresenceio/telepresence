package trafficmgr

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type workloadsAndServicesWatcher struct {
	sync.Mutex
	wlKinds     []manager.WorkloadInfo_Kind
	nsWatchers  map[string]*namespacedWASWatcher
	nsListeners []func()
	cond        sync.Cond
}

const (
	deployments  = 0
	replicasets  = 1
	statefulsets = 2
	rollouts     = 3
)

// namespacedWASWatcher is watches Workloads And Services (WAS) for a namespace.
type namespacedWASWatcher struct {
	svcWatcher *k8sapi.Watcher[*core.Service]
	wlWatchers [4]*k8sapi.Watcher[runtime.Object]
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

func newNamespaceWatcher(c context.Context, namespace string, cond *sync.Cond, wlKinds []manager.WorkloadInfo_Kind) *namespacedWASWatcher {
	dlog.Debugf(c, "newNamespaceWatcher %s", namespace)
	ki := k8sapi.GetJoinedClientSetInterface(c)
	appsGetter, rolloutsGetter := ki.AppsV1().RESTClient(), ki.ArgoprojV1alpha1().RESTClient()
	w := &namespacedWASWatcher{
		svcWatcher: k8sapi.NewWatcher("services", ki.CoreV1().RESTClient(), cond, k8sapi.WithEquals(svcEquals), k8sapi.WithNamespace[*core.Service](namespace)),
		wlWatchers: [4]*k8sapi.Watcher[runtime.Object]{
			k8sapi.NewWatcher("deployments", appsGetter, cond, k8sapi.WithEquals(workloadEquals), k8sapi.WithNamespace[runtime.Object](namespace)),
			k8sapi.NewWatcher("replicasets", appsGetter, cond, k8sapi.WithEquals(workloadEquals), k8sapi.WithNamespace[runtime.Object](namespace)),
			k8sapi.NewWatcher("statefulsets", appsGetter, cond, k8sapi.WithEquals(workloadEquals), k8sapi.WithNamespace[runtime.Object](namespace)),
			nil,
		},
	}
	if slices.Contains(wlKinds, manager.WorkloadInfo_ROLLOUT) {
		w.wlWatchers[rollouts] = k8sapi.NewWatcher("rollouts", rolloutsGetter, cond, k8sapi.WithEquals(workloadEquals), k8sapi.WithNamespace[runtime.Object](namespace))
	}
	return w
}

func (nw *namespacedWASWatcher) cancel() {
	nw.svcWatcher.Cancel()
	for _, w := range nw.wlWatchers {
		if w != nil {
			w.Cancel()
		}
	}
}

func (nw *namespacedWASWatcher) hasSynced() bool {
	return nw.svcWatcher.HasSynced() &&
		nw.wlWatchers[deployments].HasSynced() &&
		nw.wlWatchers[replicasets].HasSynced() &&
		nw.wlWatchers[statefulsets].HasSynced() &&
		(nw.wlWatchers[rollouts] == nil || nw.wlWatchers[rollouts].HasSynced())
}

func newWASWatcher(knownWorkloadKinds *manager.KnownWorkloadKinds) *workloadsAndServicesWatcher {
	w := &workloadsAndServicesWatcher{
		wlKinds:    knownWorkloadKinds.Kinds,
		nsWatchers: make(map[string]*namespacedWASWatcher),
	}
	w.cond.L = &w.Mutex
	return w
}

// eachWorkload will iterate over the workloads in the current snapshot. Unless namespace
// is the empty string, the iteration is limited to the workloads matching that namespace.
// The traffic-manager workload is excluded.
func (w *workloadsAndServicesWatcher) eachWorkload(c context.Context, tmns string, namespaces []string, f func(workload k8sapi.Workload)) {
	if len(namespaces) != 1 {
		// Produce workloads in a predictable order
		nss := make([]string, len(namespaces))
		copy(nss, namespaces)
		sort.Strings(nss)
		for _, n := range nss {
			w.eachWorkload(c, tmns, []string{n}, f)
		}
	} else {
		ns := namespaces[0]
		w.Lock()
		nw, ok := w.nsWatchers[ns]
		w.Unlock()
		if ok {
			for _, wlw := range nw.wlWatchers {
				if wlw == nil {
					continue
				}
				wls, err := wlw.List(c)
				if err != nil {
					dlog.Errorf(c, "error listing workloads: %v", err)
					return
				}

			nextWorkload:
				for _, ro := range wls {
					wl, err := k8sapi.WrapWorkload(ro)
					if err != nil {
						dlog.Errorf(c, "error wrapping runtime object as a workload: %v", err)
						return
					}

					// Exclude workloads that are owned by a supported workload.
					for _, or := range wl.GetOwnerReferences() {
						if or.Controller != nil && *or.Controller {
							switch or.Kind {
							case "Deployment", "ReplicaSet", "StatefulSet":
								continue nextWorkload
							case "Rollout":
								if slices.Contains(w.wlKinds, manager.WorkloadInfo_ROLLOUT) {
									continue nextWorkload
								}
							}
						}
					}
					// If this is our traffic-manager namespace, then exclude the traffic-manager service.
					lbs := wl.GetLabels()
					if !(ns == tmns && lbs["app"] == "traffic-manager" && lbs["telepresence"] == "manager") {
						f(wl)
					}
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
	nw := newNamespaceWatcher(c, ns, &w.cond, w.wlKinds)
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
