package rt

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Env is the context passed to a fixture's Provision, Adopt, and Destroy
// functions.
type Env struct {
	Ctx context.Context
	T   testing.TB
	R   *Runtime
}

// Fixture describes one memoized resource, identified by Hash (a canonical
// spec hash). ProvisionFn creates it; AdoptFn (dev mode only, best-effort)
// reuses one left by a previous run; DestroyFn tears it down and must be
// idempotent.
type Fixture[T any] struct {
	Name        string
	Hash        string
	ProvisionFn func(Env) (T, error)
	AdoptFn     func(Env) (T, bool)
	DestroyFn   func(Env, T) error
	// AlwaysDestroy marks a fixture whose DestroyFn runs at the end of every
	// run, even in dev mode where KeepResources() would otherwise leave it
	// for adoption. Used for per-test ephemera (PrivateNamespace,
	// SecondaryManager) that must never accumulate across runs since they
	// are never adopted anyway.
	AlwaysDestroy bool
}

// memoEntry is the type-erased record the engine keeps per fixture hash.
// destroy closes over the fixture's typed DestroyFn and value at the point
// it was stored.
type memoEntry struct {
	name    string
	hash    string
	value   any
	failed  error
	always  bool
	destroy func(Env) error
	// used is the engine clock reading of this entry's most recent Get,
	// which orders eviction candidates.
	used uint64
}

// engine is the single, mutex-guarded fixture store owned by Runtime. It
// never holds its lock while a Provision/Adopt/Destroy function runs, so a
// ProvisionFn may itself call Get on another fixture (nested fixtures).
type engine struct {
	mu       sync.Mutex
	entries  map[string]*memoEntry
	sequence []*memoEntry // provision order, for LIFO teardown
	clock    uint64       // ticks on every Get, stamping memoEntry.used
}

func newEngine() *engine {
	return &engine{entries: map[string]*memoEntry{}}
}

// tick advances the engine clock and returns the new reading. Caller holds
// e.mu.
func (e *engine) tick() uint64 {
	e.clock++
	return e.clock
}

func (e *engine) lookup(hash string) (*memoEntry, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	en, ok := e.entries[hash]
	if ok {
		en.used = e.tick()
	}
	return en, ok
}

func (e *engine) store(en *memoEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	en.used = e.tick()
	e.entries[en.hash] = en
	e.sequence = append(e.sequence, en)
}

// mark returns the current clock reading, which callers pass to evictIdle as
// the boundary before which an entry counts as idle.
func (e *engine) mark() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

// evictIdle removes the workload memo entries whose last Get predates since,
// keeping the keep most recently used among them, and returns them for the
// caller to destroy. Entries are dropped from sequence as well: unlike
// invalidate, eviction does destroy the resource, so the final teardown has
// nothing left to do for them.
//
// Bounding by since is what makes eviction safe: an entry the running suite
// touched is stamped at or after the mark taken when that suite began, so
// only fixtures no live suite is holding are ever considered.
func (e *engine) evictIdle(since uint64, keep int) []*memoEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	var idle []*memoEntry
	for _, en := range e.entries {
		if en.used <= since && en.destroy != nil && strings.HasPrefix(en.name, workloadFixturePrefix) {
			idle = append(idle, en)
		}
	}
	slices.SortFunc(idle, func(a, b *memoEntry) int { return cmp.Compare(b.used, a.used) })
	if len(idle) <= keep {
		return nil
	}
	evicted := idle[keep:]
	for _, en := range evicted {
		delete(e.entries, en.hash)
	}
	e.sequence = slices.DeleteFunc(e.sequence, func(en *memoEntry) bool {
		return slices.Contains(evicted, en)
	})
	return evicted
}

