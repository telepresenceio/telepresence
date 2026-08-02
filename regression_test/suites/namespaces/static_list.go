package namespaces

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// StaticList proves a managers.StaticNamespaces spec manages exactly the
// namespaces it names, ignoring the managed label entirely: both listed
// namespaces are attachable, and a third, labeled-but-unlisted namespace is
// refused. The chart forbids namespaces and namespaceSelector together, so
// StaticNamespaces fully replaces the default selector rather than adding to
// it (managers.Merge nulls NamespaceSelector when Namespaces is set).
type StaticList struct {
	rt.Suite
}

func init() {
	rt.Register(&StaticList{}, rt.InArea("namespaces"), rt.NeedsManager(managers.Default))
}

// Test_ScopedToListedNamespaces switches the shared release to
// StaticNamespaces(nsA, nsB) (over two PrivateUnmanagedNamespace namespaces:
// a static list needs no managed label) and checks connect/list/attach
// against nsA and nsB, then that a third, managed-labeled namespace left out
// of the list is refused. The spec switch is acquired via rt.Mutate, so a
// later area re-provisions Default.
func (s *StaticList) Test_ScopedToListedNamespaces() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	// Ensure Default is the live spec (self-contained: correct even if this
	// suite runs without SelectorSemantics/MappedNamespaces beforehand), so
	// freeing the default connection below is safe.
	s.Manager()

	env := rt.Env{Ctx: ctx, T: t, R: r}
	nsA := rt.PrivateUnmanagedNamespace(env, "static-a")
	nsB := rt.PrivateUnmanagedNamespace(env, "static-b")
	// Labeled managed, but left out of the static list below: proves the
	// list, not the label, decides membership once namespaces is set.
	nsC := rt.PrivateNamespace(env, "static-c")

	freeDefaultConnection(t, s.AppNamespace())
	rt.Mutate(t, rt.ManagerFixture(managers.StaticNamespaces(nsA, nsB)))
	quitDefensively(t, r, ctx, "StaticList")

	connA := rt.Mutate(t, rt.ConnectionFixture(nsA))
	wlA := rt.Get(t, rt.WorkloadFixture(nsA, workloads.Echo("static-a-wl")))
	s.True(present(connA.List(t), wlA.Name, wlA.Namespace), "nsA's workload should be listed")

	ls := s.LocalEcho()
	a := connA.Intercept(t, wlA, rt.ToLocal(ls, "http"), cli.MountFalse())
	s.Eventually(func() bool {
		return attached(connA.List(t), wlA.Name, wlA.Namespace)
	}, attachPollTimeout, attachPollInterval, "intercept of %s did not show up in list", wlA.Name)
	a.Detach(t)
	connA.Disconnect(t)

	connB := rt.Mutate(t, rt.ConnectionFixture(nsB))
	wlB := rt.Get(t, rt.WorkloadFixture(nsB, workloads.Echo("static-b-wl")))
	s.True(present(connB.List(t), wlB.Name, wlB.Namespace), "nsB's workload should be listed")
	connB.Disconnect(t)

	ok, stderr := probeConnect(t, r, ctx, nsC)
	s.False(ok, "connect to a namespace outside the static list should be refused")
	s.Contains(strings.ToLower(stderr), "not managed")
}
