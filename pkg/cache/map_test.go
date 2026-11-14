package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAllDeltaSendAfterSubscriptionClose(t *testing.T) {
	sb := &subscription[string, string]{
		channel: make(chan Delta[string, string], 1),
		doneCh:  make(chan struct{}),
	}
	sb.close()

	ad := allDelta[string, string]{
		snapshot: map[string]string{"key": "value"},
	}
	require.NotPanics(t, func() {
		ad.send(sb)
	})

	_, ok := <-sb.channel
	require.False(t, ok)
}

func newStringMap() *Map[string, string] {
	return NewMap[string, string](func(a, b string) bool { return a == b }, time.Millisecond)
}

func receive(t *testing.T, ch <-chan Delta[string, string]) Delta[string, string] {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timeout waiting for delta")
	}
	return Delta[string, string]{}
}

func requireNoDelta(t *testing.T, ch <-chan Delta[string, string]) {
	t.Helper()
	select {
	case d := <-ch:
		require.Failf(t, "unexpected delta", "%v", d)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRenotifyDeliversUnchangedValue(t *testing.T) {
	m := newStringMap()
	m.Store("a", "1")
	m.Store("b", "2")
	done := make(chan struct{})
	defer close(done)
	ch := m.Subscribe(done, nil)
	require.Equal(t, map[string]string{"a": "1", "b": "2"}, receive(t, ch).Upserts)

	m.Renotify("a")
	d := receive(t, ch)
	require.Equal(t, map[string]string{"a": "1"}, d.Upserts)
	require.Empty(t, d.Removals)
	requireNoDelta(t, ch)
}

func TestRenotifyRespectsFilter(t *testing.T) {
	m := newStringMap()
	m.Store("a", "1")
	m.Store("b", "2")
	done := make(chan struct{})
	defer close(done)
	onlyB := m.Subscribe(done, func(k, _ string) bool { return k == "b" })
	all := m.Subscribe(done, nil)
	receive(t, onlyB)
	receive(t, all)

	m.Renotify("a")
	require.Equal(t, map[string]string{"a": "1"}, receive(t, all).Upserts)
	requireNoDelta(t, onlyB)
}

func TestRenotifyMissingKey(t *testing.T) {
	m := newStringMap()
	m.Store("a", "1")
	done := make(chan struct{})
	defer close(done)
	ch := m.Subscribe(done, nil)
	receive(t, ch)

	m.Renotify("missing")
	requireNoDelta(t, ch)
}

func TestRenotifyDoesNotMutateSubscriberSnapshot(t *testing.T) {
	m := newStringMap()
	m.Store("a", "1")
	done := make(chan struct{})
	defer close(done)
	first := m.Subscribe(done, nil)
	initial := receive(t, first).Upserts

	m.Renotify("a")
	receive(t, first)
	require.Equal(t, map[string]string{"a": "1"}, initial)
}

func hasUpsert(d Delta[string, string], k string) bool {
	_, ok := d.Upserts[k]
	return ok
}

func awaitUpsert(ch <-chan Delta[string, string], k string) bool {
	timeout := time.After(2 * time.Second)
	for {
		select {
		case d, ok := <-ch:
			if !ok {
				return false
			}
			if hasUpsert(d, k) {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func TestStoreRacingSubscribeIsDelivered(t *testing.T) {
	for i := 0; i < 300; i++ {
		m := newStringMap()
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			m.Store("k", "v")
		}()
		close(start)
		ch := m.Subscribe(done, nil)
		require.Truef(t, awaitUpsert(ch, "k"), "iteration %d: upsert lost", i)
		wg.Wait()
		close(done)
	}
}

func TestStoreRacingSubscribeReachesExistingSubscriber(t *testing.T) {
	for i := 0; i < 300; i++ {
		m := newStringMap()
		done := make(chan struct{})
		first := m.Subscribe(done, nil)
		receive(t, first)
		var wg sync.WaitGroup
		wg.Add(1)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			m.Store("k", "v")
		}()
		close(start)
		second := m.Subscribe(done, nil)
		require.Truef(t, awaitUpsert(first, "k"), "iteration %d: existing subscriber lost upsert", i)
		require.Truef(t, awaitUpsert(second, "k"), "iteration %d: new subscriber lost upsert", i)
		wg.Wait()
		close(done)
	}
}
