package attach

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AttachConflicts proves the same-workload conflict matrix: a second
// global (unfiltered) intercept on an already-intercepted container port
// fails, ingest and intercept coexist on the same workload (ingest never
// claims a port or registers a manager-side Intercept), and repeating an
// identical ingest is idempotent.
type AttachConflicts struct {
	rt.Suite
}

func init() {
	rt.Register(&AttachConflicts{}, rt.InArea("attach"), rt.NeedsManager(managers.Default))
}

// rawInterceptArgs builds the argument list Conn.attach would build for
// "intercept", but with an explicit intercept name so a caller can give it
// one distinct from the workload's own (Conn.Intercept always reuses
// wl.Name). Conn.Intercept calls t.Fatalf on a non-zero exit, which a
// conflict test that expects failure can't use, so conflict tests that need
// to observe the failure invoke the CLI directly with this instead.
func rawInterceptArgs(name string, wl *rt.Workload, opts ...cli.InterceptOpt) []string {
	args := []string{"intercept", name, "--namespace", wl.Namespace, "--format", "json"}
	for _, o := range opts {
		args = append(args, o()...)
	}
	return args
}

// Test_InterceptInterceptConflict proves a second global intercept on the
// same container port fails. Two intercepts without header/path filters
// are both "global", and any two globals conflict
// (pkg/icept/conflicts.go:234, IsInConflict's isGlobal1||isGlobal2
// branch); the manager's checkInterceptConflicts turns that into an error
// (cmd/traffic/cmd/manager/state/intercept.go:404: "conflict with
// intercept %s on %s created by client %q: %s"), which PrepareIntercept
// returns as pi.Error and the userd client re-raises as a plain error
// (pkg/client/userd/trafficmgr/intercept.go:575-576) that the CLI prints
// (to stdout or stderr, depending on --format) and exits non-zero for.
func (s *AttachConflicts) Test_InterceptInterceptConflict() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("attach-conflict-ii"))
	ls1 := s.LocalEcho()
	ls2 := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls1, "http"), cli.MountFalse())
	defer a.Detach(t)

	// A distinct name for the second intercept: same name would fail with
	// "already exists" before ever reaching the port-conflict path.
	args := rawInterceptArgs(wl.Name+"-second", wl, cli.WorkloadFlag(wl.Name), rt.ToLocal(ls2, "http"), cli.MountFalse())
	stdout, stderr, err := s.CLI().Run(s.Ctx(), args...)
	s.Error(err, "a second global intercept on the same container port should fail")
	// --format json puts the error on stdout, not stderr; check both.
	s.Contains(stdout+stderr, "conflict with intercept", "output should name the conflicting intercept")
}

// Test_IngestThenIntercept proves ingest and intercept coexist on the same
// workload. The Ingest RPC (pkg/client/userd/trafficmgr/ingest.go) never
// checks the manager's per-workload Intercept records, and its only
// cross-attachment check, ensureNoMountConflict
// (pkg/client/userd/trafficmgr/mount.go:78-110), no-ops when mounting is
// disabled, as it is here. Both attaches must succeed.
func (s *AttachConflicts) Test_IngestThenIntercept() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("attach-conflict-ingest-intercept"))
	ls := s.LocalEcho()

	ig := conn.Ingest(t, wl, cli.MountFalse())
	ic := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())

	s.True(listContains(conn.List(t), wl.Name, wl.Namespace), "list should show %s.%s", wl.Name, wl.Namespace)

	// Detach the intercept before the ingest: `detach <name>` resolves an
	// exact intercept-spec-name match before falling back to an ingest
	// lookup (pkg/client/cli/cmd/detach.go:103-109). Both attaches share
	// the workload's name here, so detaching the ingest first would
	// instead remove the intercept a second time and leave the ingest
	// dangling.
	ic.Detach(t)
	ig.Detach(t)
}

// Test_InterceptThenIngest is Test_IngestThenIntercept with the attach
// order reversed; the same no-conflict behavior and detach-order caveat
// apply.
func (s *AttachConflicts) Test_InterceptThenIngest() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("attach-conflict-intercept-ingest"))
	ls := s.LocalEcho()

	ic := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	ig := conn.Ingest(t, wl, cli.MountFalse())

	s.True(listContains(conn.List(t), wl.Name, wl.Namespace), "list should show %s.%s", wl.Name, wl.Namespace)

	ic.Detach(t)
	ig.Detach(t)
}

// Test_IngestRepeat proves a repeated, identical ingest is idempotent: the
// fast path in the Ingest RPC (pkg/client/userd/trafficmgr/ingest.go:152-
// 154) returns the cached response for an already-ingested workload
// instead of erroring or creating a second attachment.
func (s *AttachConflicts) Test_IngestRepeat() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("attach-conflict-repeat"))

	a1 := conn.Ingest(t, wl, cli.MountFalse())
	defer a1.Detach(t)
	a2 := conn.Ingest(t, wl, cli.MountFalse())

	s.Require().NotNil(a1.Ingest, "first ingest response should include IngestInfo")
	s.Require().NotNil(a2.Ingest, "repeated ingest response should include IngestInfo")
	s.Equal(a1.Ingest.WorkloadName, a2.Ingest.WorkloadName)
	s.Equal(a1.Ingest.Container, a2.Ingest.Container)

	count := 0
	for _, e := range conn.List(t) {
		if e.Name == wl.Name && e.Namespace == wl.Namespace {
			count++
		}
	}
	s.Equal(1, count, "repeated ingest should not duplicate the list entry")
}
