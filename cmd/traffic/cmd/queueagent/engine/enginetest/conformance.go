package enginetest

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

const (
	// pollInterval is the delay between polling attempts inside the
	// suite's own wait loops.
	pollInterval = 250 * time.Millisecond

	// readTimeout bounds one Probe call that reads from a shadow or the
	// source.
	readTimeout = 15 * time.Second

	// suiteDeadline bounds an entire dedup-and-wait loop across several
	// broker reads, generous enough for a real broker under test load.
	suiteDeadline = 60 * time.Second
)

// RunConformance runs the shared lifecycle scenarios against h as named
// subtests. It skips the whole suite, with h.Skip's reason, when no broker
// is reachable.
func RunConformance(t *testing.T, h *Harness) {
	t.Helper()

	if h.Skip != nil {
		if reason, skip := h.Skip(); skip {
			t.Skip(reason)
		}
	}

	runScenario(t, h, "PrepareOnlyRecovery", runPrepareOnlyRecovery)
	runScenario(t, h, "Activation", runActivation)
	runScenario(t, h, "Routing", runRouting)
	runScenario(t, h, "CrashRecovery", runCrashRecovery)
	runScenario(t, h, "RouteDrain", runRouteDrain)
	runScenario(t, h, "AbortRollback", runAbortRollback)
	runScenario(t, h, "StopDrainHandback", runStopDrainHandback)
	runScenario(t, h, "Fencing", runFencing)
}

// runScenario runs one named conformance scenario as a subtest, calling
// h.BeginScenario first so a Probe can reset its own per-scenario state.
func runScenario(t *testing.T, h *Harness, name string, fn func(t *testing.T, h *Harness)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if h.BeginScenario != nil {
			h.BeginScenario()
		}
		fn(t, h)
	})
}

// runPrepareOnlyRecovery seeds 3 source messages, prepares an activation
// without starting it, and restarts. Recover with Started false must ensure
// only Prepare-level resources -- it never consumes the source, so the app
// shadow stays empty and the application still owns the source.
func runPrepareOnlyRecovery(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	msgs := genMessages("prep-only", 3, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, msgs))

	eng1 := h.NewEngine(t)
	overrides, err := eng1.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, id.expectedOverrides(), overrides)
	require.NoError(t, eng1.Close()) // simulated restart

	eng2 := h.NewEngine(t)
	t.Cleanup(func() { _ = eng2.Close() })
	require.NoError(t, eng2.Recover(ctx, engine.RecoveredState{Started: false}))

	got, err := h.Probe.ReadShadow(ctx, id.appShadow(), 1, 8*time.Second)
	require.NoError(t, err)
	require.Empty(t, got, "app shadow must stay empty when the pump never started")

	want := idsOf(msgs)
	source, err := h.Probe.ConsumeSourceAsApp(ctx, len(want)+2, suiteDeadline)
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOf(source))
}

// runActivation seeds 10 source messages, marks the first 4 already
// consumed by the application's pre-split identity, and verifies that
// Prepare returns exactly the queuestate-derived overrides and that Start
// resumes the pump at message 5 -- no replay of 1-4, no skip past 10.
func runActivation(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	msgs := genMessages("act", 10, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, msgs))
	require.NoError(t, h.Probe.SetAppConsumed(ctx, 4))

	eng := h.NewEngine(t)
	t.Cleanup(func() { _ = eng.Close() })

	overrides, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, id.expectedOverrides(), overrides)

	require.NoError(t, eng.Start(ctx))

	want := idsOf(msgs[4:])
	seen := collectShadow(t, ctx, h.Probe, id.appShadow(), want, nil)
	require.ElementsMatch(t, want, keysOf(seen))
}

