package queuestate

import "slices"

// Phase is a logical queue's position in the activation/deactivation state
// machine. Every step a phase names is idempotent: a manager that restarts
// while a queue is persisted in a phase resumes that phase's step rather than
// treating partial progress as an error. See Recover.
type Phase string

const (
	// Preparing holds while durable shadows and provider checkpoints are
	// created for a queue joining the active set. The source is not yet
	// consumed and no application env override is active for this queue.
	Preparing Phase = "Preparing"

	// QuiescingApp holds while the workload is scaled to zero and the
	// manager waits for its Pods and their broker consumers to disappear and
	// for final acknowledgements or commits to settle. The pre-activation
	// replica count has been recorded before this phase is entered.
	QuiescingApp Phase = "QuiescingApp"

	// StartingPump holds while this queue's engine starts consuming the
	// source at the application's final position -- Kafka from the original
	// group's committed offsets, RabbitMQ from the source queue -- and
	// copying messages to shadows. The workload remains at zero replicas.
	StartingPump Phase = "StartingPump"

	// RedirectingApp holds while the active override generation is
	// persisted, replicas are restored, and the manager waits for every
	// ready Pod to carry that generation and consume only the app shadow.
	RedirectingApp Phase = "RedirectingApp"

	// Active holds while the queue accepts filtered developer routes. The
	// engine owns the source, the application consumes only the app shadow,
	// and every route not draining receives messages matching its filter.
	Active Phase = "Active"

	// AbortingActivation holds while a failed activation is unwound: Kafka
	// leaves the original app group at its pre-activation offsets, RabbitMQ
	// republishes every unconsumed shadow message to the source, and the
	// workload stays scaled to zero until every provider has proved that
	// replay is safe.
	AbortingActivation Phase = "AbortingActivation"

	// DrainingApp holds while source consumption has stopped at a recorded
	// frontier and the application drains its app shadow toward
	// broker-reported zero lag. New source messages wait at the broker.
	DrainingApp Phase = "DrainingApp"

	// QuiescingHandback holds while the workload is scaled to zero, the
	// manager waits for its consumers to disappear, and it rechecks for
	// deliveries requeued during shutdown, repeating the drain until none
	// remain.
	QuiescingHandback Phase = "QuiescingHandback"

	// HandingBack holds while the provider-specific offset handback runs:
	// Kafka sets the original app group's source offsets to the splitter
	// frontier while the group has no members; RabbitMQ needs no offset
	// operation.
	HandingBack Phase = "HandingBack"

	// RestoringApp holds while an inactive override generation is
	// persisted, replicas are restored, and the manager verifies that every
	// ready Pod no longer carries a queue override.
	RestoringApp Phase = "RestoringApp"

	// CleaningUp holds while empty shadows and provider groups are deleted,
	// the ownership Lease is released, and the idle queue-agent is reaped
	// after its TTL.
	CleaningUp Phase = "CleaningUp"

	// Degraded holds while progress is blocked on a condition this package's
	// state machine cannot resolve on its own: a proof the current step
	// needs is unavailable, or a precondition it depends on -- such as
	// exclusive source ownership -- has been violated. Leaving Degraded
	// requires proved provider state or an explicit operator action; there
	// is no automatic timeout back to normal progress. A queue in Degraded
	// carries its ResumePhase and BlockedReason.
	Degraded Phase = "Degraded"

	// Inactive is the terminal phase: the queue holds no source ownership,
	// no active override, and no shadow this package's naming and cleanup
	// contract still protects.
	Inactive Phase = "Inactive"
)

