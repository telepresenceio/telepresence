package rt

import (
	"sort"
	"sync"
	"time"
)

// Manifest is the run's machine-readable summary, written to
// ArtifactDir()/manifest.json at the end of Main.
type Manifest struct {
	Run      string          `json:"run"`
	Start    time.Time       `json:"start"`
	End      time.Time       `json:"end"`
	Tests    []TestResult    `json:"tests"`
	Fixtures []FixtureResult `json:"fixtures"`
}

// TestResult is one test's outcome in the manifest.
type TestResult struct {
	Name       string   `json:"name"`
	Outcome    string   `json:"outcome"` // "pass", "fail", or "skip"
	DurationMs int64    `json:"duration_ms"`
	Labels     []string `json:"labels,omitempty"`
	Artifacts  string   `json:"artifacts,omitempty"`
}

// FixtureResult is one fixture action in the manifest.
type FixtureResult struct {
	Name       string `json:"name"`
	Hash       string `json:"hash"`
	Action     string `json:"action"` // "provisioned", "adopted", or "failed"
	DurationMs int64  `json:"duration_ms"`
}

// manifestState accumulates Manifest data over the run under a mutex, since
// fixtures can be provisioned from any test goroutine calling Get/Mutate.
type manifestState struct {
	mu       sync.Mutex
	run      string
	start    time.Time
	tests    []TestResult
	fixtures []FixtureResult
}

func newManifestState(runID string) *manifestState {
	return &manifestState{run: runID, start: time.Now().UTC()}
}

func (m *manifestState) recordFixture(name, hash, action string, dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fixtures = append(m.fixtures, FixtureResult{
		Name:       name,
		Hash:       hash,
		Action:     action,
		DurationMs: dur.Milliseconds(),
	})
}

// recordTest records one test's outcome. labels is sorted for determinism.
func (m *manifestState) recordTest(name, outcome string, dur time.Duration, labels []Label) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ls := make([]string, len(labels))
	for i, l := range labels {
		ls[i] = string(l)
	}
	sort.Strings(ls)
	m.tests = append(m.tests, TestResult{
		Name:       name,
		Outcome:    outcome,
		DurationMs: dur.Milliseconds(),
		Labels:     ls,
	})
}

func (m *manifestState) snapshot() Manifest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Manifest{
		Run:      m.run,
		Start:    m.start,
		End:      time.Now().UTC(),
		Tests:    append([]TestResult{}, m.tests...),
		Fixtures: append([]FixtureResult{}, m.fixtures...),
	}
}
