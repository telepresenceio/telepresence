package namespaces

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// SelectorSemantics proves the shared manager's namespaceSelector reacts to
// a namespace's managed label: unlabeled, connect is refused; labeled, the
// namespace becomes attachable; unlabeled again, connect is refused once
// more.
type SelectorSemantics struct {
	rt.Suite
}

func init() {
	rt.Register(&SelectorSemantics{}, rt.InArea("namespaces"), rt.NeedsManager(managers.Default))
}

// Test_LabelToggle drives a PrivateUnmanagedNamespace through
// unlabeled -> labeled -> unlabeled, checking connect's admission at each
// step. Every attempt against the private namespace is a raw CLI probe
// (probeConnect); the one connect expected to succeed is then handed to
// rt.ConnectionFixture via rt.Mutate to get a real *rt.Conn for the
// list/intercept checks.
func (s *SelectorSemantics) Test_LabelToggle() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()

	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "selector")

	// The shared release is still Default here (just ensured above), so
	// freeing the default connection is safe: AppNamespace stays reachable.
	freeDefaultConnection(t, s.AppNamespace())
	quitDefensively(t, r, ctx, "SelectorSemantics")

	// Unlabeled: connect is refused immediately, nothing to wait on.
	ok, stderr := probeConnect(t, r, ctx, ns)
	s.False(ok, "connect to an unlabeled namespace should be refused")
	s.Contains(strings.ToLower(stderr), "not managed")

	// Label it managed; the manager only sees it once it restarts (no live
	// namespaceSelector pickup), so restart before polling for admission.
	_, err := r.Kubectl(ctx, "", "label", "namespace", ns, managers.ManagedNamespaceLabel+"=true", "--overwrite")
	s.Require().NoError(err)
	s.Require().NoError(rt.RestartManager(env))
	s.Eventually(func() bool {
		ok, _ := probeConnect(t, r, ctx, ns)
		return ok
	}, labelPollTimeout, labelPollInterval, "connect to %s did not succeed after labeling it managed", ns)

	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.Echo("selector-wl")))
	s.True(present(conn.List(t), wl.Name, wl.Namespace), "labeled namespace's workload should be listed")

	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	s.Eventually(func() bool {
		return attached(conn.List(t), wl.Name, wl.Namespace)
	}, attachPollTimeout, attachPollInterval, "intercept of %s did not show up in list", wl.Name)
	a.Detach(t)
	conn.Disconnect(t)

	// Unlabel; the manager only forgets the namespace once it restarts, same
	// as the label direction above. A probe that unexpectedly still succeeds
	// (label removal not yet propagated) is quit before the next attempt,
	// so every attempt reflects the manager's current state.
	_, err = r.Kubectl(ctx, "", "label", "namespace", ns, managers.ManagedNamespaceLabel+"-")
	s.Require().NoError(err)
	s.Require().NoError(rt.RestartManager(env))
	var lastStderr string
	s.Eventually(func() bool {
		ok, stderr := probeConnect(t, r, ctx, ns)
		if ok {
			if _, _, err := r.CLI().Run(ctx, "quit", "-s"); err != nil {
				r.Infof("[rtest] SelectorSemantics: quit -s after unexpected connect success: %v", err)
			}
			r.ForgetConnections()
			return false
		}
		lastStderr = stderr
		return true
	}, labelPollTimeout, labelPollInterval, "connect to %s did not become refused after removing the label", ns)
	s.Contains(strings.ToLower(lastStderr), "not managed")
}
