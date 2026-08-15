package queuestate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// allPhases lists every defined Phase, independent of the transitions table,
// so tests can assert full coverage of both transitions and recoveryActions.
var allPhases = []Phase{
	Preparing, QuiescingApp, StartingPump, RedirectingApp, Active,
	AbortingActivation, DrainingApp, QuiescingHandback, HandingBack,
	RestoringApp, CleaningUp, Degraded, Inactive,
}

func TestCanTransitionToActivationPath(t *testing.T) {
	path := []Phase{Preparing, QuiescingApp, StartingPump, RedirectingApp, Active}
	for i := 0; i < len(path)-1; i++ {
		assert.Truef(t, path[i].CanTransitionTo(path[i+1]), "%s -> %s", path[i], path[i+1])
	}
}

func TestCanTransitionToDeactivationPath(t *testing.T) {
	path := []Phase{Active, DrainingApp, QuiescingHandback, HandingBack, RestoringApp, CleaningUp, Inactive}
	for i := 0; i < len(path)-1; i++ {
		assert.Truef(t, path[i].CanTransitionTo(path[i+1]), "%s -> %s", path[i], path[i+1])
	}
}

func TestCanTransitionToAbortPath(t *testing.T) {
	for _, p := range []Phase{Preparing, QuiescingApp, StartingPump, RedirectingApp} {
		assert.Truef(t, p.CanTransitionTo(AbortingActivation), "%s -> AbortingActivation", p)
	}
	assert.True(t, AbortingActivation.CanTransitionTo(Inactive))
	assert.True(t, AbortingActivation.CanTransitionTo(Degraded))
}

func TestCanTransitionToDegradedFromEveryNonTerminalPhase(t *testing.T) {
	for _, p := range allPhases {
		if p == Inactive || p == Degraded {
			continue
		}
		assert.Truef(t, p.CanTransitionTo(Degraded), "%s -> Degraded", p)
	}
}

func TestCanTransitionToRecoveryFromDegraded(t *testing.T) {
	for _, p := range allPhases {
		if p == Inactive || p == Degraded {
			continue
		}
		assert.Truef(t, Degraded.CanTransitionTo(p), "Degraded -> %s", p)
	}
	assert.False(t, Degraded.CanTransitionTo(Inactive), "Degraded must not skip straight to the terminal phase")
	assert.False(t, Degraded.CanTransitionTo(Degraded))
}

func TestCanTransitionToInactiveIsTerminal(t *testing.T) {
	for _, p := range allPhases {
		assert.Falsef(t, Inactive.CanTransitionTo(p), "Inactive -> %s", p)
	}
}

func TestCanTransitionToRejectsIllegalJumps(t *testing.T) {
	cases := []struct {
		from, to Phase
	}{
		{Preparing, Active},
		{Preparing, StartingPump},
		{Active, Preparing},
		{Active, Inactive},
		{AbortingActivation, Active},
		{DrainingApp, HandingBack},
		{Inactive, Preparing},
		{Preparing, Preparing},
	}
	for _, c := range cases {
		assert.Falsef(t, c.from.CanTransitionTo(c.to), "%s -> %s should be illegal", c.from, c.to)
	}
}

func TestCanTransitionToEveryPhaseHasATransitionsEntry(t *testing.T) {
	for _, p := range allPhases {
		_, ok := transitions[p]
		assert.Truef(t, ok, "phase %s missing from the transitions table", p)
	}
}

func TestRecoverEveryDefinedPhase(t *testing.T) {
	want := map[Phase]Action{
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
	for _, p := range allPhases {
		assert.Equalf(t, want[p], Recover(p), "Recover(%s)", p)
	}
}

func TestRecoverUnrecognizedPhaseHoldsDegraded(t *testing.T) {
	assert.Equal(t, HoldDegraded, Recover(Phase("SomeFuturePhase")))
	assert.Equal(t, HoldDegraded, Recover(Phase("")))
}
