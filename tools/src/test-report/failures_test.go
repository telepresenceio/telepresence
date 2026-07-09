package main

import (
	"reflect"
	"regexp"
	"testing"
)

const testPkg = ".../integration_test"

func run(test string) *Line { return &Line{TestID: TestID{Package: testPkg, Test: test}, Action: RUN} }
func pass(test string) *Line {
	return &Line{TestID: TestID{Package: testPkg, Test: test}, Action: PASS}
}
func skip(test string) *Line {
	return &Line{TestID: TestID{Package: testPkg, Test: test}, Action: SKIP}
}
func fail(test string) *Line {
	return &Line{TestID: TestID{Package: testPkg, Test: test}, Action: FAIL}
}
func output(test string) *Line {
	return &Line{TestID: TestID{Package: testPkg, Test: test}, Action: OUTPUT, Output: "x"}
}
func pkgFail() *Line   { return &Line{TestID: TestID{Package: testPkg}, Action: FAIL} }
func buildFail() *Line { return &Line{TestID: TestID{Package: testPkg}, Action: BUILD_FAIL} }

func TestCollector(t *testing.T) {
	tests := []struct {
		name  string
		lines []*Line
		want  FailuresDocument
	}{
		{
			name: "all pass",
			lines: []*Line{
				run("Test_A"), output("Test_A"), pass("Test_A"),
				run("Test_B"), pass("Test_B"),
				pkgFail(), // package summary with no Test never happens on an all-pass run, but must be harmless
			},
			want: FailuresDocument{Complete: true, Failures: []FailureEntry{}},
		},
		{
			name: "skip is not a failure",
			lines: []*Line{
				run("Test_A"), skip("Test_A"),
			},
			want: FailuresDocument{Complete: true, Failures: []FailureEntry{}},
		},
		{
			name: "leaf fail propagates fail actions to ancestors",
			lines: []*Line{
				run("Test_Integration"),
				run("Test_Integration/Test_Namespaces_x"),
				run("Test_Integration/Test_Namespaces_x/Test_TrafficManager"),
				run("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected"),
				run("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts"),
				run("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts"),
				run("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts/no_ingored_volumes"),
				fail("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts/no_ingored_volumes"),
				fail("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts"),
				fail("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts"),
				fail("Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected"),
				fail("Test_Integration/Test_Namespaces_x/Test_TrafficManager"),
				fail("Test_Integration/Test_Namespaces_x"),
				fail("Test_Integration"),
				pkgFail(),
			},
			want: FailuresDocument{
				Complete: true,
				Failures: []FailureEntry{
					{Package: testPkg, Test: "Test_Integration"},
					{Package: testPkg, Test: "Test_Integration/Test_Namespaces_x"},
					{Package: testPkg, Test: "Test_Integration/Test_Namespaces_x/Test_TrafficManager"},
					{Package: testPkg, Test: "Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected"},
					{Package: testPkg, Test: "Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts"},
					{Package: testPkg, Test: "Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts"},
					// Only the entry that is not a prefix-rollup of a deeper
					// failure is annotated with its suite and method, which
					// carry no per-run namespace segment and are therefore
					// comparable across retry attempts.
					{
						Package: testPkg,
						Test:    "Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts/no_ingored_volumes",
						Suite:   "Mounts",
						Method:  "Test_IgnoredMounts",
					},
				},
			},
		},
		{
			name: "build fail marks incomplete",
			lines: []*Line{
				run("Test_A"), pass("Test_A"),
				buildFail(),
			},
			want: FailuresDocument{Complete: false, Failures: []FailureEntry{}},
		},
		{
			name: "run without terminal action marks incomplete",
			lines: []*Line{
				run("Test_A"), pass("Test_A"),
				run("Test_B"), // stream truncated: crash or interrupt
			},
			want: FailuresDocument{Complete: false, Failures: []FailureEntry{}},
		},
		{
			name: "run without terminal action alongside a real failure",
			lines: []*Line{
				run("Test_A"), fail("Test_A"),
				run("Test_B"),
			},
			want: FailuresDocument{
				Complete: false,
				Failures: []FailureEntry{{Package: testPkg, Test: "Test_A"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCollector()
			for _, l := range tc.lines {
				c.Report(l)
			}
			got := c.Document()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Document() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func entries(paths ...string) []FailureEntry {
	out := make([]FailureEntry, len(paths))
	for i, p := range paths {
		out[i] = FailureEntry{Package: testPkg, Test: p}
	}
	return out
}

func TestMapFailuresToScope(t *testing.T) {
	tests := []struct {
		name        string
		doc         FailuresDocument
		wantSuites  []string
		wantMethods []string
		wantOK      bool
	}{
		{
			name: "leaf and all ancestors map to one suite and method",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries(
					"Test_Integration",
					"Test_Integration/Test_Namespaces_x",
					"Test_Integration/Test_Namespaces_x/Test_TrafficManager",
					"Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected",
					"Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts",
					"Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts",
					"Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/Mounts/Test_IgnoredMounts/no_ingored_volumes",
				),
			},
			wantSuites:  []string{"Mounts"},
			wantMethods: []string{"Test_IgnoredMounts"},
			wantOK:      true,
		},
		{
			name: "two leaves in different suites union",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries(
					"Test_Integration/Test_Namespaces_x/Wiretap/Test_B",
					"Test_Integration/Test_Namespaces_y/Mounts/Test_A",
				),
			},
			wantSuites:  []string{"Mounts", "Wiretap"},
			wantMethods: []string{"Test_A", "Test_B"},
			wantOK:      true,
		},
		{
			name: "suite-level failure mixed with method failure suppresses TEST_NAME",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries(
					"Test_Integration/Test_Namespaces_x/Test_TrafficManager/Test_Connected/NodeAgentMulti",
					"Test_Integration/Test_Namespaces_y/Test_TrafficManager/Test_Connected/Mounts/Test_A",
				),
			},
			wantSuites:  []string{"Mounts", "NodeAgentMulti"},
			wantMethods: nil,
			wantOK:      true,
		},
		{
			name: "harness-only failure is unscopeable",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries("Test_Integration/Test_Namespaces_x"),
			},
			wantSuites:  nil,
			wantMethods: nil,
			wantOK:      false,
		},
		{
			name: "just Test_Integration is unscopeable",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries("Test_Integration"),
			},
			wantSuites:  nil,
			wantMethods: nil,
			wantOK:      false,
		},
		{
			name: "incomplete document is unscopeable",
			doc: FailuresDocument{
				Complete: false,
				Failures: entries("Test_Integration/Test_Namespaces_x/Mounts/Test_A"),
			},
			wantSuites:  nil,
			wantMethods: nil,
			wantOK:      false,
		},
		{
			name:        "no failures",
			doc:         FailuresDocument{Complete: true, Failures: nil},
			wantSuites:  nil,
			wantMethods: nil,
			wantOK:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			suites, methods, ok := MapFailuresToScope(tc.doc)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !reflect.DeepEqual(suites, tc.wantSuites) {
				t.Errorf("suites = %v, want %v", suites, tc.wantSuites)
			}
			if !reflect.DeepEqual(methods, tc.wantMethods) {
				t.Errorf("methods = %v, want %v", methods, tc.wantMethods)
			}
		})
	}
}

