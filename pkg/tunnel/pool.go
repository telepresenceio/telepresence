package tunnel

import (
	"context"
	"sync"

	"github.com/datawire/dlib/dlog"
)

type Pool struct {
	handlers map[ConnID]Handler

	lock sync.RWMutex
}

type Handler interface {
	// Close closes the handle
	Close(context.Context)

	Start(ctx context.Context) error
}

func NewPool() *Pool {
	return &Pool{handlers: make(map[ConnID]Handler)}
}

func (p *Pool) release(ctx context.Context, id ConnID) {
	p.lock.Lock()
	delete(p.handlers, id)
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
	if err != nil || handler == nil {
		return nil, false, err
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
	if err = handler.Start(handlerCtx); err != nil {
		release()
		return nil, false, err
	}
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
