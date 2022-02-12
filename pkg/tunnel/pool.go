package tunnel

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
)

// RecursionBlocker is implemented by handlers that may experience recursive calls
// back into the TUN device for IP addresses that have not been forwarded by the cluster.
// This typically happens when running a cluster in a docker container on the local host
// and making attempts to connect to an existing IP on a port that no service is
// listening to.
type RecursionBlocker interface {
	InitDone() <-chan struct{}
	Proceed() bool
	Reset(context.Context, ip.Packet) error
	Discard(ip.Packet) bool
}

// The error returned when recursion is encountered
var errRecursion = errors.New("connection recursion")

type Pool struct {
	handlers map[ConnID]Handler
	blockers map[ip.AddrKey]RecursionBlocker

	lock sync.RWMutex
}

type Handler interface {
	// Close closes the handle
	Close(context.Context)

	Start(ctx context.Context)
}

func NewPool() *Pool {
	return &Pool{handlers: make(map[ConnID]Handler), blockers: make(map[ip.AddrKey]RecursionBlocker)}
}

func (p *Pool) release(ctx context.Context, id ConnID) {
	p.lock.Lock()
	if h, ok := p.handlers[id]; ok {
		delete(p.handlers, id)
		if b, ok := h.(RecursionBlocker); ok {
			destKey := ip.MakeAddrKey(id.Destination(), id.DestinationPort())
			if p.blockers[destKey] == b {
				if !b.Proceed() {
					// Delete after a delay to ensure that all recursive attempts to
					// establish have ceased.
					time.AfterFunc(20*time.Second, func() {
						p.lock.Lock()
						delete(p.blockers, destKey)
						p.lock.Unlock()
					})
				} else {
					// Delete now (using the current lock)
					delete(p.blockers, destKey)
				}
			}
		}
	}
	count := len(p.handlers)
	p.lock.Unlock()
	dlog.Debugf(ctx, "-- POOL %s, count now is %d", id, count)
}

// HandlerCreator describes the function signature for the function that creates a handler
type HandlerCreator func(ctx context.Context, release func()) (Handler, error)

// Get finds a handler for the given id from the pool and returns it. Nil is returned if no such handler exists
func (p *Pool) Get(id ConnID) Handler {
	p.lock.RLock()
	handler := p.handlers[id]
	p.lock.RUnlock()
	return handler
}

// GetOrCreate finds a handler for the given id from the pool, or creates a new handler using the given createHandler func
// when no handler was found. The handler is returned together with a boolean flag which is set to true if
// the handler was found or false if it was created.
func (p *Pool) GetOrCreate(ctx context.Context, id ConnID, createHandler HandlerCreator) (Handler, bool, error) {
	p.lock.RLock()
	handler, ok := p.handlers[id]
	p.lock.RUnlock()

	if ok {
		return handler, true, nil
	}

	handlerCtx, cancel := context.WithCancel(ctx)
	release := func() {
		p.release(ctx, id)
		cancel()
	}

	var err error
	handler, err = createHandler(handlerCtx, release)
	if err != nil {
		return nil, false, err
	}
	if handler == nil {
		return nil, false, errors.New("createHandler function did not produce a handler")
	}

	p.lock.Lock()
	var old Handler
	if old, ok = p.handlers[id]; !ok {
		p.handlers[id] = handler
	}
	count := len(p.handlers)
	p.lock.Unlock()
	if ok {
		// Toss newly created handler. It's not started anyway.
		return old, true, nil
	}
	handler.Start(handlerCtx)
	dlog.Debugf(ctx, "++ POOL %s, count now is %d", id, count)
	return handler, false, nil
}

// GetOrCreateTCP is like GetOrCreate but with the addition that it detects and delays attempts to
// create handlers for the same destination IP and port until the first attempt has either succeeded
// or failed. If it fails, then attempts made during between the start and end of that attempt will
// fail too.
//
func (p *Pool) GetOrCreateTCP(ctx context.Context, id ConnID, createHandler HandlerCreator, initialPacket ip.Packet) (Handler, bool, error) {
	var blocker RecursionBlocker
	p.lock.RLock()
	if handler, ok := p.handlers[id]; ok {
		p.lock.RUnlock()
		return handler, true, nil
	}

	blocker = p.blockers[ip.MakeAddrKey(id.Destination(), id.DestinationPort())]
	if blocker != nil {
		p.lock.RUnlock()
		if blocker.Discard(initialPacket) {
			return nil, false, nil
		}
		<-blocker.InitDone()
		if blocker.Proceed() {
			return p.GetOrCreate(ctx, id, createHandler)
		}
		if err := blocker.Reset(ctx, initialPacket); err != nil {
			dlog.Errorf(ctx, "failed to send RST after recursion block: %v", err)
		}
		return nil, false, errRecursion
	}

	handlerCtx, cancel := context.WithCancel(ctx)
	release := func() {
		p.release(ctx, id)
		cancel()
	}

	handler, err := createHandler(handlerCtx, release)
	p.lock.RUnlock()
	if err != nil {
		return nil, false, err
	}
	if handler == nil {
		return nil, false, errors.New("createHandler function did not produce a handler")
	}

	p.lock.Lock()
	var old Handler
	var ok bool
	if old, ok = p.handlers[id]; !ok {
		p.handlers[id] = handler
		var isBlocker bool
		if blocker, isBlocker = handler.(RecursionBlocker); isBlocker {
			p.blockers[ip.MakeAddrKey(id.Destination(), id.DestinationPort())] = blocker
		}
	}
	count := len(p.handlers)
	p.lock.Unlock()
	if ok {
		// Toss newly created handler. It's not started anyway.
		return old, true, nil
	}
	handler.Start(handlerCtx)
	dlog.Debugf(ctx, "++ POOL %s, count now is %d", id, count)
	return handler, false, nil
}

func (p *Pool) CloseAll(ctx context.Context) {
	p.lock.RLock()
	handlers := make([]Handler, len(p.handlers))
	i := 0
	for _, handler := range p.handlers {
		handlers[i] = handler
		i++
	}
	p.lock.RUnlock()

	for _, handler := range handlers {
		handler.Close(ctx)
	}
}
