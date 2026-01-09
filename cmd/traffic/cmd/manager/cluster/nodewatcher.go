package cluster

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"sync"
	"time"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	listersCore "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type nodeWatcher struct {
	informer cache.SharedIndexInformer
	subnets  subnet.Set
	changed  chan struct{}
	lock     sync.Mutex // Protects all access to subnets
}

func newNodeWatcher(ctx context.Context, lister listersCore.NodeLister, informer cache.SharedIndexInformer) (*nodeWatcher, error) {
	nodes, err := lister.List(labels.Everything())
	if err != nil {
		clog.Errorf(ctx, "unable to list nodes: %v", err)
		return nil, err
	}
	subnets := make(subnet.Set)
	podIP := managerutil.GetEnv(ctx).PodIP
	viable := false
	clog.Infof(ctx, "Scanning %d nodes", len(nodes))
	for _, node := range nodes {
		for _, sn := range nodeSubnets(ctx, node) {
			if sn.Contains(podIP) {
				viable = true
			}
			subnets.Add(sn)
		}
	}
	if !viable {
		return nil, fmt.Errorf("no node subnets contain the traffic manager pod IP %q", podIP)
	}
	clog.Infof(ctx, "Found %d subnets", len(subnets))
	w := &nodeWatcher{
		informer: informer,
		subnets:  subnets,
		changed:  make(chan struct{}, 10),
	}

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if node, ok := obj.(*core.Node); ok {
				w.onNodeAdded(ctx, node)
			}
		},
		DeleteFunc: func(obj any) {
			if node, ok := obj.(*core.Node); ok {
				w.onNodeDeleted(ctx, node)
			} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
				if node, ok := dfsu.Obj.(*core.Node); ok {
					w.onNodeDeleted(ctx, node)
				}
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			if oldNode, ok := oldObj.(*core.Node); ok {
				if newNode, ok := newObj.(*core.Node); ok {
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
	var lastSent subnet.Set
	sendUpdate := func() {
		w.lock.Lock()
		doSend := !w.subnets.Equals(lastSent)
		if doSend {
			lastSent = w.subnets.Clone()
		}
		w.lock.Unlock()
		if doSend {
			clog.Debugf(ctx, "nodeWatcher calling updateSubnets with %v", lastSent)
			updateSubnets(lastSent)
		}
	}

	// Send an initial update.
	sendUpdate()

	// And then send updates with a short delay every time a change arrives.
	const nodeCollectTime = 100 * time.Millisecond
	triggerSend := time.AfterFunc(math.MaxInt64, sendUpdate)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.changed:
			triggerSend.Reset(nodeCollectTime)
		}
	}
}

func (w *nodeWatcher) viable(ctx context.Context) bool {
	return true
}

func (w *nodeWatcher) onNodeAdded(ctx context.Context, node *core.Node) {
	if subnets := nodeSubnets(ctx, node); len(subnets) > 0 {
		w.add(subnets)
	}
}

func (w *nodeWatcher) onNodeDeleted(ctx context.Context, node *core.Node) {
	if subnets := nodeSubnets(ctx, node); len(subnets) > 0 {
		w.drop(subnets)
	}
}

func (w *nodeWatcher) onNodeUpdated(ctx context.Context, oldNode, newNode *core.Node) {
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

func (w *nodeWatcher) add(subnets []netip.Prefix) {
	w.lock.Lock()
	if w.addLocked(subnets) {
		w.changed <- struct{}{}
	}
	w.lock.Unlock()
}

func (w *nodeWatcher) drop(subnets []netip.Prefix) {
	w.lock.Lock()
	if w.dropLocked(subnets) {
		w.changed <- struct{}{}
	}
	w.lock.Unlock()
}

func (w *nodeWatcher) update(dropped, added []netip.Prefix) {
	w.lock.Lock()
	if w.dropLocked(dropped) || w.addLocked(added) {
		w.changed <- struct{}{}
	}
	w.lock.Unlock()
}

func (w *nodeWatcher) addLocked(subnets []netip.Prefix) bool {
	changed := false
	for _, sn := range subnets {
		if w.subnets.Add(sn) {
			changed = true
		}
	}
	return changed
}

func (w *nodeWatcher) dropLocked(subnets []netip.Prefix) bool {
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
func getSubnetsDelta(oldSubnets, newSubnets []netip.Prefix) (added, dropped []netip.Prefix) {
	lastOI := len(oldSubnets) - 1
	if lastOI < 0 {
		return newSubnets, nil
	}

nextN:
	for _, n := range newSubnets {
		for oi, o := range oldSubnets {
			if n == o {
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

// compareNodeConditions compares two NodeCondition objects and returns an integer indicating their
// relative order based on transition time (later is higher) and status (true is higher).
func compareNodeConditions(a, b *core.NodeCondition) int {
	cmp := a.LastTransitionTime.Compare(b.LastTransitionTime.Time)
	if cmp != 0 {
		return cmp
	}
	switch a.Status {
	case b.Status:
		return 0
	case core.ConditionTrue:
		return 1
	default:
		return -1
	}
}

// nodeReady returns true if the last state transition to a Ready condition is true.
func nodeReady(node *core.Node) bool {
	if node == nil {
		return false
	}
	var lastCond *core.NodeCondition
	conds := node.Status.Conditions
	for i := range conds {
		cond := &conds[i]
		if cond.Type == core.NodeReady && (lastCond == nil || compareNodeConditions(cond, lastCond) > 0) {
			lastCond = cond
		}
	}
	return lastCond != nil && lastCond.Status == core.ConditionTrue
}

func nodeSubnets(ctx context.Context, node *core.Node) []netip.Prefix {
	if !nodeReady(node) {
		return nil
	}
	spec := node.Spec
	cidrs := spec.PodCIDRs
	if len(cidrs) == 0 && spec.PodCIDR != "" {
		cidrs = []string{spec.PodCIDR}
	}
	subnets := make([]netip.Prefix, 0, len(cidrs))
	for _, cs := range cidrs {
		cidr, err := netip.ParsePrefix(cs)
		if err != nil {
			clog.Errorf(ctx, "unable to parse podCIDR %q in node %s", cs, node.Name)
			continue
		}
		subnets = append(subnets, cidr)
	}
	if len(subnets) == 0 {
		subnets = nil
	}
	return subnets
}
