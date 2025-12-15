package cache

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v4"
)

type Delta[K comparable, V any] struct {
	Upserts  map[K]V
	Removals map[K]V
}

func (delta *Delta[K, V]) Merge(other Delta[K, V]) {
	if len(delta.Upserts) == 0 {
		delta.Upserts = other.Upserts
	} else {
		for k, v := range other.Upserts {
			delta.Upserts[k] = v
		}
		for k := range other.Removals {
			delete(delta.Upserts, k)
		}
	}
	if len(delta.Removals) == 0 {
		delta.Removals = other.Removals
	} else {
		for k, v := range other.Removals {
			delta.Removals[k] = v
		}
		for k := range other.Upserts {
			delete(delta.Removals, k)
		}
	}
}

type subscription[K comparable, V any] struct {
	channel     chan Delta[K, V]
	include     func(K, V) bool
	initialized atomic.Bool
	mark        atomic.Bool
	doneCh      <-chan struct{}
}

type Map[K comparable, V any] struct {
	*xsync.Map[K, V]
	snapLock    sync.Mutex
	snapshot    map[K]V
	equal       func(V, V) bool
	subscribers *xsync.Map[uuid.UUID, *subscription[K, V]]
	notifyDelay time.Duration
	notifier    *time.Timer
}

// NewMap creates a new Map instance configured with the given options.
func NewMap[K comparable, V any](equal func(V, V) bool, notifyDelay time.Duration, config ...func(*xsync.MapConfig)) *Map[K, V] {
	m := &Map[K, V]{
		Map:         xsync.NewMap[K, V](config...),
		equal:       equal,
		subscribers: xsync.NewMap[uuid.UUID, *subscription[K, V]](),
		notifyDelay: notifyDelay,
	}
	m.notifier = time.AfterFunc(math.MaxInt64, m.notify)
	return m
}

// Subscribe returns a channel that will emit deltas that corresponds to modifications of the contained
// values filtered by the given filter.
//
// The first delta is a snapshot of all values, and it is emitted immediately after the call to Subscribe().
// After that, a new Delta is emitted then whenever the map changes a value for which the filter evaluates
// to true.
//
// The values contained in a delta will reflect actual values in the map and must be considered immutable.
// Mutating them will mutate the map without the map's knowledge and hence not trigger notifications to
// subscribers.
//
// The returned channel will be closed when the given channel is closed.
func (m *Map[K, V]) Subscribe(done <-chan struct{}, includeFilter func(K, V) bool) <-chan Delta[K, V] {
	ch := make(chan Delta[K, V], 1)
	select {
	case <-done:
		close(ch)
	default:
		m.notifier.Reset(math.MaxInt64)

		id := uuid.New()
		sb := &subscription[K, V]{include: includeFilter, channel: ch, doneCh: done}
		sb.mark.Store(true)
		m.subscribers.Store(id, sb)

		// Fire notifier immediately to send the snapshot.
		m.notify()
		go func() {
			<-done
			m.subscribers.Delete(id)
			close(ch)
		}()
	}
	return ch
}

// Compute either sets the computed new value for the key or deletes the value for the key.
// When the delete result of the valueFn function is set to true, the value will be deleted if it exists.
// When delete is set to false, the value is updated to the newValue. The ok result indicates whether the
// value was computed and stored, thus, is present in the map. The actual result contains the new value in
// cases where the value was computed and stored. See the example for a few use cases.
//
// This call locks a hash table bucket while the compute function is executed. It means that modifications
// on other entries in the bucket will be blocked until the valueFn executes. Consider this when the function
// includes long-running operations.
func (m *Map[K, V]) Compute(key K, f func(V, bool) (V, xsync.ComputeOp)) (V, bool) {
	modified := false
	actual, ok := m.Map.Compute(key, func(v V, loaded bool) (V, xsync.ComputeOp) {
		fv, op := f(v, loaded)
		switch op {
		case xsync.CancelOp:
		case xsync.UpdateOp:
			if loaded && m.equal(fv, v) {
				fv = v
				op = xsync.CancelOp
			} else {
				modified = true
				m.markSubscribers(key, fv)
			}
		case xsync.DeleteOp:
			modified = true
			m.markSubscribers(key, v)
		}
		return fv, op
	})
	if modified {
		m.notifier.Reset(m.notifyDelay)
	}
	return actual, ok
}

// CompareAndSwap checks if the current value for the given key equals the oldValue, and if
// so, swaps the current value for the newValue.
// The swapped result reports whether the value was swapped.
func (m *Map[K, V]) CompareAndSwap(key K, oldValue, newValue V) (swapped bool) {
	m.Compute(key, func(cur V, loaded bool) (V, xsync.ComputeOp) {
		if loaded && m.equal(cur, oldValue) {
			swapped = true
			return newValue, xsync.UpdateOp
		}
		return oldValue, xsync.CancelOp
	})
	return swapped
}

// Delete deletes the value for a key.
func (m *Map[K, V]) Delete(key K) {
	m.Compute(key, func(oldValue V, loaded bool) (V, xsync.ComputeOp) {
		if !loaded {
			return oldValue, xsync.CancelOp
		}
		return oldValue, xsync.DeleteOp
	})
}

// LoadAll return a map of all entries.
func (m *Map[K, V]) LoadAll() map[K]V {
	mr := make(map[K]V, m.Size())
	m.Range(func(key K, value V) bool {
		mr[key] = value
		return true
	})
	return mr
}

