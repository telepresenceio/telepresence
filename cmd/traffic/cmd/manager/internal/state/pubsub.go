package state

import (
	"sync"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// TODO rewrite with generics when
// golang 1.18 releases
type PubSub struct {
	mu   sync.Mutex
	subs []chan ChGroup
	// closed bool
}

type ChGroup struct {
	Ch chan *rpc.HealthMessage
	Wg sync.WaitGroup
}

func NewPubSub() *PubSub {
	ps := &PubSub{}
	ps.subs = make([]chan ChGroup, 0, 100) // max 100 agents ?? make resizer?
	return ps
}

func (ps *PubSub) Subscribe() <-chan ChGroup {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ch := make(chan ChGroup, 10)
	ps.subs = append(ps.subs, ch)
	return ch
}

func (ps *PubSub) Unsubscribe(ch <-chan ChGroup) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for i, sub := range ps.subs {
		if ch == sub {
			close(sub)
			ps.subs = append(ps.subs[:i], ps.subs[i+1:]...)
		}
	}
}

func (ps *PubSub) Publish() ChGroup {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	/*
		if ps.closed {
			return ChGroup{}
		}
	*/

	chgroup := ChGroup{
		Ch: make(chan *rpc.HealthMessage, 10),
		Wg: sync.WaitGroup{},
	}
	chgroup.Wg.Add(len(ps.subs))
	go func() {
		chgroup.Wg.Wait()
		close(chgroup.Ch)
	}()

	for _, sub := range ps.subs {
		go func(sub chan ChGroup) {
			sub <- chgroup
		}(sub)
	}

	return chgroup
}

/*
func (ps *PubSub) Close() {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.closed {
		ps.closed = true
		for _, ch := range ps.subs {
			close(ch)
		}
	}
}
*/