func TestBuildScopeOutput(t *testing.T) {
	tests := []struct {
		name string
		doc  FailuresDocument
		want []string
	}{
		{
			name: "scopeable single suite and method",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries("Test_Integration/Test_Namespaces_x/Mounts/Test_IgnoredMounts/sub"),
			},
			want: []string{
				"TEST_SUITE=^(Mounts)$",
				"TEST_NAME=^(Test_IgnoredMounts)$",
			},
		},
		{
			name: "suite-only failure suppresses TEST_NAME line entirely",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries("Test_Integration/Test_Namespaces_x/NodeAgentMulti"),
			},
			want: []string{"TEST_SUITE=^(NodeAgentMulti)$"},
		},
		{
			name: "unscopeable harness failure prints nothing",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries("Test_Integration/Test_Namespaces_x"),
			},
			want: nil,
		},
		{
			name: "incomplete document prints nothing",
			doc: FailuresDocument{
				Complete: false,
				Failures: entries("Test_Integration/Test_Namespaces_x/Mounts/Test_A"),
			},
			want: nil,
		},
		{
			name: "names needing QuoteMeta produce a valid, matching regex",
			doc: FailuresDocument{
				Complete: true,
				Failures: entries("Test_Integration/Test_Namespaces_x/Suite(v2)/Test_Foo.Bar"),
			},
			want: []string{
				`TEST_SUITE=^(Suite\(v2\))$`,
				`TEST_NAME=^(Test_Foo\.Bar)$`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildScopeOutput(tc.doc)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("BuildScopeOutput() = %v, want %v", got, tc.want)
			}
			for _, line := range got {
				eq := indexByte(line, '=')
				re := regexp.MustCompile(line[eq+1:])
				if !re.MatchString(literalNameFromRegex(line[eq+1:])) {
					t.Errorf("regex %q does not match its own literal source", line)
				}
			}
		})
	}
}

// indexByte finds the first '=' separating KEY from VALUE.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// literalNameFromRegex strips the ^( )$ anchors and takes the first
// alternative, unescaping QuoteMeta's backslash-escaping, to recover a
// string the regex must match.
func literalNameFromRegex(re string) string {
	inner := re[2 : len(re)-2] // drop "^(" and ")$"
	first := inner
	for i := 0; i < len(inner); i++ {
		if inner[i] == '|' {
			first = inner[:i]
			break
		}
	}
	out := make([]byte, 0, len(first))
	for i := 0; i < len(first); i++ {
		if first[i] == '\\' && i+1 < len(first) {
			i++
		}
		out = append(out, first[i])
	}
	return string(out)
}
