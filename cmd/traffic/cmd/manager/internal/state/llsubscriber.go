package state

import (
	"context"
	"fmt"
	"sync"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type loglevelSubscribers struct {
	sync.Mutex
	idGen       int
	current     *rpc.LogLevelRequest
	subscribers map[int]chan *rpc.LogLevelRequest
}

func newLoglevelSubscribers() *loglevelSubscribers {
	return &loglevelSubscribers{
		current:     &rpc.LogLevelRequest{},
		subscribers: make(map[int]chan *rpc.LogLevelRequest),
	}
}

func (ss *loglevelSubscribers) notify(ctx context.Context, ll *rpc.LogLevelRequest) {
	ss.Lock()
	defer ss.Unlock()
	if ss.current.LogLevel == ll.LogLevel && ss.current.Duration.AsDuration() == ll.Duration.AsDuration() {
		return
	}
	ss.current = ll
	for _, ch := range ss.subscribers {
		select {
		case <-ctx.Done():
			return
		case ch <- ll:
		default:
		}
	}
}

func (ss *loglevelSubscribers) subscribe() (int, <-chan *rpc.LogLevelRequest) {
	ch := make(chan *rpc.LogLevelRequest, 3)
	ss.Lock()
	id := ss.idGen
	ss.idGen++
	ss.subscribers[id] = ch
	curr := ss.current
	ss.Unlock()
	if curr.Duration != nil {
		// Post initial state
		ch <- curr
	}
	return id, ch
}

func (ss *loglevelSubscribers) unsubscribe(id int) {
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

func (ss *loglevelSubscribers) subscriberLoop(ctx context.Context, rec interface {
	Send(request *rpc.LogLevelRequest) error
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
					return fmt.Errorf("WatchLogLevel.Send() failed: %w", err)
				}
				return nil
			}
		}
	}
}
