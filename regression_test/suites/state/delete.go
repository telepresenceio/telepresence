package state

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Delete ports state_manifest_test.go's `telepresence delete -f` coverage
// (pkg/client/cli/manifest/delete.go): reverse-manifest-order teardown, a
// manually detached attachment reporting absent, the "not connected"/"not
// running; nothing to tear down" variants, and a connection-less manifest
// both requiring and preserving an already established session. Supersedes
// Test_Delete, Test_DeleteAbsentAttachment,
// Test_ApplyDeleteNoConnectionRequiresSession,
// Test_ApplyDeleteWithoutConnectionKeepsSession.
type Delete struct {
	rt.Suite
}

func init() {
	rt.Register(&Delete{},
		rt.InArea("state"),
		rt.NeedsManager(managers.Default),
	)
}

// Test_Delete ports Test_Delete: delete tears down attachments in reverse
// manifest order (ingest before intercept) and disconnects; repeating it
// against the now session-less daemon reports "not connected", and again
// after a full quit reports "not running".
func (s *Delete) Test_Delete() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Workload(workloads.Echo(stateInterceptWL))
	s.Workload(workloads.Echo(stateIngestWL))
	defer quitAll(t, s.CLI(), ctx)

	mf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort, false))
	applyJSON(t, s.CLI(), ctx, mf, false)

	res := deleteJSON(t, s.CLI(), ctx, mf)
	s.Equal("disconnected", res.Connection)
	s.Require().Len(res.Attachments, 2)
	// delete tears down in reverse manifest order: the ingest attachment
	// (declared second) is removed before the intercept (declared first).
	s.Equal(stateIngestWL, res.Attachments[0].Name)
	s.Equal("removed", res.Attachments[0].Action)
	s.Equal(stateInterceptWL, res.Attachments[1].Name)
	s.Equal("removed", res.Attachments[1].Action)

	var st cli.Status
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.Empty(st.TrafficManager.Name, "traffic manager should be disconnected")

	res = deleteJSON(t, s.CLI(), ctx, mf)
	s.Equal("not connected; nothing to tear down", res.Connection)

	quitAll(t, s.CLI(), ctx)

	res = deleteJSON(t, s.CLI(), ctx, mf)
	s.Equal("not running; nothing to tear down", res.Connection)
}

// Test_DeleteAbsentAttachment ports Test_DeleteAbsentAttachment: an
// attachment detached out-of-band (`telepresence detach`, not `delete -f`)
// reports absent instead of removed, while its sibling still reports
// removed normally.
func (s *Delete) Test_DeleteAbsentAttachment() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Workload(workloads.Echo(stateInterceptWL))
	s.Workload(workloads.Echo(stateIngestWL))
	defer quitAll(t, s.CLI(), ctx)

	mf := writeManifest(t, s.R(), ctx, connectionManifest(ns, stateLocalPort, false))
	applyJSON(t, s.CLI(), ctx, mf, false)

	_, stderr, err := s.CLI().Run(ctx, "detach", stateInterceptWL)
	s.Require().NoError(err, "detach: %s", stderr)

	res := deleteJSON(t, s.CLI(), ctx, mf)
	s.Equal("absent", res.attachment(t, stateInterceptWL).Action)
	s.Equal("removed", res.attachment(t, stateIngestWL).Action)
	s.Equal("disconnected", res.Connection)
}

// Test_ApplyDeleteNoConnectionRequiresSession ports
// Test_ApplyDeleteNoConnectionRequiresSession: a connection-less manifest
// against a clean workstation errors, for both apply and delete, instead of
// connecting implicitly.
func (s *Delete) Test_ApplyDeleteNoConnectionRequiresSession() {
	t := s.T()
	ctx := s.Ctx()

	// Clean workstation: no daemon running, so there is no "current session"
	// for the connection-less manifest to depend on.
	quitAll(t, s.CLI(), ctx)

	mf := writeManifest(t, s.R(), ctx, connectionlessManifest(stateLocalPort))

	_, stderr, err := s.CLI().Run(ctx, "apply", "-f", mf)
	s.Error(err)
	s.Contains(stderr, `not connected; run "telepresence connect" first`)

	_, stderr, err = s.CLI().Run(ctx, "delete", "-f", mf)
	s.Error(err)
	s.Contains(stderr, `not connected; run "telepresence connect" first`)

	var st cli.Status
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.False(st.UserDaemon.Running)
	s.False(st.RootDaemon.Running)
}

// Test_ApplyDeleteWithoutConnectionKeepsSession ports
// Test_ApplyDeleteWithoutConnectionKeepsSession: apply/delete against a
// connection-less manifest never print a connection line and never
// disconnect the session they depend on. Establishes the connection through
// the shared framework fixture (Mutate, since this test disturbs and then
// quits it) rather than a manifest, specifically to prove the manifest-driven
// verbs leave a pre-existing, independently owned session alone.
func (s *Delete) Test_ApplyDeleteWithoutConnectionKeepsSession() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Workload(workloads.Echo(stateInterceptWL))
	s.Workload(workloads.Echo(stateIngestWL))

	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	defer conn.Disconnect(t)

	mf := writeManifest(t, s.R(), ctx, connectionlessManifest(stateLocalPort))

	res := applyJSON(t, s.CLI(), ctx, mf, true)
	s.Empty(res.Connection)
	s.Equal("would-create", res.attachment(t, stateInterceptWL).Action)
	s.Equal("would-create", res.attachment(t, stateIngestWL).Action)

	res = applyJSON(t, s.CLI(), ctx, mf, false)
	s.Empty(res.Connection)
	s.Equal("created", res.attachment(t, stateInterceptWL).Action)
	s.Equal("created", res.attachment(t, stateIngestWL).Action)

	res = applyJSON(t, s.CLI(), ctx, mf, false)
	s.Equal("unchanged", res.attachment(t, stateInterceptWL).Action)
	s.Equal("unchanged", res.attachment(t, stateIngestWL).Action)

	res = deleteJSON(t, s.CLI(), ctx, mf)
	s.Empty(res.Connection)
	s.Equal("removed", res.attachment(t, stateInterceptWL).Action)
	s.Equal("removed", res.attachment(t, stateIngestWL).Action)

	var st cli.Status
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.NotEmpty(st.TrafficManager.Name, "session should survive a connection-less apply/delete")
}
