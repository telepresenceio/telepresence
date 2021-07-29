//+build ignore

package watchable_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"VALPKG"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable"
)

func assertMAPTYPESnapshotEqual(t *testing.T, expected, actual watchable.MAPTYPESnapshot, msgAndArgs ...interface{}) bool {
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
		if expected.Updates[i].Value == nil {
			continue
		}
		if !assertDeepCopies(t, expected.Updates[i].Value, actual.Updates[i].Value, msgAndArgs...) {
			return false
		}
	}

	return true
}

func TestMAPTYPE_Close(t *testing.T) {
	// TODO
}

func TestMAPTYPE_Delete(t *testing.T) {
	var m watchable.MAPTYPE

	// Check that a delete on a zero map works
	m.Delete("a")
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{State: map[string]VALTYPE{}},
		watchable.MAPTYPESnapshot{State: m.LoadAll()})

	// Check that a normal delete works
	m.Store("a", VALCTOR{TESTFIELD: "a"})
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"a": VALCTOR{TESTFIELD: "a"},
			},
		},
		watchable.MAPTYPESnapshot{State: m.LoadAll()})
	m.Delete("a")
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{},
		},
		watchable.MAPTYPESnapshot{State: m.LoadAll()})

	// Check that a repeated delete works
	m.Delete("a")
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{},
		},
		watchable.MAPTYPESnapshot{State: m.LoadAll()})
}

func TestMAPTYPE_Load(t *testing.T) {
	var m watchable.MAPTYPE

	a := VALCTOR{TESTFIELD: "value"}
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

func TestMAPTYPE_LoadAll(t *testing.T) {
	// TODO
}

func TestMAPTYPE_LoadAndDelete(t *testing.T) {
	var m watchable.MAPTYPE

	a := VALCTOR{TESTFIELD: "value"}
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

func TestMAPTYPE_LoadOrStore(t *testing.T) {
	var m watchable.MAPTYPE

	a := VALCTOR{TESTFIELD: "value"}
	m.Store("k", a)

	b := VALCTOR{TESTFIELD: "value"}
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

func TestMAPTYPE_Store(t *testing.T) {
	// TODO
}

func TestMAPTYPE_CompareAndSwap(t *testing.T) {
	// TODO
}

func TestMAPTYPE_Subscribe(t *testing.T) {
	ctx := dlog.NewTestContext(t, true)
	ctx, cancelCtx := context.WithCancel(ctx)
	var m watchable.MAPTYPE

	m.Store("a", VALCTOR{TESTFIELD: "A"})
	m.Store("b", VALCTOR{TESTFIELD: "B"})
	m.Store("c", VALCTOR{TESTFIELD: "C"})

	ch := m.Subscribe(ctx)

	// Check that a complete snapshot is immediately available
	snapshot, ok := <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"a": VALCTOR{TESTFIELD: "A"},
				"b": VALCTOR{TESTFIELD: "B"},
				"c": VALCTOR{TESTFIELD: "C"},
			},
			Updates: nil,
		},
		snapshot)

	// Check that writes don't block on the subscriber channel
	m.Store("d", VALCTOR{TESTFIELD: "D"})
	m.Store("e", VALCTOR{TESTFIELD: "E"})
	m.Store("f", VALCTOR{TESTFIELD: "F"})

	// Check that those 3 updates get coalesced in to a single read
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"a": VALCTOR{TESTFIELD: "A"},
				"b": VALCTOR{TESTFIELD: "B"},
				"c": VALCTOR{TESTFIELD: "C"},
				"d": VALCTOR{TESTFIELD: "D"},
				"e": VALCTOR{TESTFIELD: "E"},
				"f": VALCTOR{TESTFIELD: "F"},
			},
			Updates: []watchable.MAPTYPEUpdate{
				{Key: "d", Value: VALCTOR{TESTFIELD: "D"}},
				{Key: "e", Value: VALCTOR{TESTFIELD: "E"}},
				{Key: "f", Value: VALCTOR{TESTFIELD: "F"}},
			},
		},
		snapshot)

	// Check that deletes work
	m.Delete("a")
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"b": VALCTOR{TESTFIELD: "B"},
				"c": VALCTOR{TESTFIELD: "C"},
				"d": VALCTOR{TESTFIELD: "D"},
				"e": VALCTOR{TESTFIELD: "E"},
				"f": VALCTOR{TESTFIELD: "F"},
			},
			Updates: []watchable.MAPTYPEUpdate{
				{Key: "a", Delete: true, Value: VALCTOR{TESTFIELD: "A"}},
			},
		},
		snapshot)

	// Check that deletes work with LoadAndDlete
	m.LoadAndDelete("b")
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"c": VALCTOR{TESTFIELD: "C"},
				"d": VALCTOR{TESTFIELD: "D"},
				"e": VALCTOR{TESTFIELD: "E"},
				"f": VALCTOR{TESTFIELD: "F"},
			},
			Updates: []watchable.MAPTYPEUpdate{
				{Key: "b", Delete: true, Value: VALCTOR{TESTFIELD: "B"}},
			},
		},
		snapshot)

	// Check that deletes coalesce with update
	m.Store("c", VALCTOR{TESTFIELD: "c"})
	m.Delete("c")
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"d": VALCTOR{TESTFIELD: "D"},
				"e": VALCTOR{TESTFIELD: "E"},
				"f": VALCTOR{TESTFIELD: "F"},
			},
			Updates: []watchable.MAPTYPEUpdate{
				{Key: "c", Value: VALCTOR{TESTFIELD: "c"}},
				{Key: "c", Delete: true, Value: VALCTOR{TESTFIELD: "c"}},
			},
		},
		snapshot)

	// Add some more writes, then close it
	m.Store("g", VALCTOR{TESTFIELD: "G"})
	m.Store("h", VALCTOR{TESTFIELD: "H"})
	m.Store("i", VALCTOR{TESTFIELD: "I"})
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

