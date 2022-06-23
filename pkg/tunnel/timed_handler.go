package tunnel

import (
	"context"
	"sync"
	"time"
)

type TimedHandler struct {
	ID        ConnID
	ttl       time.Duration
	remove    func()
	idleTimer *time.Timer
	idleLock  sync.Mutex
}

func NewTimedHandler(id ConnID, ttl time.Duration, remove func()) TimedHandler {
	return TimedHandler{ID: id, ttl: ttl, remove: remove}
}

func (h *TimedHandler) Idle() <-chan time.Time {
	return h.idleTimer.C
}

func (h *TimedHandler) GetTTL() time.Duration {
	h.idleLock.Lock()
	ttl := h.ttl
	h.idleLock.Unlock()
	return ttl
}

func (h *TimedHandler) SetTTL(ttl time.Duration) {
	h.idleLock.Lock()
	h.ttl = ttl
	h.idleLock.Unlock()
}

func (h *TimedHandler) ResetIdle() bool {
	h.idleLock.Lock()
	stopped := h.idleTimer.Stop()
	if stopped {
		h.idleTimer.Reset(h.ttl)
	}
	h.idleLock.Unlock()
	return stopped
}

func (h *TimedHandler) Start(_ context.Context) {
	h.idleLock.Lock()
	h.idleTimer = time.NewTimer(h.ttl)
	h.idleLock.Unlock()
}

func (h *TimedHandler) Stop(_ context.Context) {
	h.idleLock.Lock()
	if h.remove != nil {
		h.remove()
		h.remove = nil
		if h.idleTimer != nil {
			h.idleTimer.Stop()
		}
	}
	h.idleLock.Unlock()
}