// runRouting seeds messages matching two disjoint routes and messages
// matching neither, and verifies each message lands only in its own
// destination -- never both a session shadow and the app shadow, never two
// session shadows.
func runRouting(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	eng := h.NewEngine(t)
	t.Cleanup(func() { _ = eng.Close() })
	_, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng.Start(ctx))

	routeA := engine.Route{ID: "route-a", Filter: map[string]string{"user": "alice"}}
	routeB := engine.Route{ID: "route-b", Filter: map[string]string{"user": "bob"}}
	require.NoError(t, eng.ReconcileRoutes(ctx, []engine.Route{routeA, routeB}))

	matchedA := genMessages("route-a-msg", 3, map[string]string{"user": "alice"})
	matchedB := genMessages("route-b-msg", 3, map[string]string{"user": "bob"})
	unmatched := genMessages("unmatched-msg", 3, map[string]string{"user": "carol"})
	require.NoError(t, h.Probe.SeedSource(ctx, concatMessages(matchedA, matchedB, unmatched)))

	wantA, wantB, wantApp := idsOf(matchedA), idsOf(matchedB), idsOf(unmatched)
	seenA := collectShadow(t, ctx, h.Probe, id.sessionShadow(routeA.ID), wantA, nil)
	seenB := collectShadow(t, ctx, h.Probe, id.sessionShadow(routeB.ID), wantB, nil)
	seenApp := collectShadow(t, ctx, h.Probe, id.appShadow(), wantApp, nil)

	require.ElementsMatch(t, wantA, keysOf(seenA))
	require.ElementsMatch(t, wantB, keysOf(seenB))
	require.ElementsMatch(t, wantApp, keysOf(seenApp))
	assertDisjoint(t, map[string]map[string]Message{
		"session shadow " + routeA.ID: seenA,
		"session shadow " + routeB.ID: seenB,
		"app shadow":                  seenApp,
	})
}

// runCrashRecovery seeds messages, lets the pump make partial progress,
// then simulates a crash by closing the engine without Stop. A fresh
// instance recovers with the same routes, more messages are seeded, and
// every seeded ID -- before and after the crash -- must eventually be
// observed in the app shadow. Duplicates are permitted; nothing is lost.
func runCrashRecovery(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	eng1 := h.NewEngine(t)
	_, err := eng1.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng1.Start(ctx))

	routes := []engine.Route{{ID: "route-crash", Filter: map[string]string{"user": "crashy"}}}
	require.NoError(t, eng1.ReconcileRoutes(ctx, routes))

	before := genMessages("crash-before", 5, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, before))

	// Read a little before the simulated crash, proving the pump made
	// partial progress rather than never starting. Its result feeds the
	// accumulator below so the same messages are not required to reappear.
	partial, err := h.Probe.ReadShadow(ctx, id.appShadow(), 1, readTimeout)
	require.NoError(t, err)
	seen := make(map[string]Message, len(partial))
	for _, m := range partial {
		seen[m.ID] = m
	}

	require.NoError(t, eng1.Close()) // simulated crash: no Stop

	eng2 := h.NewEngine(t)
	t.Cleanup(func() { _ = eng2.Close() })
	require.NoError(t, eng2.Recover(ctx, engine.RecoveredState{Started: true, Routes: routes}))

	after := genMessages("crash-after", 5, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, after))

	want := append(idsOf(before), idsOf(after)...)
	seen = collectShadow(t, ctx, h.Probe, id.appShadow(), want, seen)
	require.ElementsMatch(t, want, keysOf(seen))
}

