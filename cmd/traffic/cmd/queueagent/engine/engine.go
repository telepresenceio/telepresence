// Package engine defines the provider engine contract for queue splitting.
// One Engine instance owns one logical queue for one activation: it pumps a
// broker source to durable per-route shadows so the application and each
// developer's local consumer can be pointed at their own shadow instead of
// the source.
//
// Delivery contract, shared by every provider: every message accepted from
// the source is durably published to exactly one shadow before its source
// position is acknowledged or committed. A crash between that publish and
// the source acknowledgement/commit may duplicate the message; it never
// loses one. Routing is deterministic per message -- the same message always
// classifies to the same destination -- so a duplicate lands in the same
// shadow both times, never in two different ones.
//
// Identity (install ID, workload UID, logical queue name, activation ID) is
// construction-time state of an implementation. It is not part of this
// interface: two Engine values built for the same identity and activation,
// even across a process restart, must behave as the same logical owner.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// Route is one developer's filter attachment to the logical queue.
type Route struct {
	// ID is the route's stable identifier. It is one of the inputs to the
	// route's session shadow name and addresses it in DrainRoute.
	ID string

	// Filter is the route's equality-conjunction predicate: a message
	// matches when every key here maps to the same value in the message. An
	// empty filter matches every message.
	Filter map[string]string
}

// FilterMatches reports whether headers satisfy filter: every filter key
// must map to the same value in headers. An empty filter matches all.
func FilterMatches(filter, headers map[string]string) bool {
	for k, v := range filter {
		if headers[k] != v {
			return false
		}
	}
	return true
}

// EnvOverrides are the application environment variable overrides an
// engine's Prepare contributes, keyed by variable name.
type EnvOverrides map[string]string

// Handoff is an engine-opaque checkpoint produced by Stop and consumed by
// CommitHandoff. Its encoding is provider-specific; the manager never
// interprets it.
type Handoff []byte

// Lifecycle tracks an activation's phase. Providers embed it, mark
// transitions on success, and Restore it from persisted state.
type Lifecycle struct {
	started, stopped, aborted atomic.Bool
}

func (l *Lifecycle) MarkStarted()  { l.started.Store(true) }
func (l *Lifecycle) MarkStopped()  { l.stopped.Store(true) }
func (l *Lifecycle) MarkAborted()  { l.aborted.Store(true) }
func (l *Lifecycle) Started() bool { return l.started.Load() }
func (l *Lifecycle) Stopped() bool { return l.stopped.Load() }
func (l *Lifecycle) Aborted() bool { return l.aborted.Load() }

// Restore rebuilds the phase from st. Aborting recovery leaves the aborted
// mark unset; the retried Abort sets it.
func (l *Lifecycle) Restore(st RecoveredState) {
	if st.Started {
		l.started.Store(true)
	}
	if st.Stopped {
		l.stopped.Store(true)
	}
}

// CleanupGate returns an error in the one state Cleanup must refuse:
// started but neither stopped nor aborted.
func (l *Lifecycle) CleanupGate() error {
	if l.started.Load() && !l.stopped.Load() && !l.aborted.Load() {
		return errors.New("activation is active; Stop or Abort first")
	}
	return nil
}

// RecoveredState is the persisted desired state a restarted process hands to
// Recover. The engine derives nothing from broker-resource existence; the
// persisted phase decides what may run.
type RecoveredState struct {
	// Started is true once the activation's pump had started.
	Started bool

	// Stopped is true once Stop already recorded the frontier.
	Stopped bool

	// Aborting is true when the activation was being aborted when the
	// process exited: Recover reacquires the fence and restores route
	// bookkeeping but never resumes consumption; the caller retries Abort.
	// Valid only with Started true and Stopped false.
	Aborting bool

	// Routes is the last-reconciled route table.
	Routes []Route

	// Handoff is the checkpoint Stop returned. It is non-nil only when
	// Stopped is true.
	Handoff Handoff

	// Retained is every resource earlier DrainRoute or Cleanup calls
	// reported retained; Recover restores it so verification and Cleanup
	// keep accounting for those resources.
	Retained []RetainedResource
}

// RetainedResource is a broker resource Cleanup verified empty but kept,
// because its provider has no deletion primitive that cannot destroy a
// concurrently arriving delivery. The caller holds it in inventory as
// cleanup-pending until a deliberate operator action reaps it.
type RetainedResource struct {
	// Name is the provider-side name of the retained resource.
	Name string

	// Reason states, for an operator, why the resource was retained.
	Reason string
}

