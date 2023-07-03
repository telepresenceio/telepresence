package watchable_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/watchable"
)

func assertMessageMapSnapshotEqual[V watchable.Message](t *testing.T, expected, actual watchable.Snapshot[V], msgAndArgs ...any) bool {
	t.Helper()

	expectedBytes, err := json.MarshalIndent(expected, "", "    ")
	if err != nil {
		t.Fatal(err)
	}

	actualBytes, err := json.MarshalIndent(actual, "", "    ")
	if err != nil {
		t.Fatal(err)
	}

	if !assert.Equal(t, string(expectedBytes), string(actualBytes)) {
		return false
	}

	for k := range actual.State {
		if !assertDeepCopies(t, expected.State[k], actual.State[k], msgAndArgs...) {
			return false
		}
	}

	for i := range actual.Updates {
		var m watchable.Message = expected.Updates[i].Value
		if m == nil {
			continue
		}
		if !assertDeepCopies(t, expected.Updates[i].Value, actual.Updates[i].Value, msgAndArgs...) {
			return false
		}
	}

	return true
}

func TestMessageMap_Close(t *testing.T) {
	// TODO
}

func agentInfoCtor(n string) *manager.AgentInfo {
	return &manager.AgentInfo{Name: n}
}

func agentInfoCmp(a *manager.AgentInfo, n string) bool {
	return a.Name == n
}

func TestMessageMap_Delete(t *testing.T) {
	typedTestMessageMap_Delete[*manager.AgentInfo](t, agentInfoCtor)
}

func typedTestMessageMap_Delete[V watchable.Message](t *testing.T, ctor func(string) V) {
	var m watchable.Map[V]

	// Check that a delete on a zero map works
	m.Delete("a")
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{State: map[string]V{}},
		watchable.Snapshot[V]{State: m.LoadAll()})

	// Check that a normal delete works
	m.Store("a", ctor("a"))
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"a": ctor("a"),
			},
		},
		watchable.Snapshot[V]{State: m.LoadAll()})
	m.Delete("a")
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{},
		},
		watchable.Snapshot[V]{State: m.LoadAll()})

	// Check that a repeated delete works
	m.Delete("a")
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{},
		},
		watchable.Snapshot[V]{State: m.LoadAll()})
}

func TestMessageMap_Load(t *testing.T) {
	typedTestMessageMap_Load[*manager.AgentInfo](t, agentInfoCtor)
}

func typedTestMessageMap_Load[V watchable.Message](t *testing.T, ctor func(string) V) {
	var m watchable.Map[V]

	a := ctor("value")
	m.Store("k", a)

	// Check that a load returns a copy of the input object
	b, ok := m.Load("k")
	assert.True(t, ok)
	assertDeepCopies(t, a, b)
	m.Delete("k")

	// Check that a load returns nil after a delete
	c, ok := m.Load("k")
	assert.False(t, ok)
	assert.Nil(t, c)

	// Check that two sequential loads return distinct copies
	m.Store("k", a)
	d, ok := m.Load("k")
	assert.True(t, ok)
	e, ok := m.Load("k")
	assert.True(t, ok)
	assertDeepCopies(t, a, d)
	assertDeepCopies(t, a, e)
	assertDeepCopies(t, d, e)
}

func TestMessageMap_LoadAll(t *testing.T) {
	// TODO
}

func TestMessageMap_LoadAndDelete(t *testing.T) {
	typedTestMessageMap_LoadAndDelete[*manager.AgentInfo](t, agentInfoCtor)
}

func typedTestMessageMap_LoadAndDelete[V watchable.Message](t *testing.T, ctor func(string) V) {
	var m watchable.Map[V]

	a := ctor("value")
	m.Store("k", a)

	// Check that a load returns a copy of the input object
	b, ok := m.LoadAndDelete("k")
	assert.True(t, ok)
	assertDeepCopies(t, a, b)

	// Check that a load returns nil after a delete
	c, ok := m.Load("k")
	assert.False(t, ok)
	assert.Nil(t, c)

	// Now check the non-existing case
	d, ok := m.LoadAndDelete("k")
	assert.False(t, ok)
	assert.Nil(t, d)
}

func TestMessageMap_LoadOrStore(t *testing.T) {
	typedTestMessageMap_LoadOrStore[*manager.AgentInfo](t, agentInfoCtor)
}

func typedTestMessageMap_LoadOrStore[V watchable.Message](t *testing.T, ctor func(string) V) {
	var m watchable.Map[V]

	a := ctor("value")
	m.Store("k", a)

	b := ctor("value")
	assertDeepCopies(t, a, b)

	c, ok := m.LoadOrStore("k", b)
	assert.True(t, ok)
	assertDeepCopies(t, a, c)
	assertDeepCopies(t, b, c)

	d, ok := m.LoadOrStore("k", b)
	assert.True(t, ok)
	assertDeepCopies(t, a, d)
	assertDeepCopies(t, b, d)
	assertDeepCopies(t, c, d)

	e, ok := m.LoadOrStore("x", a)
	assert.False(t, ok)
	assertDeepCopies(t, a, e)
	assertDeepCopies(t, b, e)
	assertDeepCopies(t, c, e)
	assertDeepCopies(t, d, e)
}

func TestMessageMap_Store(t *testing.T) {
	// TODO
}

func TestMessageMap_CompareAndSwap(t *testing.T) {
	// TODO
}

func TestMessageMap_Subscribe(t *testing.T) {
	typedTestMessageMap_Subscribe[*manager.AgentInfo](t, agentInfoCtor)
}

