package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
)

// FailureEntry identifies a single failing test within a package. Suite and
// Method carry the harness-level identity of the failure and are only set on
// entries that are not mere rollups of a deeper failure: unlike Test, whose
// path embeds a random per-run namespace segment, they are stable across
// runs and therefore comparable between retry attempts. Method is empty for
// a suite-level (setup) failure.
type FailureEntry struct {
	Package string `json:"package"`
	Test    string `json:"test"`
	Suite   string `json:"suite,omitempty"`
	Method  string `json:"method,omitempty"`
}

// FailuresDocument is the JSON document written to TEST_FAILURES_OUT and read
// back by -scope. Complete is false when the go test stream was truncated
// (crash, interrupt) or a package failed to build, in which case Failures is
// not trustworthy as the full failure set and callers should re-run unscoped.
type FailuresDocument struct {
	Complete bool           `json:"complete"`
	Failures []FailureEntry `json:"failures"`
}

// Collector derives a FailuresDocument from a stream of Lines. It tracks
// tests that received a RUN action separately from those that failed, so
// that a truncated stream (a test started but never reached a terminal
// action) can be distinguished from an actual test failure.
type Collector struct {
	failures map[FailureEntry]struct{}
	running  map[TestID]struct{}
	complete bool
}

func NewCollector() *Collector {
	return &Collector{
		failures: make(map[FailureEntry]struct{}),
		running:  make(map[TestID]struct{}),
		complete: true,
	}
}

// Report feeds one Line from the go test -json stream into the collector.
func (c *Collector) Report(line *Line) {
	switch line.Action {
	case RUN:
		c.running[line.TestID] = struct{}{}
	case PASS, SKIP:
		delete(c.running, line.TestID)
	case FAIL:
		delete(c.running, line.TestID)
		// A package-level FAIL summary carries no Test name; it is not a
		// test failure by itself, only a rollup of the ones already seen.
		if line.Test != "" {
			c.failures[FailureEntry{Package: line.Package, Test: line.Test}] = struct{}{}
		}
	case BUILD_FAIL:
		c.complete = false
	}
}

// Document returns the current state as a FailuresDocument, sorted
// deterministically by package then test. Complete is false when a
// BUILD_FAIL was seen, or when a test that started (RUN) never reached a
// terminal action. Entries that are not prefix-rollups of a deeper failure
// are annotated with their mapped Suite and Method.
func (c *Collector) Document() FailuresDocument {
	doc := FailuresDocument{Complete: c.complete && len(c.running) == 0}
	doc.Failures = make([]FailureEntry, 0, len(c.failures))
	byPackage := make(map[string][]string)
	for f := range c.failures {
		doc.Failures = append(doc.Failures, f)
		byPackage[f.Package] = append(byPackage[f.Package], f.Test)
	}
	pruned := make(map[FailureEntry]struct{})
	for pkg, paths := range byPackage {
		for _, p := range prunePrefixPaths(paths) {
			pruned[FailureEntry{Package: pkg, Test: p}] = struct{}{}
		}
	}
	for i := range doc.Failures {
		f := &doc.Failures[i]
		if _, ok := pruned[*f]; !ok {
			continue
		}
		if suite, method, ok := mapPathToSuiteMethod(f.Test); ok {
			f.Suite = suite
			f.Method = method
		}
	}
	sort.Slice(doc.Failures, func(i, j int) bool {
		if doc.Failures[i].Package != doc.Failures[j].Package {
			return doc.Failures[i].Package < doc.Failures[j].Package
		}
		return doc.Failures[i].Test < doc.Failures[j].Test
	})
	return doc
}

// WriteFailuresFile writes doc as indented JSON to path.
func WriteFailuresFile(path string, doc FailuresDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// testPrefix is the prefix that distinguishes a registered test method
// (always Test_-prefixed) from a suite name or subtest name (never
// Test_-prefixed in the harness's naming scheme).
const testPrefix = "Test_"

// prunePrefixPaths drops any path that is a strict prefix, on '/' segment
// boundaries, of another path in the same slice. Ancestor test names fail
// because a descendant failed and carry no extra scoping information.
func prunePrefixPaths(paths []string) []string {
	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		isPrefix := false
		for _, q := range paths {
			if p != q && strings.HasPrefix(q, p+"/") {
				isPrefix = true
				break
			}
		}
		if !isPrefix {
			kept = append(kept, p)
		}
	}
	return kept
}

// mapPathToSuiteMethod maps a single (pruned) failing test path to a suite
// and, optionally, a test method, per the harness's naming scheme: suite
// names never start with Test_, methods always do.
//
// It scans from the end of the path for the last adjacent pair (a, b) where
// a does not start with Test_ and b does; a is the suite, b the method, and
// any segments after b (subtests) are ignored. If no such pair exists but
// the last segment does not start with Test_, that segment is a suite-only
// failure (e.g. a SetupSuite failure) with no method. Otherwise the path
// carries no suite/method information and is unscopeable.
func mapPathToSuiteMethod(path string) (suite, method string, ok bool) {
	segs := strings.Split(path, "/")
	for i := len(segs) - 2; i >= 0; i-- {
		a, b := segs[i], segs[i+1]
		if !strings.HasPrefix(a, testPrefix) && strings.HasPrefix(b, testPrefix) {
			return a, b, true
		}
	}
	last := segs[len(segs)-1]
	if !strings.HasPrefix(last, testPrefix) {
		return last, "", true
	}
	return "", "", false
}

// MapFailuresToScope reduces a FailuresDocument to the set of suites and
// methods that should be re-run. ok is false when the document is
// incomplete or contains a failure that carries no suite/method information
// (an unscopeable, harness-level failure) — callers must then re-run
// unscoped. methods is only meaningful (and only returned) when every
// scoped failure mapped to a method; a mix of suite-only and method
// failures returns methods == nil so the suite re-runs in full.
func MapFailuresToScope(doc FailuresDocument) (suites, methods []string, ok bool) {
	if !doc.Complete {
		return nil, nil, false
	}
	if len(doc.Failures) == 0 {
		return nil, nil, true
	}

	byPackage := make(map[string][]string)
	for _, f := range doc.Failures {
		byPackage[f.Package] = append(byPackage[f.Package], f.Test)
	}

	suiteSet := make(map[string]struct{})
	methodSet := make(map[string]struct{})
	allMethodsMapped := true
	for _, paths := range byPackage {
		for _, p := range prunePrefixPaths(paths) {
			suite, method, mapped := mapPathToSuiteMethod(p)
			if !mapped {
				return nil, nil, false
			}
			suiteSet[suite] = struct{}{}
			if method == "" {
				allMethodsMapped = false
			} else {
				methodSet[method] = struct{}{}
			}
		}
	}

	suites = sortedKeys(suiteSet)
	if allMethodsMapped {
		methods = sortedKeys(methodSet)
	}
	return suites, methods, true
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// BuildScopeOutput renders the KEY=VALUE lines for -scope mode. It returns
// no lines when the failure set is unscopeable, incomplete, or empty — the
// caller is expected to re-run unscoped in that case.
func BuildScopeOutput(doc FailuresDocument) []string {
	suites, methods, ok := MapFailuresToScope(doc)
	if !ok || len(suites) == 0 {
		return nil
	}
	lines := []string{"TEST_SUITE=" + anchoredAlternation(suites)}
	if len(methods) > 0 {
		lines = append(lines, "TEST_NAME="+anchoredAlternation(methods))
	}
	return lines
}

// anchoredAlternation builds a `^(a|b|c)$` regexp from names, escaping each
// name with regexp.QuoteMeta.
func anchoredAlternation(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}
