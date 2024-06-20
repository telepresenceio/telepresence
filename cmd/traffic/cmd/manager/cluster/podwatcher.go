package cluster

import (
	"context"
	"math"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

// PodLister helps list Pods.
// All objects returned here must be treated as read-only.
type PodLister interface {
	// List lists all Pods in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*corev1.Pod, err error)
}

type podWatcher struct {
	ipsMap     map[iputil.IPKey]struct{}
	timer      *time.Timer
	namespaces []string
	notifyCh   chan subnet.Set
	lock       sync.Mutex // Protects all access to ipsMap
}

func newPodWatcher(ctx context.Context, nss []string) *podWatcher {
	if len(nss) == 0 {
		// Create one event handler for the global informer
		nss = []string{""}
	}
	w := &podWatcher{
		ipsMap:     make(map[iputil.IPKey]struct{}),
		notifyCh:   make(chan subnet.Set),
		namespaces: nss,
	}

	var oldSubnets subnet.Set
	sendIfChanged := func() {
		w.lock.Lock()
		ips := make(iputil.IPs, len(w.ipsMap))
		i := 0
		for ip := range w.ipsMap {
			ips[i] = ip.IP()
			i++
		}
		w.lock.Unlock()

		newSubnets := subnet.NewSet(subnet.CoveringCIDRs(ips))
		if !newSubnets.Equals(oldSubnets) {
			dlog.Debugf(ctx, "podWatcher calling updateSubnets with %v", newSubnets)
			select {
			case <-ctx.Done():
				return
			case w.notifyCh <- newSubnets:
				oldSubnets = newSubnets
			}
		}
	}

	w.timer = time.AfterFunc(time.Duration(math.MaxInt64), sendIfChanged)
	for _, ns := range nss {
		inf := informer.GetFactory(ctx, ns).Core().V1().Pods().Informer()
		_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if pod, ok := obj.(*corev1.Pod); ok {
					w.onPodAdded(ctx, pod)
				}
			},
			DeleteFunc: func(obj any) {
				if pod, ok := obj.(*corev1.Pod); ok {
					w.onPodDeleted(ctx, pod)
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if pod, ok := dfsu.Obj.(*corev1.Pod); ok {
						w.onPodDeleted(ctx, pod)
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if oldPod, ok := oldObj.(*corev1.Pod); ok {
					if newPod, ok := newObj.(*corev1.Pod); ok {
						w.onPodUpdated(ctx, oldPod, newPod)
					}
				}
			},
		})
		if err != nil {
			dlog.Errorf(ctx, "failed to create pod watcher : %v", err)
		}
	}
	return w
}

func (w *podWatcher) changeNotifier(ctx context.Context, updateSubnets func(set subnet.Set)) {
	for {
		select {
		case <-ctx.Done():
			return
		case subnets := <-w.notifyCh:
			updateSubnets(subnets)
		}
	}
}

func (w *podWatcher) viable(ctx context.Context) bool {
	w.lock.Lock()
	defer w.lock.Unlock()
	if len(w.ipsMap) > 0 {
		return true
	}

	// Create the initial snapshot
	var pods []*corev1.Pod
	var err error
	for _, ns := range w.namespaces {
		lister := informer.GetFactory(ctx, ns).Core().V1().Pods().Lister()
		if ns != "" {
			pods, err = lister.Pods(ns).List(labels.Everything())
		} else {
			pods, err = lister.List(labels.Everything())
		}
		if err != nil {
			dlog.Errorf(ctx, "unable to list pods: %v", err)
			return false
		}
		for _, pod := range pods {
			w.addLocked(podIPKeys(ctx, pod))
		}
	}

	return true
}

func (w *podWatcher) onPodAdded(ctx context.Context, pod *corev1.Pod) {
	if ipKeys := podIPKeys(ctx, pod); len(ipKeys) > 0 {
		w.add(ipKeys)
	}
}

func (w *podWatcher) onPodDeleted(ctx context.Context, pod *corev1.Pod) {
	if ipKeys := podIPKeys(ctx, pod); len(ipKeys) > 0 {
		w.drop(ipKeys)
	}
}

func (w *podWatcher) onPodUpdated(ctx context.Context, oldPod, newPod *corev1.Pod) {
	added, dropped := getIPsDelta(podIPKeys(ctx, oldPod), podIPKeys(ctx, newPod))
	if len(added) > 0 {
		if len(dropped) > 0 {
			w.update(dropped, added)
		} else {
			w.add(added)
		}
	} else if len(dropped) > 0 {
		w.drop(dropped)
	}
}

const podWatcherSendDelay = 10 * time.Millisecond

func (w *podWatcher) add(ips []iputil.IPKey) {
	w.lock.Lock()
	w.addLocked(ips)
	w.lock.Unlock()
}

func (w *podWatcher) drop(ips []iputil.IPKey) {
	w.lock.Lock()
	w.dropLocked(ips)
	w.lock.Unlock()
}

func (w *podWatcher) update(dropped, added []iputil.IPKey) {
	w.lock.Lock()
	w.dropLocked(dropped)
	w.addLocked(added)
	w.lock.Unlock()
}

func (w *podWatcher) addLocked(ips []iputil.IPKey) {
	if w.ipsMap == nil {
		w.ipsMap = make(map[iputil.IPKey]struct{}, 100)
	}

	changed := false
	exists := struct{}{}
	for _, ip := range ips {
		if _, ok := w.ipsMap[ip]; !ok {
			w.ipsMap[ip] = exists
			changed = true
		}
	}
	if changed {
		w.timer.Reset(podWatcherSendDelay)
	}
}

func (w *podWatcher) dropLocked(ips []iputil.IPKey) {
	changed := false
	for _, ip := range ips {
		if _, ok := w.ipsMap[ip]; ok {
			delete(w.ipsMap, ip)
			changed = true
		}
	}
	if changed {
		w.timer.Reset(podWatcherSendDelay)
	}
}

// getIPsDelta returns the difference between the old and new IPs.
//
// NOTE! The array of the old slice is modified and used for the dropped return.
func getIPsDelta(oldIPs, newIPs []iputil.IPKey) (added, dropped []iputil.IPKey) {
	lastOI := len(oldIPs) - 1
	if lastOI < 0 {
		return newIPs, nil
	}

nextN:
	for _, n := range newIPs {
		for oi, o := range oldIPs {
			if n == o {
				oldIPs[oi] = oldIPs[lastOI]
				oldIPs = oldIPs[:lastOI]
				lastOI--
				continue nextN
			}
		}
		added = append(added, n)
	}
	if len(oldIPs) == 0 {
		oldIPs = nil
	}
	return added, oldIPs
}

func podIPKeys(ctx context.Context, pod *corev1.Pod) []iputil.IPKey {
	if pod == nil {
		return nil
	}
	status := pod.Status
	podIPs := status.PodIPs
	if len(podIPs) == 0 {
		if status.PodIP == "" {
			return nil
		}
		podIPs = []corev1.PodIP{{IP: status.PodIP}}
	}
	ips := make([]iputil.IPKey, 0, len(podIPs))
	for _, ps := range podIPs {
		ip := iputil.Parse(ps.IP)
		if ip == nil {
			dlog.Errorf(ctx, "unable to parse IP %q in pod %s.%s", ps.IP, pod.Name, pod.Namespace)
			continue
		}
		ips = append(ips, iputil.IPKey(ip))
	}
	return ips
}
