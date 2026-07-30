package rt

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// TestingSuite is testify's suite interface. Concrete suites embed rt.Suite
// (which embeds suite.Suite), so they satisfy it automatically.
type TestingSuite = suite.TestingSuite

// Label selects subsets of tests independently of area, via RTEST_LABELS /
// RTEST_SKIP_LABELS (ANY-match).
type Label string

// M1 labels.
const (
	CompatCore Label = "compat-core"
	Slow       Label = "slow"
	Stress     Label = "stress"
	FlakyRetry Label = "flaky-retry"
)

// registration is what Register records for one suite.
type registration struct {
	suite       TestingSuite
	name        string
	area        string
	managerSpec managers.Spec
	hasSpec     bool
	labels      map[Label]bool
	goos        goosConstraint
	caps        []Capability
}

//nolint:gochecknoglobals // process-wide suite registry, populated from suite package init()s
var registry []*registration

// RegOption configures a suite registration; pass to Register.
type RegOption func(*registration)

// InArea assigns the suite to area; RunArea(t, area) selects it.
func InArea(area string) RegOption {
	return func(r *registration) { r.area = area }
}

// NeedsManager declares the suite's primary manager spec. RunArea sorts
// suites by (spec hash, name) to minimize helm upgrades across a run.
func NeedsManager(spec managers.Spec) RegOption {
	return func(r *registration) {
		r.managerSpec = spec
		r.hasSpec = true
	}
}

// WithLabels attaches labels used by RTEST_LABELS / RTEST_SKIP_LABELS
// filtering.
func WithLabels(labels ...Label) RegOption {
	return func(r *registration) {
		for _, l := range labels {
			r.labels[l] = true
		}
	}
}

// On restricts the suite to the given GOOS values; on any other platform it
// self-skips.
func On(goos ...string) RegOption {
	return func(r *registration) { r.goos.allow = append(r.goos.allow, goos...) }
}

// NotOn excludes the suite from the given GOOS values.
func NotOn(goos ...string) RegOption {
	return func(r *registration) { r.goos.deny = append(r.goos.deny, goos...) }
}

// Requires declares host capabilities the suite needs; unmet capabilities
// cause a self-skip with the missing capability named in the skip message.
func Requires(caps ...Capability) RegOption {
	return func(r *registration) { r.caps = append(r.caps, caps...) }
}

// Register adds a suite to the process-wide registry. Call from an init()
// function in the suite's package.
func Register(s TestingSuite, opts ...RegOption) {
	r := &registration{suite: s, name: suiteName(s), labels: map[Label]bool{}}
	for _, opt := range opts {
		opt(r)
	}
	if rs, ok := s.(interface{ setRegistration(*registration) }); ok {
		rs.setRegistration(r)
	}
	registry = append(registry, r)
}

// suiteName is the bare struct type name of s, via reflection (no package
// path, no stripping of a trailing "Suite").
func suiteName(s TestingSuite) string {
	t := reflect.TypeOf(s)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

// specHash returns the registration's declared manager spec hash, or "" if
// it declared none (grouped first by RunArea's sort).
func (r *registration) specHash() string {
	if !r.hasSpec {
		return ""
	}
	return r.managerSpec.Hash()
}

// labelSlice returns the registration's labels as a sorted slice.
func (r *registration) labelSlice() []Label {
	out := make([]Label, 0, len(r.labels))
	for l := range r.labels {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// constraintUnmet returns a human-readable reason and true when the
// registration's platform or capability constraints are not satisfied.
func (r *registration) constraintUnmet() (string, bool) {
	if reason, ok := r.goos.unmet(); ok {
		return reason, true
	}
	for _, c := range r.caps {
		if !hasCapability(c) {
			return fmt.Sprintf("requires capability %q", c), true
		}
	}
	return "", false
}

func regsForArea(area string) []*registration {
	var out []*registration
	for _, r := range registry {
		if r.area == area {
			out = append(out, r)
		}
	}
	return out
}

// filterByLabels applies RTEST_LABELS (keep only suites with at least one
// matching label, if set) and RTEST_SKIP_LABELS (drop suites with at least
// one matching label).
func filterByLabels(r *Runtime, regs []*registration) []*registration {
	var out []*registration
	for _, reg := range regs {
		if len(r.labels) > 0 && !anyLabelMatch(reg.labels, r.labels) {
			continue
		}
		if anyLabelMatch(reg.labels, r.skipLabels) {
			continue
		}
		out = append(out, reg)
	}
	return out
}

func anyLabelMatch(have, want map[Label]bool) bool {
	for l := range want {
		if have[l] {
			return true
		}
	}
	return false
}

// RunArea runs every registered suite in area: filters by label env vars,
// sorts deterministically by (manager spec hash, suite name) to group
// identical manager specs, then for each suite checks its platform/
// capability constraints (self-skipping with the unmet one named) before
// handing it to testify's suite.Run.
func RunArea(t *testing.T, area string) {
	r := R()
	regs := filterByLabels(r, regsForArea(area))
	sort.SliceStable(regs, func(i, j int) bool {
		hi, hj := regs[i].specHash(), regs[j].specHash()
		if hi != hj {
			return hi < hj
		}
		return regs[i].name < regs[j].name
	})
	for _, reg := range regs {
		t.Run(reg.name, func(t *testing.T) {
			if reason, ok := reg.constraintUnmet(); ok {
				t.Skipf("skipping: %s", reason)
				return
			}
			r.Infof("[rtest] suite %s: start", reg.name)
			suite.Run(t, reg.suite)
		})
	}
}

// Main is the framework's TestMain entry point: it builds the process-global
// Runtime, runs the tests, tears down fixtures when due, and writes the run
// manifest.
func Main(m *testing.M) {
	r, err := newRuntime(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "rtest:", err)
		os.Exit(1)
	}
	globalRuntime = r

	code := m.Run()

	if r.teardown {
		r.teardownFixtures()
	} else {
		r.Infof("[rtest] keeping resources for adoption (dev mode); run `make rtest-clean` to remove them")
	}
	if err := r.writeManifest(); err != nil {
		r.Infof("[rtest] writing manifest: %v", err)
	}
	r.printSummary()
	os.Exit(code)
}
