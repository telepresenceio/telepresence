package broadcastqueue

import (
	"container/list"
	"context"
	"sync"
)

// BroadcastQueue represents a set of infinitely buffering queues.  The zero value is valid and may
// be used.
type BroadcastQueue struct {
	lock sync.Mutex
	// things protected by 'lock'
	close       chan struct{}                   // can read from the channel while unlocked, IF you've already validated it's non-nil
	subscribers map[<-chan string]chan<- string // readEnd ↦ writeEnd
}

func (bq *BroadcastQueue) buffer(ctx context.Context, upstream <-chan string, downstream chan<- string) {
	var shutdown func()
	shutdown = func() {
		shutdown = func() {}
		// Do this asyncrounously because getting the lock might block a .Push() that's
		// waiting on us to read from 'upstream'!  We don't need to worry about separately
		// waiting for this goroutine because we implicitly do that when we drain
		// 'upstream'.
		go func() {
			bq.lock.Lock()
			defer bq.lock.Unlock()
			close(bq.subscribers[upstream])
			delete(bq.subscribers, upstream)
		}()
	}

	buffer := list.New()
	defer func() {
		for buffer.Len() > 0 {
			el := buffer.Front()
			downstream <- el.Value.(string)
			buffer.Remove(el)
		}
		close(downstream)
	}()

	for {
		if buffer.Len() == 0 {
			select {
			case msg, ok := <-upstream:
				if !ok {
					return
				}
				buffer.PushBack(msg)
			case <-ctx.Done():
				shutdown()
			case <-bq.close:
				shutdown()
			}
		} else {
			el := buffer.Front()
			select {
			case msg, ok := <-upstream:
				if !ok {
					return
				}
				buffer.PushBack(msg)
			case downstream <- el.Value.(string):
				buffer.Remove(el)
			case <-ctx.Done():
				shutdown()
			case <-bq.close:
			}
		}
	}
}

func (bq *BroadcastQueue) init() {
	if bq.close == nil {
		bq.close = make(chan struct{})
		bq.subscribers = make(map[<-chan string]chan<- string)
	}
}

// Push pushes a value to each of the subscriber queues.  Push does not block.  It is a runtime
// error (panic) to call Push on a BroadcastQueue that has already had Close called on it.
func (bq *BroadcastQueue) Push(msg string) {
	bq.lock.Lock()
	defer bq.lock.Unlock()
	bq.init()

	select {
	case <-bq.close:
		panic("queue: Push called on closed BroadcastQueue")
	default:
		for _, subscriber := range bq.subscribers {
			subscriber <- msg
		}
	}
}

// Write implements io.Writer, and is a trivial wrapper around Push.
func (bq *BroadcastQueue) Write(msg []byte) (int, error) {
	bq.Push(string(msg))
	return len(msg), nil
}

// Close marks each subscriber queue as "finished", all subscriber queues will be closed an further
// pushes are forbidden.
//
// After .Close() is called:
//
// - any calls to .Push() wil panic
//
// - any calls to .Subscribe() will return an already-closed channel.
//
// - any existing subscriber queue channels will be closed once they have been drained of messages
// that have already been Pushed.
func (bq *BroadcastQueue) Close() {
	bq.lock.Lock()
	defer bq.lock.Unlock()
	bq.init()

	select {
	case <-bq.close:
	default:
		close(bq.close)
	}
}

// Subscribe allocates a new queue that future calls to Push will be broadcast to.  The queue is
// removed from the pool that is broadcast to when the Context is canceled.  Reading from the
// returned channel will pop from the queue.  The channel will be closed when either the Context is
// canceled or the BroadcastQueue is closed.
func (bq *BroadcastQueue) Subscribe(ctx context.Context) <-chan string {
	bq.lock.Lock()
	defer bq.lock.Unlock()
	bq.init()

	downstream := make(chan string)

	select {
	case <-bq.close:
		close(downstream)
	default:
		upstream := make(chan string)
		bq.subscribers[upstream] = upstream
		go bq.buffer(ctx, upstream, downstream)
	}

	return downstream
}