// LoadMatching return a map of all entries matching the given filter.
func (m *Map[K, V]) LoadMatching(filter func(K, V) bool) map[K]V {
	mr := make(map[K]V)
	m.Range(func(key K, value V) bool {
		if filter(key, value) {
			mr[key] = value
		}
		return true
	})
	return mr
}

// LoadAndDelete deletes the value for a key, returning the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map[K, V]) LoadAndDelete(key K) (previous V, loaded bool) {
	m.Compute(key, func(oldValue V, wasLoaded bool) (V, xsync.ComputeOp) {
		if wasLoaded {
			previous = oldValue
			loaded = true
			return oldValue, xsync.DeleteOp
		}
		return oldValue, xsync.CancelOp
	})
	return previous, loaded
}

// LoadAndStore stores a new value for the key and returns the existing one, if present. The loaded result is true if the
// existing value was loaded, false otherwise.
func (m *Map[K, V]) LoadAndStore(key K, value V) (existing V, loaded bool) {
	m.Compute(key, func(oldValue V, wasLoaded bool) (V, xsync.ComputeOp) {
		if wasLoaded {
			existing = oldValue
			loaded = true
		}
		return value, xsync.UpdateOp
	})
	return existing, loaded
}

// LoadOrCompute returns the existing value for the key if present. Otherwise, it computes the value using
// the provided function and returns the computed value. The loaded result is true if the value was loaded,
// false if stored.
//
// This call locks a hash table bucket while the compute function is executed. It means that modifications
// on other entries in the bucket will be blocked until the valueFn executes. Consider this when the function
// includes long-running operations.
func (m *Map[K, V]) LoadOrCompute(key K, valueFn func() V) (actual V, loaded bool) {
	actual, _ = m.Compute(key, func(oldValue V, wasLoaded bool) (V, xsync.ComputeOp) {
		if wasLoaded {
			loaded = true
			return oldValue, xsync.CancelOp
		}
		return valueFn(), xsync.UpdateOp
	})
	return actual, loaded
}

// LoadOrStore returns the existing value for the key if present. Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *Map[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	actual, _ = m.Compute(key, func(oldValue V, wasLoaded bool) (V, xsync.ComputeOp) {
		if wasLoaded {
			loaded = true
			return oldValue, xsync.CancelOp
		}
		return value, xsync.UpdateOp
	})
	return actual, loaded
}

// Store stores a new value for the key.
func (m *Map[K, V]) Store(key K, value V) {
	m.Compute(key, func(oldValue V, wasLoaded bool) (V, xsync.ComputeOp) { return value, xsync.UpdateOp })
}

// markSubscribers marks all subscribers interested in the given key and value binding.
func (m *Map[K, V]) markSubscribers(key K, value V) {
	m.subscribers.Range(func(_ uuid.UUID, sb *subscription[K, V]) bool {
		// Don't run the filter if the subscriber is marked already.
		if !sb.mark.Load() && sb.include(key, value) {
			sb.mark.Store(true)
		}
		return true
	})
}

// notify will send a snapshot to all subscribers that have been marked.
func (m *Map[K, V]) notify() {
	// We need to loop until all marked snapshots have been sent, because new marks may be added during sending.
	delta := m.makeDelta()
	for didSend := true; didSend; {
		didSend = false
		m.subscribers.Range(func(_ uuid.UUID, sb *subscription[K, V]) bool {
			if sb.mark.CompareAndSwap(true, false) {
				delta.send(sb)
				didSend = true
			}
			return true
		})
	}
}

type allDelta[K comparable, V any] struct {
	snapshot map[K]V
	upserts  map[K]V
	removals map[K]V
}

func filteredMap[K comparable, V any](m map[K]V, include func(K, V) bool) map[K]V {
	if include == nil {
		return m
	}
	fm := make(map[K]V)
	for k, v := range m {
		if include(k, v) {
			fm[k] = v
		}
	}
	return fm
}

func (ad *allDelta[K, V]) filteredDelta(initialized bool, include func(K, V) bool) Delta[K, V] {
	var upserts map[K]V
	var removals map[K]V
	if initialized {
		upserts = ad.upserts
		removals = ad.removals
	} else {
		upserts = ad.snapshot
		removals = nil
	}
	return Delta[K, V]{Upserts: filteredMap(upserts, include), Removals: filteredMap(removals, include)}
}

func (ad *allDelta[K, V]) send(sb *subscription[K, V]) {
	fd := ad.filteredDelta(sb.initialized.Swap(true), sb.include)
	if len(fd.Upserts) == 0 && len(fd.Removals) == 0 {
		return
	}
	select {
	case <-sb.doneCh:
	case prevDelta := <-sb.channel:
		// The previous delta was not read by the subscriber yet, so we need to merge it with the new delta
		// and put it back on the channel.
		prevDelta.Merge(fd)
		sb.channel <- prevDelta
	default:
		// The channel is empty, so we can just send the delta.
		sb.channel <- fd
	}
}

func (m *Map[K, V]) makeDelta() allDelta[K, V] {
	m.snapLock.Lock()
	previous := m.snapshot
	current := m.LoadAll()
	m.snapshot = current
	var upserts map[K]V
	for k, v := range current {
		if prev, ok := previous[k]; !(ok && m.equal(prev, v)) {
			if upserts == nil {
				upserts = make(map[K]V)
			}
			upserts[k] = v
		}
	}
	var removals map[K]V
	for k, v := range previous {
		if _, ok := current[k]; !ok {
			if removals == nil {
				removals = make(map[K]V)
			}
			removals[k] = v
		}
	}
	m.snapLock.Unlock()
	return allDelta[K, V]{
		snapshot: current,
		upserts:  upserts,
		removals: removals,
	}
}