// evictByPrefix removes the memo entries whose name starts with prefix and
// returns them for the caller to destroy. Like evictIdle, and unlike
// invalidate, it drops them from sequence too: eviction destroys the
// resource, so the final teardown has nothing left to do for them.
func (e *engine) evictByPrefix(prefix string) []*memoEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	var evicted []*memoEntry
	for _, en := range e.entries {
		if en.destroy != nil && strings.HasPrefix(en.name, prefix) {
			evicted = append(evicted, en)
		}
	}
	for _, en := range evicted {
		delete(e.entries, en.hash)
	}
	e.sequence = slices.DeleteFunc(e.sequence, func(en *memoEntry) bool {
		return slices.Contains(evicted, en)
	})
	return evicted
}

// invalidate removes the memo entry for hash, used by Mutate's t.Cleanup so
// the next Get re-provisions. It does not run DestroyFn: the resource is in
// unknown state, not necessarily gone. The sequence entry is kept so a final
// teardown still destroys the resource even when no later Get re-provisions
// it; DestroyFn idempotency makes the possible duplicate destroy harmless.
func (e *engine) invalidate(hash string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.entries, hash)
}

// invalidateSiblings removes memo entries whose name starts with prefix but
// whose hash differs from keepHash: fixtures describing the same underlying
// resource in a different configuration. Provisioning one manager spec makes
// every other spec's memoized handle stale, since they all share one
// release.
func (e *engine) invalidateSiblings(prefix, keepHash string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for h, en := range e.entries {
		if h != keepHash && strings.HasPrefix(en.name, prefix) {
			delete(e.entries, h)
		}
	}
}

func (e *engine) snapshot() []*memoEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*memoEntry{}, e.sequence...)
}

// Get returns the memoized value for f, provisioning (or, in dev mode,
// adopting) it on first use under the calling test's t. A fixture that
// failed to provision earlier causes an immediate Skip rather than a retry.
func Get[T any](t testing.TB, f *Fixture[T]) T {
	t.Helper()
	return getOrProvision(t, f)
}

// Mutate is like Get, but registers a t.Cleanup that invalidates the memo
// entry when the calling test ends, so the next Get re-provisions. A
// fixture's ProvisionFn must converge from any prior state (helm
// install-or-upgrade, apply + rollout wait, reconnect) since Mutate does not
// imply a fresh start.
func Mutate[T any](t testing.TB, f *Fixture[T]) T {
	t.Helper()
	v := getOrProvision(t, f)
	t.Cleanup(func() { R().engine.invalidate(f.Hash) })
	return v
}

func getOrProvision[T any](t testing.TB, f *Fixture[T]) T {
	t.Helper()
	r := R()
	if en, ok := r.engine.lookup(f.Hash); ok {
		if en.failed != nil {
			t.Skipf("fixture %s failed earlier: %v", f.Name, en.failed)
		}
		return en.value.(T) //nolint:forcetypeassert // engine keys are per-Fixture[T] hashes
	}

	start := time.Now()
	env := Env{Ctx: r.ctx, T: t, R: r}
	var (
		value  T
		err    error
		action string
	)
	if !r.fresh && f.AdoptFn != nil {
		if v, ok := f.AdoptFn(env); ok {
			value, action = v, "adopted"
		}
	}
	if action == "" {
		value, err = f.ProvisionFn(env)
		action = "provisioned"
	}
	dur := time.Since(start)
	if err != nil {
		r.engine.store(&memoEntry{name: f.Name, hash: f.Hash, failed: err})
		r.manifest.recordFixture(f.Name, f.Hash, "failed", dur)
		t.Fatalf("fixture %s: %v", f.Name, err)
		var zero T
		return zero
	}
	r.engine.store(&memoEntry{
		name:   f.Name,
		hash:   f.Hash,
		value:  value,
		always: f.AlwaysDestroy,
		destroy: func(e Env) error {
			if f.DestroyFn == nil {
				return nil
			}
			return f.DestroyFn(e, value)
		},
	})
	r.manifest.recordFixture(f.Name, f.Hash, action, dur)
	r.Infof("[rtest] fixture %s: %s %s", f.Name, action, dur.Round(time.Millisecond))
	return value
}

