package cluster

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"

	"golang.org/x/exp/slices"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type clusterInfoSubscribers struct {
	sync.Mutex
	idGen       int
	current     *rpc.ClusterInfo
	subscribers map[int]chan *rpc.ClusterInfo
}

func newClusterInfoSubscribers(ci *rpc.ClusterInfo) *clusterInfoSubscribers {
	return &clusterInfoSubscribers{
		current:     ci,
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
	if curr.Dns.KubeIp != nil {
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
},
) error {
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
		a.Dns.ClusterDomain != b.Dns.ClusterDomain ||
		!net.IP(a.Dns.KubeIp).Equal(b.Dns.KubeIp) {
		return false
	}
	ipNetEQ := func(a, b *rpc.IPNet) bool {
		return a.Mask == b.Mask && bytes.Equal(a.Ip, b.Ip)
	}
	if !slices.EqualFunc(a.PodSubnets, b.PodSubnets, ipNetEQ) {
		return false
	}
	if !slices.EqualFunc(a.Routing.AlsoProxySubnets, b.Routing.AlsoProxySubnets, ipNetEQ) {
		return false
	}
	if !slices.EqualFunc(a.Routing.NeverProxySubnets, b.Routing.NeverProxySubnets, ipNetEQ) {
		return false
	}
	if !slices.EqualFunc(a.Routing.AllowConflictingSubnets, b.Routing.AllowConflictingSubnets, ipNetEQ) {
		return false
	}
	if !slices.Equal(a.Dns.IncludeSuffixes, b.Dns.IncludeSuffixes) {
		return false
	}
	if !slices.Equal(a.Dns.ExcludeSuffixes, b.Dns.ExcludeSuffixes) {
		return false
	}
	return true
}
