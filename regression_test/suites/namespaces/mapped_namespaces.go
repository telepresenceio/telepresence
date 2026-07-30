package namespaces

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// MappedNamespaces proves --mapped-namespaces scopes a connection's DNS/list
// visibility to the given namespaces, independent of which namespaces the
// shared manager itself manages: a namespace the manager would happily serve
// is invisible to list once it falls outside the flag's value.
//
// Drives connect through rt.ConnExtraArgs("--mapped-namespaces", ns): the
// framework has no dedicated ConnOpt for --mapped-namespaces, and
// ConnWithConfig is the wrong tool (it's a connect flag, not a client
// config field), but ConnExtraArgs (added in m3 wave 4) covers any connect
// flag verbatim, folded into the fixture hash like every other ConnOpt.
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

	conn := rt.Mutate(t, rt.ConnectionFixture(appNS, rt.ConnExtraArgs("--mapped-namespaces", appNS)))

	appEntries := conn.List(t)
	s.True(present(appEntries, wlApp.Name, wlApp.Namespace), "app namespace's workload should be listed")

	otherEntries := conn.ListNamespace(t, otherNS)
	s.Empty(otherEntries, "namespace %s outside --mapped-namespaces should list empty (workload %s)",
		otherNS, wlOther.Name)
}
