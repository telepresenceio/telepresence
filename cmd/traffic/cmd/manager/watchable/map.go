package watchable

import (
	"math"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v4"
)

type subscription[K comparable, V any] struct {
	channel chan map[K]V
	filter  func(K, V) bool
	mark    atomic.Bool
	doneCh  <-chan struct{}
}

type Map[K comparable, V any] struct {
	*xsync.Map[K, V]
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

// Subscribe returns a channel that will emit a snapshot of the map that corresponds to the content
// of the map filtered by the given filter. The filter is re-evaluated each time a snapshot is
// emitted.
//
// The first snapshot is emitted immediately after the call to Subscribe(), and then whenever the map
// changes a key - value binding for which the filter evaluates to true.
//
// The snapshot content will reflect actual values in the map. Mutating them will thus mutate the
// map without the map's knowledge, and hence not trigger notifications to subscribers.
//
// The returned channel will be closed when the given channel is closed.
func (m *Map[K, V]) Subscribe(done <-chan struct{}, filter func(K, V) bool) <-chan map[K]V {
	ch := make(chan map[K]V, 1)
	select {
	case <-done:
		close(ch)
	default:
		id := uuid.New()
		if filter == nil {
			filter = func(K, V) bool { return true }
		}
		sb := &subscription[K, V]{filter: filter, channel: ch}
		m.subscribers.Store(id, sb)
		go func() {
			// Trigger initial snapshot, then wait for subscription to end
			m.sendSnapshot(sb)
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
func (m *Map[K, V]) Compute(key K, f func(oldValue V, loaded bool) (newValue V, op xsync.ComputeOp)) (actual V, ok bool) {
	didMark := false
	actual, ok = m.Map.Compute(key, func(oldValue V, loaded bool) (V, xsync.ComputeOp) {
		value, del := f(oldValue, loaded)
		if del == xsync.CancelOp {
			return value, del
		}
		if del == xsync.DeleteOp {
			if loaded && m.markSubscribers(key, oldValue) {
				didMark = true
			}
		} else if !(loaded && m.equal(value, oldValue)) && m.markSubscribers(key, value) {
			didMark = true
		}
		return value, del
	})
	if didMark {
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
func (m *Map[K, V]) markSubscribers(key K, value V) (didMark bool) {
	m.subscribers.Range(func(_ uuid.UUID, sb *subscription[K, V]) bool {
		if !sb.mark.Load() && sb.filter(key, value) {
			didMark = true
			sb.mark.Store(true)
		}
		return true
	})
	return didMark
}

// notify will send a snapshot to all subscribers that have been marked.
func (m *Map[K, V]) notify() {
	m.subscribers.Range(func(_ uuid.UUID, sb *subscription[K, V]) bool {
		if sb.mark.CompareAndSwap(true, false) {
			m.sendSnapshot(sb)
		}
		return true
	})
}

// sendSnapshot evaluates and sends a snapshot to the subscriber.
func (m *Map[K, V]) sendSnapshot(sb *subscription[K, V]) {
	snap := make(map[K]V)
	m.Range(func(key K, value V) bool {
		if sb.filter(key, value) {
			snap[key] = value
		}
		return true
	})
	select {
	case <-sb.doneCh:
	case sb.channel <- snap:
	default:
	}
}
