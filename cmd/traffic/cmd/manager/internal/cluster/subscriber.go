package cluster

import (
	"context"
	"fmt"
	"net"
	"sync"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type clusterInfoSubscribers struct {
	sync.Mutex
	idGen       int
	current     *rpc.ClusterInfo
	subscribers map[int]chan *rpc.ClusterInfo
}

func newClusterInfoSubscribers() *clusterInfoSubscribers {
	return &clusterInfoSubscribers{
		current:     &rpc.ClusterInfo{},
		subscribers: make(map[int]chan *rpc.ClusterInfo),
	}
}

func (ss *clusterInfoSubscribers) notify(ctx context.Context, ci *rpc.ClusterInfo) {
	ss.Lock()
	defer ss.Unlock()
	if clusterInfoEqual(ss.current, ci) {
		return
	}
	ss.current = ci
	for _, ch := range ss.subscribers {
		select {
		case <-ctx.Done():
			return
		case ch <- ci:
		default:
		}
	}
}

func (ss *clusterInfoSubscribers) subscribe() (int, <-chan *rpc.ClusterInfo) {
	ch := make(chan *rpc.ClusterInfo, 3)
	ss.Lock()
	id := ss.idGen
	ss.idGen++
	ss.subscribers[id] = ch
	curr := ss.current
	ss.Unlock()
	if curr.KubeDnsIp != nil {
		// Post initial state
		ch <- curr
	}
	return id, ch
}

func (ss *clusterInfoSubscribers) unsubscribe(id int) {
	ss.Lock()
	ch, ok := ss.subscribers[id]
	if ok {
		delete(ss.subscribers, id)
	}
	ss.Unlock()
	if ok {
		close(ch)
	}
}

func (ss *clusterInfoSubscribers) subscriberLoop(ctx context.Context, rec interface {
	Send(request *rpc.ClusterInfo) error
}) error {
	id, ch := ss.subscribe()
	defer ss.unsubscribe(id)
	for {
		select {
		case <-ctx.Done():
			return nil
		case ll := <-ch:
			if ll == nil {
				return nil
			}
			if err := rec.Send(ll); err != nil {
				if ctx.Err() == nil {
					return fmt.Errorf("WatchCusterInfo.Send() failed: %w", err)
				}
				return nil
			}
		}
	}
}

func clusterInfoEqual(a, b *rpc.ClusterInfo) bool {
	if len(a.PodSubnets) != len(b.PodSubnets) ||
		a.ServiceSubnet != b.ServiceSubnet ||
		a.ClusterDomain != b.ClusterDomain ||
		!net.IP(a.KubeDnsIp).Equal(b.KubeDnsIp) {
		return false
	}
	for i, aps := range a.PodSubnets {
		bps := b.PodSubnets[i]
		if !net.IP(aps.Ip).Equal(bps.Ip) || aps.Mask != bps.Mask {
			return false
		}
	}
	return true
}