func TestMAPTYPE_SubscribeSubset(t *testing.T) {
	ctx := dlog.NewTestContext(t, true)
	var m watchable.MAPTYPE

	m.Store("a", VALCTOR{TESTFIELD: "A"})
	m.Store("b", VALCTOR{TESTFIELD: "B"})
	m.Store("c", VALCTOR{TESTFIELD: "C"})

	ch := m.SubscribeSubset(ctx, func(k string, v VALTYPE) bool {
		return v.TESTFIELD != "ignoreme"
	})

	// Check that a complete snapshot is immediately available
	snapshot, ok := <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"a": VALCTOR{TESTFIELD: "A"},
				"b": VALCTOR{TESTFIELD: "B"},
				"c": VALCTOR{TESTFIELD: "C"},
			},
		},
		snapshot)

	// Check that a no-op write doesn't trigger snapshot
	m.Store("a", VALCTOR{TESTFIELD: "A"})
	select {
	case <-ch:
	case <-time.After(10 * time.Millisecond): // just long enough that we have confidence <-ch isn't going to happen
	}

	// Check that an overwrite triggers a new snapshot
	m.Store("a", VALCTOR{TESTFIELD: "a"})
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"a": VALCTOR{TESTFIELD: "a"},
				"b": VALCTOR{TESTFIELD: "B"},
				"c": VALCTOR{TESTFIELD: "C"},
			},
			Updates: []watchable.MAPTYPEUpdate{
				{Key: "a", Value: VALCTOR{TESTFIELD: "a"}},
			},
		},
		snapshot)

	// Check that a now-ignored entry gets deleted from the snapshot
	m.Store("a", VALCTOR{TESTFIELD: "ignoreme"})
	snapshot, ok = <-ch
	assert.True(t, ok)
	assertMAPTYPESnapshotEqual(t,
		watchable.MAPTYPESnapshot{
			State: map[string]VALTYPE{
				"b": VALCTOR{TESTFIELD: "B"},
				"c": VALCTOR{TESTFIELD: "C"},
			},
			Updates: []watchable.MAPTYPEUpdate{
				{Key: "a", Delete: true, Value: VALCTOR{TESTFIELD: "a"}},
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
	ch = m.SubscribeSubset(ctx, func(k string, v VALTYPE) bool {
		return v.TESTFIELD != "ignoreme"
	})
	snapshot, ok = <-ch
	assert.False(t, ok)
	assert.Zero(t, snapshot)
}