// runRouteDrain seeds messages matching an active route and drains the
// route without ever consuming its session shadow, so the whole seeded
// batch is unconsumed residue. It must arrive in the app shadow, and the
// session shadow must be deleted -- or, for a provider with no loss-proof
// conditional delete, verified empty and left for Cleanup to report
// retained. It then simulates a restart before winding down, so a resource
// DrainRoute retained is proven to survive via RecoveredState.Retained
// alone, not the process memory that produced it.
func runRouteDrain(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	eng := h.NewEngine(t)
	t.Cleanup(func() { _ = eng.Close() })
	_, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng.Start(ctx))

	route := engine.Route{ID: "route-drain", Filter: map[string]string{"user": "draining"}}
	require.NoError(t, eng.ReconcileRoutes(ctx, []engine.Route{route}))

	msgs := genMessages("drain-msg", 4, map[string]string{"user": "draining"})
	require.NoError(t, h.Probe.SeedSource(ctx, msgs))
	retained, err := eng.DrainRoute(ctx, route.ID)
	require.NoError(t, err)

	want := idsOf(msgs)
	seen := collectShadow(t, ctx, h.Probe, id.appShadow(), want, nil)
	require.ElementsMatch(t, want, keysOf(seen))

	shadow := id.sessionShadow(route.ID)
	waitShadowDrainedOrGone(t, ctx, h.Probe, shadow)

	require.NoError(t, eng.Close()) // simulated restart

	// A retained resource lives only in RecoveredState across a restart, not
	// in the new process's memory: the drained route is gone from the
	// desired table, and Retained is what carries the retained inventory
	// forward for the recovered engine's own Cleanup to still report it.
	eng2 := h.NewEngine(t)
	t.Cleanup(func() { _ = eng2.Close() })
	require.NoError(t, eng2.Recover(ctx, engine.RecoveredState{Started: true, Routes: nil, Retained: retained}))

	// Cleanup is only contractually safe to call once every shadow consumer
	// is quiesced, so wind the activation down with Abort first -- there is
	// nothing left to roll back, since the app shadow was already drained
	// above.
	require.NoError(t, eng2.Abort(ctx))
	require.NoError(t, eng2.VerifyCleanupReady(ctx))
	cleanedUp, err := eng2.Cleanup(ctx)
	require.NoError(t, err)
	requireCleanedUp(t, ctx, h, cleanedUp, shadow)
}

// runAbortRollback seeds matched and unmatched messages into a started
// activation with an active filtered route, settles, then simulates a crash
// mid-Abort -- closing the engine with neither Stop nor Abort called -- and
// recovers with Aborting true before retrying Abort. It never reads a
// shadow before the crash: any such read consumes shadow content on a
// provider whose observers consume destructively, which would make the
// number of messages Abort can republish provider-dependent. At-least-once
// semantics guarantee each seeded message is either still on the source or
// in a shadow when the crash hits, so after the retried Abort and Cleanup
// every seeded ID must eventually reappear from the source, and every
// shadow this activation created must be gone or retained verified-empty.
// VerifyCleanupReady must accept once Abort completes.
//
// Recover with Aborting true must reacquire the fence and restore route
// bookkeeping for the retried Abort and the Cleanup that follows, but must
// never resume the pump. The pre-crash pump may already have moved seeded
// messages into shadows, so a shadow read after Recover would prove nothing
// -- and would itself consume residue on a provider whose observers consume
// destructively. The proof is depth-based instead: a marker seeded only
// after Recover, with the app shadow's ShadowDepth never increasing, shows
// the pump stayed stopped without observing (or disturbing) any residue.
func runAbortRollback(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	eng1 := h.NewEngine(t)
	_, err := eng1.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng1.Start(ctx))

	route := engine.Route{ID: "route-abort", Filter: map[string]string{"user": "aborting"}}
	require.NoError(t, eng1.ReconcileRoutes(ctx, []engine.Route{route}))

	matched := genMessages("abort-matched", 2, map[string]string{"user": "aborting"})
	unmatched := genMessages("abort-unmatched", 2, map[string]string{"user": "someone-else"})
	all := concatMessages(matched, unmatched)
	require.NoError(t, h.Probe.SeedSource(ctx, all))

	// Settle without reading a shadow: a fixed number of poll rounds during
	// which the engine reports healthy, giving the pump time to move
	// messages before the simulated crash, without the harness observing
	// where they landed.
	settled := 0
	require.Eventually(t, func() bool {
		if !eng1.Status(ctx).Healthy {
			return false
		}
		settled++
		return settled >= 4
	}, suiteDeadline, pollInterval, "engine must stay healthy through the settle window")

	require.NoError(t, eng1.Close()) // simulated crash: mid-Abort, Abort never ran

	eng2 := h.NewEngine(t)
	t.Cleanup(func() { _ = eng2.Close() })
	recovered := engine.RecoveredState{Started: true, Aborting: true, Routes: []engine.Route{route}}
	require.NoError(t, eng2.Recover(ctx, recovered))

	depth0, err := h.Probe.ShadowDepth(ctx, id.appShadow())
	require.NoError(t, err)

	marker := genMessages("abort-marker", 1, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, marker))

	require.Never(t, func() bool {
		d, derr := h.Probe.ShadowDepth(ctx, id.appShadow())
		return derr == nil && d > depth0
	}, 8*time.Second, pollInterval, "pump must not resume after an Aborting recovery")

	require.NoError(t, eng2.Abort(ctx))
	require.NoError(t, eng2.VerifyCleanupReady(ctx))

	retained, err := eng2.Cleanup(ctx)
	require.NoError(t, err)

	requireCleanedUp(t, ctx, h, retained, id.sessionShadow(route.ID), id.appShadow())

	want := append(idsOf(all), marker[0].ID)
	seen := collectSource(t, ctx, h.Probe, want, nil)
	require.ElementsMatch(t, want, keysOf(seen))
}