// transitions is the phase state machine's adjacency list: transitions[p]
// lists every phase that CanTransitionTo(p, next) permits. It is the single
// source of truth for legal phase changes.
//
//nolint:gochecknoglobals // immutable transition table, not mutable state
var transitions = map[Phase][]Phase{
	Preparing:      {QuiescingApp, AbortingActivation, Degraded},
	QuiescingApp:   {StartingPump, AbortingActivation, Degraded},
	StartingPump:   {RedirectingApp, AbortingActivation, Degraded},
	RedirectingApp: {Active, AbortingActivation, Degraded},
	Active:         {DrainingApp, Degraded},

	AbortingActivation: {Inactive, Degraded},

	DrainingApp:       {QuiescingHandback, Degraded},
	QuiescingHandback: {HandingBack, Degraded},
	HandingBack:       {RestoringApp, Degraded},
	RestoringApp:      {CleaningUp, Degraded},
	CleaningUp:        {Inactive, Degraded},

	// Degraded is reachable from, and recovers back into, every non-terminal
	// phase: whichever phase's precondition or proof was unavailable is the
	// phase recovery resumes, recorded as the queue's ResumePhase.
	Degraded: {
		Preparing, QuiescingApp, StartingPump, RedirectingApp, Active,
		AbortingActivation, DrainingApp, QuiescingHandback, HandingBack,
		RestoringApp, CleaningUp,
	},

	Inactive: nil,
}

// CanTransitionTo reports whether next is a legal direct transition from p.
// Persisting the same phase again -- resuming an idempotent step without
// changing Phase -- is not a transition and is not governed by this method.
func (p Phase) CanTransitionTo(next Phase) bool {
	return slices.Contains(transitions[p], next)
}

// definedPhase reports whether p is one of the phases this package defines.
// transitions has an entry for every defined phase, including the terminal
// Inactive phase, whose entry is nil.
func definedPhase(p Phase) bool {
	_, ok := transitions[p]
	return ok
}

// RouteState is a Route's position in its own drain lifecycle, independent
// of the owning queue's Phase.
type RouteState string

const (
	// RouteActive routes messages matching its filter to the route's
	// session shadow.
	RouteActive RouteState = "Active"

	// RouteDraining accepts no new messages. The engine has stopped
	// classifying messages to this route and is moving, or has moved, its
	// shadow's unconsumed suffix to the app shadow.
	RouteDraining RouteState = "Draining"
)

// Action says what a manager that finds a queue persisted in a given phase
// must do about it on restart, before any new desired-state change is
// accepted for that queue.
type Action int

const (
	// ResumeStep re-runs the persisted phase's own idempotent step exactly
	// as if the phase had just been entered.
	ResumeStep Action = iota

	// VerifyActive re-establishes confidence in a steady-state Active queue:
	// check that the persisted overrides are in effect and the engine
	// reports healthy. It repeats no scaling or cutover step.
	VerifyActive

	// HoldDegraded reports the degraded condition and takes no independent
	// recovery step; see Degraded.
	HoldDegraded

	// NoAction is the recovery for the terminal phase: there is nothing to
	// resume.
	NoAction
)

// recoveryActions maps every defined Phase to the Action a restarted manager
// must take for a queue found persisted in it.
//
//nolint:gochecknoglobals // immutable recovery table, not mutable state
var recoveryActions = map[Phase]Action{
	Preparing:          ResumeStep,
	QuiescingApp:       ResumeStep,
	StartingPump:       ResumeStep,
	RedirectingApp:     ResumeStep,
	Active:             VerifyActive,
	AbortingActivation: ResumeStep,
	DrainingApp:        ResumeStep,
	QuiescingHandback:  ResumeStep,
	HandingBack:        ResumeStep,
	RestoringApp:       ResumeStep,
	CleaningUp:         ResumeStep,
	Degraded:           HoldDegraded,
	Inactive:           NoAction,
}

// Recover returns the Action a restarted manager must take for a queue found
// persisted in phase p. A phase outside this package's defined set -- data a
// newer version wrote, or corruption -- recovers as HoldDegraded: resuming an
// unrecognized step is unsafe, so it is treated the same as Degraded.
func Recover(p Phase) Action {
	if a, ok := recoveryActions[p]; ok {
		return a
	}
	return HoldDegraded
}
