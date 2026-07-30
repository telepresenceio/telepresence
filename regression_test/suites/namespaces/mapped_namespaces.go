package namespaces

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// MappedNamespaces proves --mapped-namespaces scopes a connection's DNS/list
// visibility to the given namespaces, independent of which namespaces the
// shared manager itself manages: a namespace the manager would happily serve
// is invisible to list once it falls outside the flag's value.
//
// rt.ConnOpt has no constructor for --mapped-namespaces: fixture_connection.go's
// connSpec (the type every ConnOpt closes over) is unexported, so a suite
// package can't add one from the outside, and rt.ConnWithConfig is the wrong
// tool (--mapped-namespaces is a connect flag, not a client config field).
// This suite drives connect via a raw CLI invocation instead, like several
// suites/connect tests already do; see docs/plans/regression-test-framework/
// m3-wave2-spec.md's namespaces-area entry for the gap.
type MappedNamespaces struct {
	rt.Suite
}

func init() {
	rt.Register(&MappedNamespaces{}, rt.InArea("namespaces"), rt.NeedsManager(managers.Default))
}

// Test_ScopedToMappedNamespaces connects with --mapped-namespaces limited to
// the app namespace and checks that list shows the app namespace's workload,
// while list -n on a second, otherwise-managed namespace comes back empty:
// the daemon never watches it, since it fell outside --mapped-namespaces
// (pkg/client/cli/daemon/request.go's InitRequest registers the flag on
// every command using InitRequest, connect included).
func (s *MappedNamespaces) Test_ScopedToMappedNamespaces() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()

	appNS := s.AppNamespace()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	otherNS := rt.PrivateNamespace(env, "mapped-other")

	wlApp := s.Workload(workloads.Echo("mapped-app-wl"))
	wlOther := rt.Get(t, rt.WorkloadFixture(otherNS, workloads.Echo("mapped-other-wl")))

	freeDefaultConnection(t, appNS)
	quitDefensively(t, r, ctx, "MappedNamespaces")

	args := append(rawConnectArgs(appNS), "--mapped-namespaces", appNS)
	_, stderr, err := r.CLI().Run(ctx, args...)
	s.Require().NoError(err, "connect --mapped-namespaces %s: %s", appNS, stderr)

	var appEntries []cli.ListEntry
	s.Require().NoError(r.CLI().JSON(ctx, &appEntries, "list", "--format", "json"))
	s.True(present(appEntries, wlApp.Name, wlApp.Namespace), "app namespace's workload should be listed")

	var otherEntries []cli.ListEntry
	s.Require().NoError(r.CLI().JSON(ctx, &otherEntries, "list", "--format", "json", "-n", otherNS))
	s.Empty(otherEntries, "namespace %s outside --mapped-namespaces should list empty (workload %s)",
		otherNS, wlOther.Name)
}
