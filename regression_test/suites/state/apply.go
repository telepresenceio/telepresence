package state

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Apply ports state_manifest_test.go's dry-run/create/reuse/drift coverage
// of `telepresence apply -f` (pkg/client/cli/manifest/apply.go): a dry run
// against a clean workstation touches nothing, a real apply creates and a
// re-apply reuses/leaves things unchanged, a local-port change re-creates
// just the drifted attachment while its sibling stays unchanged, and a
// connection-shape change errors without touching the live session.
// Supersedes Test_ApplyDryRunConnectionMissing, Test_ApplyCreatesReusesUnchanged,
// Test_ApplyAttachmentDrift, Test_ApplyConnectionDrift.
type Apply struct {
	rt.Suite
}

func init() {
	rt.Register(&Apply{},
		rt.InArea("state"),
		rt.NeedsManager(managers.Default),
	)
}

// Test_ApplyDryRunConnectionMissing ports Test_ApplyDryRunConnectionMissing:
// a dry-run apply against a clean workstation (no daemon running at all)
// reports what it would do without starting either daemon.
func (s *Apply) Test_ApplyDryRunConnectionMissing() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	// Clean workstation: no daemon running at all, matching what a dry run
	// is meant to prove.
	quitAll(t, s.CLI(), ctx)

	s.Workload(workloads.Echo(stateInterceptWL))
	s.Workload(workloads.Echo(stateIngestWL))

	mf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort, false))

	res := applyJSON(t, s.CLI(), ctx, mf, true)
	s.Equal("would-connect", res.Connection)
	s.Equal("would-create", res.attachment(t, stateInterceptWL).Action)
	s.Equal("would-create", res.attachment(t, stateIngestWL).Action)

	var st cli.Status
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.False(st.UserDaemon.Running, "user daemon should stay down after a dry-run apply")
	s.False(st.RootDaemon.Running, "root daemon should stay down after a dry-run apply")
}

// Test_ApplyCreatesReusesUnchanged ports Test_ApplyCreatesReusesUnchanged: a
// first apply connects and creates both attachments, a re-apply reuses the
// connection and leaves both unchanged (dry-run and real alike), and delete
// disconnects.
func (s *Apply) Test_ApplyCreatesReusesUnchanged() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Workload(workloads.Echo(stateInterceptWL))
	s.Workload(workloads.Echo(stateIngestWL))
	defer quitAll(t, s.CLI(), ctx)

	mf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort, false))

	res := applyJSON(t, s.CLI(), ctx, mf, false)
	s.Equal("connected", res.Connection)
	s.Equal("created", res.attachment(t, stateInterceptWL).Action)
	s.Equal("created", res.attachment(t, stateIngestWL).Action)

	var entries []cli.ListEntry
	s.Require().NoError(s.CLI().JSON(ctx, &entries, "list", "--intercepts", "--format", "json"))
	s.True(listHasWorkload(entries, stateInterceptWL), "list --intercepts should report %s", stateInterceptWL)

	res = applyJSON(t, s.CLI(), ctx, mf, false)
	s.Equal("reused", res.Connection)
	s.Equal("unchanged", res.attachment(t, stateInterceptWL).Action)
	s.Equal("unchanged", res.attachment(t, stateIngestWL).Action)

	res = applyJSON(t, s.CLI(), ctx, mf, true)
	s.Equal("reused", res.Connection)
	s.Equal("unchanged", res.attachment(t, stateInterceptWL).Action)
	s.Equal("unchanged", res.attachment(t, stateIngestWL).Action)

	res = deleteJSON(t, s.CLI(), ctx, mf)
	s.Equal("disconnected", res.Connection)
}

// Test_ApplyAttachmentDrift ports Test_ApplyAttachmentDrift: changing the
// intercept attachment's local port re-creates just that attachment (dry-run
// reports would-re-create with a drift Detail, a real apply re-creates it),
// while the sibling ingest attachment -- unaffected by the change -- stays
// unchanged throughout.
func (s *Apply) Test_ApplyAttachmentDrift() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Workload(workloads.Echo(stateInterceptWL))
	s.Workload(workloads.Echo(stateIngestWL))
	defer quitAll(t, s.CLI(), ctx)

	mf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort, false))
	driftMf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort+1, false))

	applyJSON(t, s.CLI(), ctx, mf, false)

	res := applyJSON(t, s.CLI(), ctx, driftMf, true)
	ia := res.attachment(t, stateInterceptWL)
	s.Equal("would-re-create", ia.Action)
	s.NotEmpty(ia.Detail)
	s.Equal("unchanged", res.attachment(t, stateIngestWL).Action)

	res = applyJSON(t, s.CLI(), ctx, driftMf, false)
	ia = res.attachment(t, stateInterceptWL)
	s.Equal("re-created", ia.Action)
	s.NotEmpty(ia.Detail)
	s.Equal("unchanged", res.attachment(t, stateIngestWL).Action)

	var entries []cli.ListEntry
	s.Require().NoError(s.CLI().JSON(ctx, &entries, "list", "--intercepts", "--format", "json"))
	s.True(listHasWorkload(entries, stateInterceptWL))

	deleteJSON(t, s.CLI(), ctx, driftMf)
}

// Test_ApplyConnectionDrift ports Test_ApplyConnectionDrift: once a
// manifest's connection is live, re-applying a manifest whose connection
// block has drifted (mappedNamespaces changed) errors both as a dry run and
// for real, and leaves the live session and its attachments untouched.
func (s *Apply) Test_ApplyConnectionDrift() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Workload(workloads.Echo(stateInterceptWL))
	s.Workload(workloads.Echo(stateIngestWL))
	defer quitAll(t, s.CLI(), ctx)

	mf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort, false))
	driftMf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort, true))

	applyJSON(t, s.CLI(), ctx, mf, false)

	_, stderr, err := s.CLI().Run(ctx, "apply", "--dry-run", "-f", driftMf)
	s.Error(err)
	s.Contains(stderr, "has drifted from the manifest")

	_, stderr, err = s.CLI().Run(ctx, "apply", "-f", driftMf)
	s.Error(err)
	s.Contains(stderr, "has drifted from the manifest")

	var entries []cli.ListEntry
	s.Require().NoError(s.CLI().JSON(ctx, &entries, "list", "--intercepts", "--format", "json"))
	s.True(listHasWorkload(entries, stateInterceptWL),
		"a drifted manifest must leave the connection and its attachments untouched")

	deleteJSON(t, s.CLI(), ctx, mf)
}
