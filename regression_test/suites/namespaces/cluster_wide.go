package namespaces

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// clusterWideStatus is the minimal shape of `status --format json`'s
// user_daemon object needed to check the mapped-namespaces list (pkg/client/
// cli/cmd/status.go's UserDaemonStatus.MappedNamespaces).
type clusterWideStatus struct {
	UserDaemon struct {
		MappedNamespaces []string `json:"mapped_namespaces"`
	} `json:"user_daemon"`
}

// ClusterWide proves a spec with no namespace restriction manages every
// namespace: a workload in a namespace that never carries the managed label
// is listed and interceptable, and the session maps namespaces the test run
// never created.
//
// The chart refuses an unrestricted install while any other traffic-manager
// release exists anywhere in the cluster (the overlap validation in
// charts/telepresence-oss/templates/agentInjectorWebhook.yaml), so this
// suite requires the framework's single shared release to be the only
// manager on the cluster; a leftover release from other work fails the
// helm upgrade with "already manages namespace".
type ClusterWide struct {
	rt.Suite
}

func init() {
	rt.Register(&ClusterWide{}, rt.InArea("namespaces"), rt.NeedsManager(managers.Default))
}

// Test_ManagesEveryNamespace switches the shared release to
// managers.ClusterWide() and checks that an unlabeled namespace's workload is
// listed and interceptable, then that `status` reports namespaces this run
// never created or labeled among the session's mapped namespaces.
func (s *ClusterWide) Test_ManagesEveryNamespace() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	// Ensure Default is the live spec (self-contained: correct even if this
	// suite runs without SelectorSemantics/MappedNamespaces beforehand), so
	// freeing the default connection below is safe.
	s.Manager()

	env := rt.Env{Ctx: ctx, T: t, R: r}
	// Created before the spec switch below: the manager only lists
	// namespaces at pod start (see RestartManager's doc comment in
	// regression_test/framework/rt/fixture_manager.go), and the Mutate below
	// rolls the pod, so the namespace must already exist when it does.
	ns := rt.PrivateUnmanagedNamespace(env, "cluster-wide")

	freeDefaultConnection(t, s.AppNamespace())
	rt.Mutate(t, rt.ManagerFixture(managers.ClusterWide()))
	quitDefensively(t, r, ctx, "ClusterWide")

	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.Echo("cluster-wide-wl")))
	s.True(present(conn.List(t), wl.Name, wl.Namespace), "ns's workload should be listed")

	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	s.Eventually(func() bool {
		return attached(conn.List(t), wl.Name, wl.Namespace)
	}, attachPollTimeout, attachPollInterval, "intercept of %s did not show up in list", wl.Name)
	a.Detach(t)

	// The session maps namespaces this run never created or labeled --
	// except kube-system and kube-node-lease, which the chart's dynamic
	// selector always rejects, even unrestricted
	// (charts/telepresence-oss/templates/_helpers.tpl's
	// traffic-manager.namespaceSelector).
	var st clusterWideStatus
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.Contains(st.UserDaemon.MappedNamespaces, "default")
	s.Contains(st.UserDaemon.MappedNamespaces, "kube-public")
	s.NotContains(st.UserDaemon.MappedNamespaces, "kube-system")

	conn.Disconnect(t)
}
