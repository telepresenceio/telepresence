package watcher

import (
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

type Value[V any] interface {
	Equal(V) bool
}

type State[K comparable, V Value[V]] interface {
	AddChangeNotifier(func(), time.Duration)
	Get(K) V
	Keys() []K
	FindFirst(func(K, V) bool) V
}

type EventHandlerState[T runtime.Object, K comparable, V Value[V]] interface {
	State[K, V]
	EventHandler[T]
}

type state[T runtime.Object, K comparable, V Value[V]] struct {
	sync.RWMutex
	keysExtractor  func(T) []K
	valueExtractor func(T) V
	state          map[K]V
	notifiers      []*stateChangeNotifier
}

type stateChangeNotifier struct {
	ticker   *time.Timer
	delay    time.Duration
	notified atomic.Bool
}

func (n *stateChangeNotifier) touch() {
	if n.notified.CompareAndSwap(true, false) {
		n.ticker.Reset(n.delay)
	}
}

func NewState[T runtime.Object, K comparable, V Value[V]](keysExtractor func(T) []K, valueExtractor func(T) V) EventHandlerState[T, K, V] {
	return &state[T, K, V]{
		keysExtractor:  keysExtractor,
		valueExtractor: valueExtractor,
		state:          make(map[K]V),
	}
}

// AddChangeNotifier adds a function that will get called after a state change. The call is delayed the given
// duration to give other adjacent modifications time to arrive.
func (s *state[T, K, V]) AddChangeNotifier(fn func(), delay time.Duration) {
	var n *stateChangeNotifier
	n = &stateChangeNotifier{
		ticker: time.AfterFunc(delay, func() {
			n.notified.Store(true)
			fn()
		}),
		delay: delay,
	}
	n.notified.Store(true)
	s.Lock()
	s.notifiers = append(s.notifiers, n)
	s.Unlock()
}

func (s *state[T, K, V]) HandleEvent(eventType watch.EventType, obj T) {
	switch eventType {
	case watch.Deleted:
		if keys := s.keysExtractor(obj); len(keys) > 0 {
			s.Lock()
			changed := false
			for _, key := range keys {
				if !changed {
					_, changed = s.state[key]
				}
				delete(s.state, key)
			}
			if changed {
				for _, n := range s.notifiers {
					n.touch()
				}
			}
			s.Unlock()
		}
	case watch.Added, watch.Modified:
		if keys := s.keysExtractor(obj); len(keys) > 0 {
			s.Lock()
			changed := false
			value := s.valueExtractor(obj)
			for _, key := range keys {
				if !changed {
					oldValue, ok := s.state[key]
					changed = !(ok && oldValue.Equal(value))
				}
				s.state[key] = value
			}
			if changed {
				for _, n := range s.notifiers {
					n.touch()
				}
			}
			s.Unlock()
		}
	}
}

func (s *state[T, K, V]) Keys() []K {
	s.RLock()
	keys := make([]K, len(s.state))
	i := 0
	for k := range s.state {
		keys[i] = k
		i++
	}
	s.RUnlock()
	return keys
}

func (s *state[T, K, V]) Get(key K) V {
	s.RLock()
	v := s.state[key]
	s.RUnlock()
	return v
}

func (s *state[T, K, V]) FindFirst(filter func(K, V) bool) (found V) {
	s.RLock()
	for k, v := range s.state {
		if filter(k, v) {
			found = v
			break
		}
	}
	s.RUnlock()
	return
}