// NeedsDrainError is VerifyCleanupReady's refusal for message residue: a
// shadow still holds messages that deleting it would destroy, so the
// caller must loop back to draining.
type NeedsDrainError struct {
	// Queue is the shadow holding the residue.
	Queue string

	// Ready is the live ready-message count observed on Queue.
	Ready int
}

func (e *NeedsDrainError) Error() string {
	return fmt.Sprintf("shadow %q needs draining: %d ready message(s)", e.Queue, e.Ready)
}

// Status reports an engine's current health.
type Status struct {
	// Healthy is false once the engine knows it can no longer make
	// progress: a lost fence, an exhausted retry budget, or an equivalent
	// unrecoverable condition.
	Healthy bool

	// Detail is a short, human-readable explanation. It is most useful when
	// Healthy is false.
	Detail string
}

// Engine owns one logical queue for one activation. One instance is used for
// the life of one process; a restart constructs a fresh instance for the
// same identity and calls Recover.
//
// Every method is idempotent for the activation the instance was constructed
// for, so a caller may retry any method after a timeout or a restart.
type Engine interface {
	// Prepare creates durable shadows and provider checkpoints for this
	// activation without consuming the source, and returns the application
	// environment overrides. It does not start the pump; call Start for
	// that.
	Prepare(ctx context.Context) (EnvOverrides, error)

	// Start acquires the provider fence for this activation, then begins
	// pumping the source at the application's final position. Once a newer
	// instance has taken the fence, this instance must report the failure
	// from Start or from any later operation.
	Start(ctx context.Context) error

	// ReconcileRoutes installs the desired route table, creating a session
	// shadow for each route not previously seen. It never deletes a shadow;
	// removing one is DrainRoute's job. Idempotent per desired table.
	ReconcileRoutes(ctx context.Context, routes []Route) error

	// DrainRoute stops classifying messages to route id, waits for its
	// in-flight publishes to be acknowledged, moves the route's unconsumed
	// residue to the app shadow, and deletes its session resources only
	// after proof that they are empty. Resources with no loss-proof delete
	// are returned as retained; the caller persists them for Recover.
	DrainRoute(ctx context.Context, id string) ([]RetainedResource, error)

	// Stop stops source consumption at a recorded frontier and returns an
	// engine-opaque checkpoint for the handoff sequence that follows.
	Stop(ctx context.Context) (Handoff, error)

	// Abort unwinds a started activation without a handback: it stops
	// source consumption and returns every message already in this
	// activation's shadows to the application's ownership. Replay may
	// duplicate; it never loses. Cleanup afterward removes the shadows.
	Abort(ctx context.Context) error

	// DrainApplication returns nil only once the app shadow is proven
	// drained. It blocks, bounded by ctx, until that proof is available.
	DrainApplication(ctx context.Context) error

	// CommitHandoff performs the provider-specific handback of source
	// ownership to the application, using the checkpoint Stop returned.
	CommitHandoff(ctx context.Context, h Handoff) error

	// VerifyCleanupReady reports whether every resource this activation
	// created can be deleted without destroying a message. Called only
	// after all shadow consumers are quiesced (connections gone, unacked
	// requeued); only live broker reads decide, never statistics. Residue
	// is a *NeedsDrainError; a still-attached consumer is a plain error.
	VerifyCleanupReady(ctx context.Context) error

	// Cleanup deletes every broker resource this activation created and
	// nothing else, repeating VerifyCleanupReady's live checks before each
	// delete. It refuses while the activation is started but neither
	// stopped nor aborted; a prepared-but-never-started activation may be
	// cleaned up. A resource with no loss-proof delete is verified empty
	// and returned as retained instead; on success everything is either
	// gone or in that list. Ambiguity leaves resources in place for retry.
	Cleanup(ctx context.Context) ([]RetainedResource, error)

	// Recover reconciles broker state after a process restart, resuming
	// exactly what st's flags permit: not Started ensures Prepare-level
	// resources only, Started resumes the pump with st.Routes, Aborting
	// restores bookkeeping without pumping, Stopped serves the handoff
	// sequence with st.Handoff.
	Recover(ctx context.Context, st RecoveredState) error

	// Status reports current health.
	Status(ctx context.Context) Status

	// Close releases held clients and connections. It deletes nothing.
	Close() error
}