// runStopDrainHandback exercises the full handback sequence: Stop records a
// frontier past pre-seeded residue, VerifyCleanupReady must refuse with
// NeedsDrainError while that residue is undrained, source messages seeded
// after the frontier must wait at the broker, a restart with the persisted
// Stopped phase must not resume the pump, AppDrainShadow and
// DrainApplication prove the app shadow empty, VerifyCleanupReady then
// accepts, CommitHandoff and Cleanup complete, every shadow is gone or
// retained verified-empty, and the application resumes the source at
// exactly the post-frontier messages.
func runStopDrainHandback(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	eng := h.NewEngine(t)
	_, err := eng.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng.Start(ctx))

	appShadow := id.appShadow()

	// Residue the pump must have already moved before Stop records the
	// frontier, so VerifyCleanupReady has something real to refuse below.
	pre := genMessages("handback-pre", 3, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, pre))
	require.Eventually(t, func() bool {
		d, derr := h.Probe.ShadowDepth(ctx, appShadow)
		return derr == nil && d >= int64(len(pre))
	}, suiteDeadline, pollInterval, "pre-frontier messages must reach the app shadow before Stop")

	handoff, err := eng.Stop(ctx)
	require.NoError(t, err)
	require.NotNil(t, handoff)

	var drainErr *engine.NeedsDrainError
	err = eng.VerifyCleanupReady(ctx)
	require.Error(t, err)
	require.ErrorAs(t, err, &drainErr)
	require.Equal(t, appShadow, drainErr.Queue)
	t.Logf("VerifyCleanupReady reported Ready=%d before drain", drainErr.Ready)

	post := genMessages("handback-msg", 3, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, post))

	require.NoError(t, eng.Close()) // simulated restart

	eng2 := h.NewEngine(t)
	recovered := engine.RecoveredState{Started: true, Stopped: true, Handoff: handoff}
	require.NoError(t, eng2.Recover(ctx, recovered))

	// Depth-based, not ReadShadow: the app shadow already holds pre's
	// unconsumed residue, so only a strictly increasing depth proves the
	// pump resumed.
	depth0, err := h.Probe.ShadowDepth(ctx, appShadow)
	require.NoError(t, err)
	require.Never(t, func() bool {
		d, derr := h.Probe.ShadowDepth(ctx, appShadow)
		return derr == nil && d > depth0
	}, 8*time.Second, pollInterval, "the pump must not resume after a Stopped recovery")

	_, err = h.Probe.AppDrainShadow(ctx, appShadow)
	require.NoError(t, err)

	require.NoError(t, eng2.DrainApplication(ctx))
	require.NoError(t, eng2.CommitHandoff(ctx, handoff))
	require.NoError(t, eng2.VerifyCleanupReady(ctx))

	retained, err := eng2.Cleanup(ctx)
	require.NoError(t, err)
	require.NoError(t, eng2.Close())

	requireCleanedUp(t, ctx, h, retained, appShadow)

	got, err := h.Probe.ConsumeSourceAsApp(ctx, len(post)+2, suiteDeadline)
	require.NoError(t, err)
	require.ElementsMatch(t, idsOf(post), idsOf(got))
}