func typedTestMessageMap_Subscribe[V watchable.Message](t *testing.T, ctor func(string) V) {
	ctx := dlog.NewTestContext(t, true)
	ctx, cancelCtx := context.WithCancel(ctx)
	var m watchable.Map[V]

	m.Store("a", ctor("A"))
	m.Store("b", ctor("B"))
	m.Store("c", ctor("C"))

	ch := m.Subscribe(ctx)

	// Check that a complete snapshot is immediately available
	snapshot, ok := <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"a": ctor("A"),
				"b": ctor("B"),
				"c": ctor("C"),
			},
			Updates: nil,
		},
		snapshot)

	// Check that writes don't block on the subscriber channel
	m.Store("d", ctor("D"))
	m.Store("e", ctor("E"))
	m.Store("f", ctor("F"))

	// Check that those 3 updates get coalesced in to a single read
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"a": ctor("A"),
				"b": ctor("B"),
				"c": ctor("C"),
				"d": ctor("D"),
				"e": ctor("E"),
				"f": ctor("F"),
			},
			Updates: []watchable.Update[V]{
				{Key: "d", Value: ctor("D")},
				{Key: "e", Value: ctor("E")},
				{Key: "f", Value: ctor("F")},
			},
		},
		snapshot)

	// Check that deletes work
	m.Delete("a")
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"b": ctor("B"),
				"c": ctor("C"),
				"d": ctor("D"),
				"e": ctor("E"),
				"f": ctor("F"),
			},
			Updates: []watchable.Update[V]{
				{Key: "a", Delete: true, Value: ctor("A")},
			},
		},
		snapshot)

	// Check that deletes work with LoadAndDelete
	m.LoadAndDelete("b")
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"c": ctor("C"),
				"d": ctor("D"),
				"e": ctor("E"),
				"f": ctor("F"),
			},
			Updates: []watchable.Update[V]{
				{Key: "b", Delete: true, Value: ctor("B")},
			},
		},
		snapshot)

	// Check that deletes coalesce with update
	m.Store("c", ctor("c"))
	m.Delete("c")
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"d": ctor("D"),
				"e": ctor("E"),
				"f": ctor("F"),
			},
			Updates: []watchable.Update[V]{
				{Key: "c", Value: ctor("c")},
				{Key: "c", Delete: true, Value: ctor("c")},
			},
		},
		snapshot)

	// Add some more writes, then close it
	m.Store("g", ctor("G"))
	m.Store("h", ctor("H"))
	m.Store("i", ctor("I"))
	cancelCtx()
	// Because the 'close' happens asynchronously when the context ends, we need to wait a
	// moment to ensure that it's actually closed before we hit the next step.
	time.Sleep(500 * time.Millisecond)

	// Check that the writes get coalesced in to a "close".
	snapshot, ok = <-ch
	assert.False(t, ok)
	assert.Zero(t, snapshot)

	snapshot, ok = <-ch
	assert.False(t, ok)
	assert.Zero(t, snapshot)

	snapshot, ok = <-ch
	assert.False(t, ok)
	assert.Zero(t, snapshot)
}

func TestMessageMap_SubscribeSubset(t *testing.T) {
	typedTestMessageMap_SubscribeSubset[*manager.AgentInfo](t, agentInfoCtor, agentInfoCmp)
}

func typedTestMessageMap_SubscribeSubset[V watchable.Message](t *testing.T, ctor func(string) V, comp func(V, string) bool) {
	ctx := dlog.NewTestContext(t, true)
	var m watchable.Map[V]

	m.Store("a", ctor("A"))
	m.Store("b", ctor("B"))
	m.Store("c", ctor("C"))

	ch := m.SubscribeSubset(ctx, func(k string, v V) bool {
		return !comp(v, "ignoreme")
	})

	// Check that a complete snapshot is immediately available
	snapshot, ok := <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"a": ctor("A"),
				"b": ctor("B"),
				"c": ctor("C"),
			},
		},
		snapshot)

	// Check that a no-op write doesn't trigger snapshot
	m.Store("a", ctor("A"))
	select {
	case <-ch:
	case <-time.After(10 * time.Millisecond): // just long enough that we have confidence <-ch isn't going to happen
	}

	// Check that an overwrite triggers a new snapshot
	m.Store("a", ctor("a"))
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"a": ctor("a"),
				"b": ctor("B"),
				"c": ctor("C"),
			},
			Updates: []watchable.Update[V]{
				{Key: "a", Value: ctor("a")},
			},
		},
		snapshot)

	// Check that a now-ignored entry gets deleted from the snapshot
	m.Store("a", ctor("ignoreme"))
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMessageMapSnapshotEqual(t,
		watchable.Snapshot[V]{
			State: map[string]V{
				"b": ctor("B"),
				"c": ctor("C"),
			},
			Updates: []watchable.Update[V]{
				{Key: "a", Delete: true, Value: ctor("a")},
			},
		},
		snapshot)

	// Close the channel.  For sake of test coverage, let's do some things different than in the
	// non-Subset Subscribe test:
	//  1. Use m.Close() to close *all* channels, rather than canceling the Context to close
	//     just the one (not that more than one exists in this test)
	//  2. Don't have updates that will get coalesced in to the close.
	m.Close()
	snapshot, ok = <-ch
	assert.False(t, ok)
	assert.Zero(t, snapshot)

	// Now, since we've called m.Close(), let's check that subscriptions get already-closed
	// channels.
	ch = m.SubscribeSubset(ctx, func(k string, v V) bool {
		return !comp(v, "ignoreme")
	})
	snapshot, ok = <-ch
	assert.False(t, ok)
	assert.Zero(t, snapshot)
}
