package rt

import (
	"context"
	"fmt"
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
}

// memoEntry is the type-erased record the engine keeps per fixture hash.
// destroy closes over the fixture's typed DestroyFn and value at the point
// it was stored.
type memoEntry struct {
	name    string
	hash    string
	value   any
	failed  error
	destroy func(Env) error
}

// engine is the single, mutex-guarded fixture store owned by Runtime. It
// never holds its lock while a Provision/Adopt/Destroy function runs, so a
// ProvisionFn may itself call Get on another fixture (nested fixtures).
type engine struct {
	mu       sync.Mutex
	entries  map[string]*memoEntry
	sequence []*memoEntry // provision order, for LIFO teardown
}

func newEngine() *engine {
	return &engine{entries: map[string]*memoEntry{}}
}

func (e *engine) lookup(hash string) (*memoEntry, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	en, ok := e.entries[hash]
	return en, ok
}

func (e *engine) store(en *memoEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries[en.hash] = en
	e.sequence = append(e.sequence, en)
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
		name:  f.Name,
		hash:  f.Hash,
		value: value,
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

// teardownFixtures destroys every live fixture in LIFO (reverse provision)
// order. Called once at the end of Main, only when a teardown is due
// (IsCI() or RTEST_TEARDOWN=1); otherwise resources are kept for adoption by
// the next run.
func (r *Runtime) teardownFixtures() {
	entries := r.engine.snapshot()
	tb := &runTB{r: r}
	for i := len(entries) - 1; i >= 0; i-- {
		en := entries[i]
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