// runFencing starts a second engine instance for the same identity while
// the first is still running, and requires the first to report the
// takeover -- as unhealthy status or as a pump-side operation failing --
// while the second is left able to pump.
func runFencing(t *testing.T, h *Harness) {
	t.Helper()
	ctx := t.Context()
	id := h.Identity

	eng1 := h.NewEngine(t)
	_, err := eng1.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng1.Start(ctx))

	eng2 := h.NewEngine(t)
	t.Cleanup(func() { _ = eng2.Close() })
	_, err = eng2.Prepare(ctx)
	require.NoError(t, err)
	require.NoError(t, eng2.Start(ctx))

	require.Eventually(t, func() bool {
		if !eng1.Status(ctx).Healthy {
			return true
		}
		return eng1.ReconcileRoutes(ctx, nil) != nil
	}, suiteDeadline, pollInterval, "engine 1 must report unhealthy or error once fenced out")
	require.NoError(t, eng1.Close())

	msgs := genMessages("fencing-msg", 3, nil)
	require.NoError(t, h.Probe.SeedSource(ctx, msgs))

	want := idsOf(msgs)
	seen := collectShadow(t, ctx, h.Probe, id.appShadow(), want, nil)
	require.ElementsMatch(t, want, keysOf(seen))
}

// genMessages returns n messages with unique IDs "tag-1".."tag-n". Each
// message's Headers is a fresh copy of extra, so scenarios may mutate one
// message's headers without affecting another.
func genMessages(tag string, n int, extra map[string]string) []Message {
	msgs := make([]Message, n)
	for i := range n {
		headers := make(map[string]string, len(extra))
		maps.Copy(headers, extra)
		id := fmt.Sprintf("%s-%d", tag, i+1)
		msgs[i] = Message{ID: id, Headers: headers, Body: []byte(id)}
	}
	return msgs
}

// concatMessages returns the concatenation of groups in order.
func concatMessages(groups ...[]Message) []Message {
	n := 0
	for _, g := range groups {
		n += len(g)
	}
	all := make([]Message, 0, n)
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}

