package rt

import (
	"testing"
)

// newEntry stores a never-failing entry named name and returns it.
func newEntry(e *engine, name, hash string) *memoEntry {
	en := &memoEntry{name: name, hash: hash, destroy: func(Env) error { return nil }}
	e.store(en)
	return en
}

func TestEvictIdleKeepsMostRecent(t *testing.T) {
	e := newEngine()
	for _, h := range []string{"a", "b", "c", "d"} {
		newEntry(e, "workload/ns/"+h, h)
	}
	mark := e.mark()

	evicted := e.evictIdle(mark, 2)
	if len(evicted) != 2 {
		t.Fatalf("evicted %d entries, want 2", len(evicted))
	}
	// c and d were stored last, so a and b are the idle ones.
	for _, en := range evicted {
		if en.hash != "a" && en.hash != "b" {
			t.Errorf("evicted %s, want only a or b", en.hash)
		}
	}
	for _, h := range []string{"c", "d"} {
		if _, ok := e.entries[h]; !ok {
			t.Errorf("entry %s was evicted but should have been kept", h)
		}
	}
}

func TestEvictIdleSparesEntriesUsedSinceMark(t *testing.T) {
	e := newEngine()
	for _, h := range []string{"a", "b", "c", "d"} {
		newEntry(e, "workload/ns/"+h, h)
	}
	// A suite starts here, then Gets "a": its use must protect it even though
	// it was the least recently used at the mark.
	mark := e.mark()
	if _, ok := e.lookup("a"); !ok {
		t.Fatal("lookup a: not found")
	}

	for _, en := range e.evictIdle(mark, 0) {
		if en.hash == "a" {
			t.Fatal("evicted a, which was used after the mark")
		}
	}
	if _, ok := e.entries["a"]; !ok {
		t.Error("entry a is gone but was used after the mark")
	}
}

func TestEvictIdleIgnoresOtherPrefixes(t *testing.T) {
	e := newEngine()
	newEntry(e, "manager/default", "m")
	newEntry(e, "connection/ns", "c")
	newEntry(e, "workload/ns/w", "w")

	evicted := e.evictIdle(e.mark(), 0)
	if len(evicted) != 1 || evicted[0].hash != "w" {
		t.Fatalf("evicted %v, want just the workload entry", evicted)
	}
	for _, h := range []string{"m", "c"} {
		if _, ok := e.entries[h]; !ok {
			t.Errorf("entry %s was evicted but only workload/ entries are candidates", h)
		}
	}
}

// An evicted entry is destroyed, so the final teardown must not see it again.
func TestEvictIdleDropsEntryFromTeardownSequence(t *testing.T) {
	e := newEngine()
	newEntry(e, "workload/ns/a", "a")
	newEntry(e, "workload/ns/b", "b")

	e.evictIdle(e.mark(), 1)
	for _, en := range e.snapshot() {
		if en.hash == "a" {
			t.Fatal("evicted entry a is still in the teardown sequence")
		}
	}
	if len(e.snapshot()) != 1 {
		t.Errorf("teardown sequence has %d entries, want 1", len(e.snapshot()))
	}
}

func TestEvictIdleKeepsAllWhenUnderCap(t *testing.T) {
	e := newEngine()
	newEntry(e, "workload/ns/a", "a")
	newEntry(e, "workload/ns/b", "b")

	if evicted := e.evictIdle(e.mark(), 5); evicted != nil {
		t.Fatalf("evicted %v with a cap above the entry count, want none", evicted)
	}
}
