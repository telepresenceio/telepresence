package session

import (
	"archive/zip"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// GatherLogs proves `telepresence gather-logs`'s --traffic-manager/
// --traffic-agents/--get-pod-yaml combinations shape the resulting zip's
// file list the way gather_logs_test.go's matrix expects, deduped: its
// TestGatherLogs_NoPodYamlUnlessLogs and TestGatherLogs_NoK8sLogs cases are
// byte-identical (same args, same assertions), ported once here as
// no-pod-yaml-unless-logs.
type GatherLogs struct {
	rt.Suite
}

func init() {
	rt.Register(&GatherLogs{}, rt.InArea("session"), rt.NeedsManager(managers.Default))
}

// gatherLogsCase is one cell of the deduped matrix: extraArgs beyond
// --get-pod-yaml (always passed), and whether this suite's own workload's
// manager/agent log+yaml entries should be present in the resulting zip.
// Presence is checked against THIS suite's own workload name only, never a
// total count: the shared app namespace accumulates agented workloads from
// every other area that ran before this one, so "all agent logs" can't be
// asserted as an exact count.
type gatherLogsCase struct {
	name        string
	extraArgs   []string
	wantManager bool
	wantAgent   bool
}

// Test_Matrix installs one intercepted workload so its agent has logs to
// gather, then runs the deduped gather-logs matrix against it, asserting on
// each resulting zip's file list (archive/zip, no unzip dependency).
func (s *GatherLogs) Test_Matrix() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := s.Connect()

	wl := s.Workload(workloads.Echo("gather-logs-wl"))
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	managerLogRx := podLogPattern("traffic-manager", managers.ManagerNamespace, "log")
	managerYamlRx := podLogPattern("traffic-manager", managers.ManagerNamespace, "yaml")
	agentLogRx := podLogPattern(wl.Name, ns, "log")
	agentYamlRx := podLogPattern(wl.Name, ns, "yaml")

	cases := []gatherLogsCase{
		{name: "all-logs", wantManager: true, wantAgent: true},
		{name: "manager-only", extraArgs: []string{"--traffic-agents=None"}, wantManager: true, wantAgent: false},
		{name: "agents-only", extraArgs: []string{"--traffic-manager=false"}, wantManager: false, wantAgent: true},
		{
			name:        "one-agent",
			extraArgs:   []string{"--traffic-manager=false", "--traffic-agents=" + wl.Name},
			wantManager: false,
			wantAgent:   true,
		},
		{
			name:        "no-pod-yaml-unless-logs",
			extraArgs:   []string{"--traffic-manager=false", "--traffic-agents=None"},
			wantManager: false,
			wantAgent:   false,
		},
	}

	outDir := t.TempDir()
	for _, c := range cases {
		s.Run(c.name, func() {
			outFile := filepath.Join(outDir, c.name+".zip")
			args := append([]string{"gather-logs", "--output-file", outFile, "--get-pod-yaml"}, c.extraArgs...)
			_, stderr, err := s.CLI().Run(ctx, args...)
			s.Require().NoError(err, "gather-logs %s: %s", c.name, stderr)

			names := zipNames(s.T(), outFile)
			s.Contains(names, "connector.log", "daemon logs should always be gathered")

			if c.wantManager {
				s.True(hasMatch(names, managerLogRx), "%s: expected a manager log, got %v", c.name, names)
				s.True(hasMatch(names, managerYamlRx), "%s: expected a manager pod yaml, got %v", c.name, names)
			} else {
				s.False(hasMatch(names, managerLogRx), "%s: unexpected manager log in %v", c.name, names)
				s.False(hasMatch(names, managerYamlRx), "%s: unexpected manager pod yaml in %v", c.name, names)
			}
			if c.wantAgent {
				s.True(hasMatch(names, agentLogRx), "%s: expected this suite's agent log, got %v", c.name, names)
				s.True(hasMatch(names, agentYamlRx), "%s: expected this suite's agent pod yaml, got %v", c.name, names)
			} else {
				s.False(hasMatch(names, agentLogRx), "%s: unexpected agent log in %v", c.name, names)
				s.False(hasMatch(names, agentYamlRx), "%s: unexpected agent pod yaml in %v", c.name, names)
			}
		})
	}
}

// podLogPattern matches a gather-logs zip entry for a pod whose name starts
// with namePrefix, running in ns: "<namePrefix>-<hash-suffix>.<ns>.<ext>",
// mirroring gather_logs_test.go's getZipData regexes.
func podLogPattern(namePrefix, ns, ext string) *regexp.Regexp {
	pat := `^` + regexp.QuoteMeta(namePrefix) + `-[0-9a-z-]+\.` + regexp.QuoteMeta(ns) + `\.` + ext + `$`
	return regexp.MustCompile(pat)
}

// hasMatch reports whether any of names matches rx.
func hasMatch(names []string, rx *regexp.Regexp) bool {
	for _, n := range names {
		if rx.MatchString(n) {
			return true
		}
	}
	return false
}

// zipNames returns the file names in the zip at path.
func zipNames(t testing.TB, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening zip %s: %v", path, err)
	}
	defer zr.Close()
	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
	}
	return names
}
