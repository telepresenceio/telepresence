package cluster

import (
	"context"
	"net"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	licorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type podWatcher struct {
	lister   licorev1.PodLister
	informer cache.SharedIndexInformer
	ipsMap   map[iputil.IPKey]struct{}
	changed  time.Time
	lock     sync.Mutex // Protects all access to ipsMap
}

func newPodWatcher(lister licorev1.PodLister, informer cache.SharedIndexInformer) *podWatcher {
	return &podWatcher{
		lister:   lister,
		informer: informer,
		ipsMap:   make(map[iputil.IPKey]struct{}),
	}
}

func (w *podWatcher) changeNotifier(ctx context.Context, updateSubnets func([]*net.IPNet)) {
	// Check for changes every 5 second
	const podReviewPeriod = 5 * time.Second

	// The time we wait from when the first change arrived until we actually do something. This
	// so that more changes can arrive (hopefully all of them) before everything is recalculated.
	const podCollectTime = 3 * time.Second

	ticker := time.NewTicker(podReviewPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		w.lock.Lock()
		if w.changed.IsZero() || time.Since(w.changed) < podCollectTime {
			w.lock.Unlock()
			continue
		}
		w.changed = time.Time{}
		ips := make(iputil.IPs, len(w.ipsMap))
		i := 0
		for ip := range w.ipsMap {
			ips[i] = ip.IP()
			i++
		}
		w.lock.Unlock()
		subnets := subnet.CoveringCIDRs(ips)
		dlog.Debugf(ctx, "podWatcher calling updateSubnets with %v", subnets)
		updateSubnets(subnets)
	}
}

func (w *podWatcher) viable(ctx context.Context) bool {
	pods, err := w.lister.List(labels.Everything())
	if err != nil {
		dlog.Errorf(ctx, "unable to list pods: %v", err)
		return false
	}

	// Create the initial snapshot
	w.lock.Lock()
	defer w.lock.Unlock()
	changed := false
	for _, pod := range pods {
		if w.addLocked(podIPKeys(ctx, pod)) {
			changed = true
		}
	}
	if changed {
		w.changed = time.Now()
	}
	return changed
}

func (w *podWatcher) watch(ctx context.Context) {
	w.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			w.onPodAdded(ctx, obj.(*corev1.Pod))
		},
		DeleteFunc: func(obj interface{}) {
			w.onPodDeleted(ctx, obj.(*corev1.Pod))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			w.onPodUpdated(ctx, oldObj.(*corev1.Pod), newObj.(*corev1.Pod))
		},
	})
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

func (w *podWatcher) add(ips []iputil.IPKey) {
	w.lock.Lock()
	if w.addLocked(ips) {
		// If this was the first change since the last subnet calculation, then store
		// its timestamp. Subsequent changes will not change that timestamp until it's
		// reset by the subnet compute worker.
		if w.changed.IsZero() {
			w.changed = time.Now()
		}
	}
	w.lock.Unlock()
}

func (w *podWatcher) drop(ips []iputil.IPKey) {
	w.lock.Lock()
	if w.dropLocked(ips) {
		// If this was the first change since the last subnet calculation, then store
		// its timestamp. Subsequent changes will not change that timestamp until it's
		// reset by the subnet compute worker.
		if w.changed.IsZero() {
			w.changed = time.Now()
		}
	}
	w.lock.Unlock()
}

func (w *podWatcher) update(dropped, added []iputil.IPKey) {
	w.lock.Lock()
	if w.dropLocked(dropped) || w.addLocked(added) {
		// If this was the first change since the last subnet calculation, then store
		// its timestamp. Subsequent changes will not change that timestamp until it's
		// reset by the subnet compute worker.
		if w.changed.IsZero() {
			w.changed = time.Now()
		}
	}
	w.lock.Unlock()
}

func (w *podWatcher) addLocked(ips []iputil.IPKey) bool {
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
	return changed
}

func (w *podWatcher) dropLocked(ips []iputil.IPKey) bool {
	changed := false
	for _, ip := range ips {
		if _, ok := w.ipsMap[ip]; ok {
			delete(w.ipsMap, ip)
			changed = true
		}
	}
	return changed
}

// getIPsDelta returns the difference between the old and new IPs.
//
// NOTE! The array of the old slice is modified and used for the dropped return
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