// workloadFixturePrefix opens the Name of every WorkloadFixture, which is
// what makes workload entries identifiable as eviction candidates.
const workloadFixturePrefix = "workload/"

// maxLiveWorkloads bounds how many workload fixtures stay provisioned once a
// suite ends. Every workload is a running pod, and a memo entry lives until
// the run ends, so without a bound a long run holds every workload any suite
// ever touched: past a single node's pod capacity, later managers cannot be
// scheduled at all. The bound is well above any one suite's usage, so
// fixtures a neighbouring suite reuses are still served from the memo.
const maxLiveWorkloads = 25

// evictIdleWorkloads destroys workload fixtures untouched since mark, past
// maxLiveWorkloads. Called at suite boundaries, where mark is the reading
// taken before the suite ran. Destroy runs outside the engine lock.
//
// This applies in dev mode too: WorkloadFixture is AlwaysDestroy, so a
// workload is never a candidate for adoption by the next run and evicting it
// early costs only its own re-provision.
func (r *Runtime) evictIdleWorkloads(mark uint64) {
	evicted := r.engine.evictIdle(mark, maxLiveWorkloads)
	tb := &runTB{r: r}
	for _, en := range evicted {
		start := time.Now()
		if err := en.destroy(Env{Ctx: r.ctx, T: tb, R: r}); err != nil {
			r.Infof("[rtest] fixture %s: evict error: %v", en.name, err)
			continue
		}
		r.Infof("[rtest] fixture %s: evicted %s", en.name, time.Since(start).Round(time.Millisecond))
	}
}

// teardownFixtures destroys live fixtures in LIFO (reverse provision) order.
// Called once at the end of Main. When force is true (IsCI() or
// RTEST_TEARDOWN=1) every fixture is destroyed; otherwise only ones marked
// AlwaysDestroy are, so dev-mode's keep-for-adoption still cleans up
// per-test ephemera (PrivateNamespace, SecondaryManager) that are never
// adopted anyway.
func (r *Runtime) teardownFixtures(force bool) {
	entries := r.engine.snapshot()
	tb := &runTB{r: r}
	for i := len(entries) - 1; i >= 0; i-- {
		en := entries[i]
		if !force && !en.always {
			continue
		}
		if en.destroy == nil {
			continue
		}
		env := Env{Ctx: r.ctx, T: tb, R: r}
		start := time.Now()
		if err := en.destroy(env); err != nil {
			r.Infof("[rtest] fixture %s: destroy error: %v", en.name, err)
			continue
		}
		r.Infof("[rtest] fixture %s: destroyed %s", en.name, time.Since(start).Round(time.Millisecond))
	}
}

// runTB is a minimal testing.TB used only by teardownFixtures, when no
// per-test *testing.T exists. DestroyFn implementations should treat it as
// log-only: Fatal/Error/Skip are reported via Runtime.Infof rather than
// aborting anything.
type runTB struct {
	testing.TB
	r *Runtime
}

func (b *runTB) Helper()                 {}
func (b *runTB) Name() string            { return "teardown" }
func (b *runTB) Cleanup(func())          {}
func (b *runTB) Log(args ...any)         { b.r.Infof("%s", fmt.Sprint(args...)) }
func (b *runTB) Logf(f string, a ...any) { b.r.Infof(f, a...) }
func (b *runTB) Error(args ...any)       { b.r.Infof("error: %s", fmt.Sprint(args...)) }
func (b *runTB) Errorf(f string, a ...any) {
	b.r.Infof("error: "+f, a...)
}

func (b *runTB) Fatal(args ...any) { b.r.Infof("fatal: %s", fmt.Sprint(args...)) }
func (b *runTB) Fatalf(f string, a ...any) {
	b.r.Infof("fatal: "+f, a...)
}
func (b *runTB) Skip(...any)          {}
func (b *runTB) Skipf(string, ...any) {}
