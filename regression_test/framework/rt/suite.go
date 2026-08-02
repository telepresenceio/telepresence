package rt

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Suite is the base type concrete suites embed. Its accessors are lazy:
// each calls Get on first use, so a -run-filtered suite that never touches
// an accessor costs nothing. SetupSuite in suites must not provision
// resources directly; provisioning belongs in the accessors, called from
// test methods.
type Suite struct {
	suite.Suite

	reg *registration

	testStart time.Time
}

// setRegistration associates the suite with the registration Register
// created for it. Called by Register via the embedded field's promoted
// method; suites never call it directly.
func (s *Suite) setRegistration(r *registration) { s.reg = r }

// R returns the process-global Runtime.
func (s *Suite) R() *Runtime { return R() }

// testCtxKey tags the run context with the current test's name.
type testCtxKey struct{}

// Ctx returns the run context, tagged with the current test's name.
func (s *Suite) Ctx() context.Context {
	return context.WithValue(R().ctx, testCtxKey{}, s.T().Name())
}

// Manager returns the handle for the suite's declared manager spec
// (NeedsManager at Register time), provisioning or adopting it on first use.
func (s *Suite) Manager() *ManagerHandle {
	spec := managers.Default
	if s.reg != nil && s.reg.hasSpec {
		spec = s.reg.managerSpec
	}
	return Get(s.T(), ManagerFixture(spec))
}

// AppNamespace returns the shared application namespace, creating it on
// first use.
func (s *Suite) AppNamespace() string {
	return Get(s.T(), appNamespaceFixture())
}

// Connect returns a live connection to the suite's manager, in the app
// namespace, provisioning both on first use.
func (s *Suite) Connect(opts ...ConnOpt) *Conn {
	s.Manager()
	ns := s.AppNamespace()
	return Get(s.T(), ConnectionFixture(ns, opts...))
}

// Workload returns the rendered, applied workload described by tpl in the
// app namespace, creating it on first use.
func (s *Suite) Workload(tpl workloads.Template) *Workload {
	ns := s.AppNamespace()
	return Get(s.T(), WorkloadFixture(ns, tpl))
}

// LocalEcho starts an in-process echo server on 127.0.0.1:0 for this test.
// It is never memoized or adopted: every call starts a fresh listener,
// stopped via t.Cleanup.
func (s *Suite) LocalEcho() *LocalService {
	return newLocalService(s.T())
}

// CLI returns a cli.TP bound to this run's binary, environment, and working
// directory.
func (s *Suite) CLI() *cli.TP {
	return R().CLI()
}

// SetupTest records the test's start time for the manifest and guarantees
// the suite's declared manager spec is the one installed: an earlier suite
// may have Mutated the shared release to a different spec, and a suite that
// never touches Manager()/Connect() would otherwise run against it.
func (s *Suite) SetupTest() {
	s.testStart = time.Now()
	if s.reg != nil && s.reg.hasSpec {
		Get(s.T(), ManagerFixture(s.reg.managerSpec))
	}
}

// TearDownTest records the test's outcome and duration for the manifest,
// and on failure dumps abnormal events for namespaces this suite has
// touched plus daemon log tails into ArtifactDir(<test>).
func (s *Suite) TearDownTest() {
	t := s.T()
	dur := time.Since(s.testStart)
	outcome := "pass"
	switch {
	case t.Skipped():
		outcome = "skip"
	case t.Failed():
		outcome = "fail"
	}
	var labels []Label
	if s.reg != nil {
		labels = s.reg.labelSlice()
	}
	R().manifest.recordTest(t.Name(), outcome, dur, labels)
	if outcome == "fail" {
		s.dumpDiagnostics(t)
	}
}

// dumpDiagnostics writes abnormal events for namespaces already provisioned
// by this run, and daemon log tails, into ArtifactDir(t.Name()). It never
// provisions a namespace itself, only inspects ones already memoized.
func (s *Suite) dumpDiagnostics(t testing.TB) {
	r := R()
	dir := r.ArtifactDir(sanitizeTestName(t.Name()))

	var namespaces []string
	if _, ok := r.engine.lookup(appNamespaceFixture().Hash); ok {
		namespaces = append(namespaces, AppNamespace)
	}
	if _, ok := r.engine.lookup(managerNamespaceFixture().Hash); ok {
		namespaces = append(namespaces, managers.ManagerNamespace)
	}
	ctx := context.Background()
	for _, ns := range namespaces {
		out, err := r.Kubectl(ctx, ns, "get", "events", "--field-selector", "type!=Normal")
		if err != nil {
			out = out + "\n" + err.Error()
		}
		_ = os.WriteFile(filepath.Join(dir, "events-"+ns+".txt"), []byte(out), 0o644)
	}
	copyDaemonLogTails(r, dir)
}

// sanitizeTestName replaces path separators in a (possibly slash-joined,
// subtest) test name so it can be used as a directory name.
func sanitizeTestName(name string) string {
	return strings.ReplaceAll(name, "/", "_")
}

// copyDaemonLogTails copies the last 64KiB of every *.log file in the run's
// stable log directory into destDir.
func copyDaemonLogTails(r *Runtime, destDir string) {
	const tailBytes = 64 * 1024
	entries, err := os.ReadDir(r.logDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		data, err := tailFile(filepath.Join(r.logDir, e.Name()), tailBytes)
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(destDir, e.Name()), data, 0o644)
	}
}

func tailFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		if _, err := f.Seek(-maxBytes, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}