// idsOf returns the IDs of msgs, in order.
func idsOf(msgs []Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

// keysOf returns the keys of seen, in no particular order.
func keysOf(seen map[string]Message) []string {
	keys := make([]string, 0, len(seen))
	for id := range seen {
		keys = append(keys, id)
	}
	return keys
}

// collect polls read, merging newly read messages into seed keyed by
// Message.ID, until every ID in want is present or suiteDeadline elapses. A
// nil seed starts from empty.
func collect(
	t *testing.T, want []string, seed map[string]Message, read func(timeout time.Duration) ([]Message, error),
) map[string]Message {
	t.Helper()

	seen := seed
	if seen == nil {
		seen = make(map[string]Message, len(want))
	}
	missing := func() bool {
		for _, id := range want {
			if _, ok := seen[id]; !ok {
				return true
			}
		}
		return false
	}

	end := time.Now().Add(suiteDeadline)
	for missing() && time.Now().Before(end) {
		left := min(time.Until(end), readTimeout)

		msgs, err := read(left)
		require.NoError(t, err)
		for _, m := range msgs {
			seen[m.ID] = m
		}
		if len(msgs) == 0 {
			time.Sleep(pollInterval)
		}
	}
	return seen
}

// collectShadow polls shadow via probe.ReadShadow until every ID in want is
// present or suiteDeadline elapses. The returned map may hold IDs outside
// want: callers that assert set equality against it catch a message that
// reached the wrong destination.
func collectShadow(
	t *testing.T, ctx context.Context, probe Probe, shadow string, want []string, seed map[string]Message,
) map[string]Message {
	t.Helper()
	return collect(t, want, seed, func(timeout time.Duration) ([]Message, error) {
		return probe.ReadShadow(ctx, shadow, len(want)*2+4, timeout)
	})
}

// collectSource polls the source through probe.ConsumeSourceAsApp until
// every ID in want is present or suiteDeadline elapses.
func collectSource(
	t *testing.T, ctx context.Context, probe Probe, want []string, seed map[string]Message,
) map[string]Message {
	t.Helper()
	return collect(t, want, seed, func(timeout time.Duration) ([]Message, error) {
		return probe.ConsumeSourceAsApp(ctx, len(want)*2, timeout)
	})
}

// assertDisjoint fails the test if any message ID appears in more than one
// of sets, keyed by a label naming where each set was read from.
func assertDisjoint(t *testing.T, sets map[string]map[string]Message) {
	t.Helper()

	owner := make(map[string]string)
	for shadow, msgs := range sets {
		for id := range msgs {
			if prior, ok := owner[id]; ok {
				t.Errorf("message %q observed in both %s and %s", id, prior, shadow)
				continue
			}
			owner[id] = shadow
		}
	}
}

// waitShadowDrainedOrGone polls until shadow is either deleted or reads
// empty (ShadowDepth 0) -- the disjunction a drain leaves a shadow in
// immediately, before any later Cleanup decides whether to delete it or
// retain it verified-empty.
func waitShadowDrainedOrGone(t *testing.T, ctx context.Context, probe Probe, shadow string) {
	t.Helper()
	require.Eventually(t, func() bool {
		exists, err := probe.ShadowExists(ctx, shadow)
		if err != nil {
			return false
		}
		if !exists {
			return true
		}
		depth, err := probe.ShadowDepth(ctx, shadow)
		return err == nil && depth == 0
	}, suiteDeadline, pollInterval, "shadow %q must be deleted or drained empty", shadow)
}

// requireCleanedUp polls, for each of shadows, until Cleanup's outcome is
// provably safe: gone (ShadowExists false), or listed in retained with a
// non-empty Reason while still existing and verified empty (ShadowDepth 0).
// Neither gone nor retained -- or a retained entry that does not exist or
// is not empty -- fails the test. A shadow not listed in retained must
// become strictly gone: unlike waitShadowDrainedOrGone, reading empty is
// not by itself a resting state here, since an unlisted shadow's deletion
// may still be in flight (asynchronous on some providers).
func requireCleanedUp(t *testing.T, ctx context.Context, h *Harness, retained []engine.RetainedResource, shadows ...string) {
	t.Helper()

	byName := make(map[string]engine.RetainedResource, len(retained))
	for _, r := range retained {
		byName[r.Name] = r
	}

	for _, shadow := range shadows {
		require.Eventually(t, func() bool {
			exists, err := h.Probe.ShadowExists(ctx, shadow)
			if err != nil {
				return false
			}
			r, isRetained := byName[shadow]
			if !isRetained {
				return !exists
			}
			if !exists || r.Reason == "" {
				return false
			}
			depth, err := h.Probe.ShadowDepth(ctx, shadow)
			return err == nil && depth == 0
		}, suiteDeadline, pollInterval, "shadow %q must be deleted, or retained and verified empty", shadow)
	}
}
