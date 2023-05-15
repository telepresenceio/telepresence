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
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type nodeWatcher struct {
	lister   licorev1.NodeLister
	informer cache.SharedIndexInformer
	subnets  subnet.Set
	changed  time.Time
	lock     sync.Mutex // Protects all access to subnets
}

func newNodeWatcher(ctx context.Context, lister licorev1.NodeLister, informer cache.SharedIndexInformer) (*nodeWatcher, error) {
	w := &nodeWatcher{
		lister:   lister,
		informer: informer,
		subnets:  make(subnet.Set),
	}
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if node, ok := obj.(*corev1.Node); ok {
				w.onNodeAdded(ctx, node)
			}
		},
		DeleteFunc: func(obj any) {
			if node, ok := obj.(*corev1.Node); ok {
				w.onNodeDeleted(ctx, node)
			} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
				if node, ok := dfsu.Obj.(*corev1.Node); ok {
					w.onNodeDeleted(ctx, node)
				}
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			if oldNode, ok := oldObj.(*corev1.Node); ok {
				if newNode, ok := newObj.(*corev1.Node); ok {
					w.onNodeUpdated(ctx, oldNode, newNode)
				}
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (w *nodeWatcher) changeNotifier(ctx context.Context, updateSubnets func(set subnet.Set)) {
	// Check for changes every 5 second
	const nodeReviewPeriod = 5 * time.Second

	// The time we wait from when the first change arrived until we actually do something. This
	// so that more changes can arrive (hopefully all of them) before everything is recalculated.
	const nodeCollectTime = 3 * time.Second

	ticker := time.NewTicker(nodeReviewPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		w.lock.Lock()
		if w.changed.IsZero() || time.Since(w.changed) < nodeCollectTime {
			w.lock.Unlock()
			continue
		}
		w.changed = time.Time{}
		subnets := w.subnets.Clone()
		w.lock.Unlock()
		dlog.Debugf(ctx, "nodeWatcher calling updateSubnets with %v", subnets)
		updateSubnets(subnets)
	}
}

func (w *nodeWatcher) viable(ctx context.Context) bool {
	// Create the initial snapshot
	w.lock.Lock()
	defer w.lock.Unlock()
	if len(w.subnets) > 0 {
		return true
	}
	if !w.changed.IsZero() {
		// Tested before but didn't produce anything
		return false
	}

	nodes, err := w.lister.List(labels.Everything())
	if err != nil {
		dlog.Errorf(ctx, "unable to list nodes: %v", err)
		return false
	}

	changed := false
	dlog.Infof(ctx, "Scanning %d nodes", len(nodes))
	for _, node := range nodes {
		if w.addLocked(nodeSubnets(ctx, node)) {
			changed = true
		}
	}
	w.changed = time.Now()
	if changed {
		// Don't consider the node subnets viable unless they contain the IP Of the traffic manager pod.
		podIP := managerutil.GetEnv(ctx).PodIP
		w.subnets.AppendSortedTo(nil)
		for _, sn := range w.subnets.AppendSortedTo(nil) {
			if sn.Contains(podIP) {
				dlog.Infof(ctx, "Found %d subnets", len(w.subnets))
				return true
			}
		}
		w.subnets = make(subnet.Set)
		dlog.Errorf(ctx, "no node subnet contains traffic-manager IP %s", podIP)
	} else {
		dlog.Info(ctx, "No subnets found")
	}
	return false
}

func (w *nodeWatcher) onNodeAdded(ctx context.Context, node *corev1.Node) {
	if subnets := nodeSubnets(ctx, node); len(subnets) > 0 {
		w.add(subnets)
	}
}

func (w *nodeWatcher) onNodeDeleted(ctx context.Context, node *corev1.Node) {
	if subnets := nodeSubnets(ctx, node); len(subnets) > 0 {
		w.drop(subnets)
	}
}

func (w *nodeWatcher) onNodeUpdated(ctx context.Context, oldNode, newNode *corev1.Node) {
	added, dropped := getSubnetsDelta(nodeSubnets(ctx, oldNode), nodeSubnets(ctx, newNode))
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

func (w *nodeWatcher) add(subnets []*net.IPNet) {
	w.lock.Lock()
	if w.addLocked(subnets) {
		// If this was the first change since the last subnet calculation, then store
		// its timestamp. Subsequent changes will not change that timestamp until it's
		// reset by the subnet compute worker.
		if w.changed.IsZero() {
			w.changed = time.Now()
		}
	}
	w.lock.Unlock()
}

func (w *nodeWatcher) drop(subnets []*net.IPNet) {
	w.lock.Lock()
	if w.dropLocked(subnets) {
		// If this was the first change since the last subnet calculation, then store
		// its timestamp. Subsequent changes will not change that timestamp until it's
		// reset by the subnet compute worker.
		if w.changed.IsZero() {
			w.changed = time.Now()
		}
	}
	w.lock.Unlock()
}

func (w *nodeWatcher) update(dropped, added []*net.IPNet) {
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

func (w *nodeWatcher) addLocked(subnets []*net.IPNet) bool {
	changed := false
	for _, sn := range subnets {
		if w.subnets.Add(sn) {
			changed = true
		}
	}
	return changed
}

func (w *nodeWatcher) dropLocked(subnets []*net.IPNet) bool {
	changed := false
	last := len(w.subnets) - 1
	if last < 0 {
		return false
	}

	for _, ds := range subnets {
		if w.subnets.Delete(ds) {
			changed = true
		}
	}
	return changed
}

// getSubnetsDelta returns the difference between the old and new subnet slices.
//
// NOTE! The array of the old slice is modified and used for the dropped return.
func getSubnetsDelta(oldSubnets, newSubnets []*net.IPNet) (added, dropped []*net.IPNet) {
	lastOI := len(oldSubnets) - 1
	if lastOI < 0 {
		return newSubnets, nil
	}

nextN:
	for _, n := range newSubnets {
		for oi, o := range oldSubnets {
			if subnet.Equal(n, o) {
				oldSubnets[oi] = oldSubnets[lastOI]
				oldSubnets = oldSubnets[:lastOI]
				lastOI--
				continue nextN
			}
		}
		added = append(added, n)
	}
	if len(oldSubnets) == 0 {
		oldSubnets = nil
	}
	return added, oldSubnets
}

func nodeSubnets(ctx context.Context, node *corev1.Node) []*net.IPNet {
	if node == nil {
		return nil
	}
	spec := node.Spec
	cidrs := spec.PodCIDRs
	if len(cidrs) == 0 && spec.PodCIDR != "" {
		cidrs = []string{spec.PodCIDR}
	}
	subnets := make([]*net.IPNet, 0, len(cidrs))
	for _, cs := range cidrs {
		_, cidr, err := net.ParseCIDR(cs)
		if err != nil {
			dlog.Errorf(ctx, "unable to parse podCIDR %q in node %s", cs, node.Name)
			continue
		}
		subnets = append(subnets, cidr)
	}
	if len(subnets) == 0 {
		subnets = nil
	}
	return subnets
}
