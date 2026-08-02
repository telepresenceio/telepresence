package install

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// HelmLifecycle drives `telepresence helm install/upgrade/uninstall`
// directly, against a fresh PrivateUnmanagedNamespace, never through a
// manager fixture and never against the shared release: install ->
// uninstall -> reinstall, a colliding second install into the same
// namespace, and a broken install (bogus image tag) followed by a
// corrected one.
type HelmLifecycle struct {
	rt.Suite
}

func init() {
	rt.Register(&HelmLifecycle{}, rt.InArea("install"))
}

// Test_InstallUninstallReinstall drives a release through install ->
// uninstall -> reinstall, checking the Deployment's presence/absence at
// each step.
func (s *HelmLifecycle) Test_InstallUninstallReinstall() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "lifecycle")

	install := func() {
		_, stderr, err := s.CLI().Run(ctx, helmInstallArgs(r, ns)...)
		s.Require().NoError(err, "helm install: %s", stderr)
	}
	uninstall := func() {
		_, stderr, err := s.CLI().Run(ctx, "helm", "uninstall", "--manager-namespace", ns)
		s.Require().NoError(err, "helm uninstall: %s", stderr)
	}

	install()
	t.Cleanup(uninstall)
	requireManagerReady(t, ctx, r, ns)

	uninstall()
	requireManagerAbsent(t, ctx, r, ns)

	install()
	requireManagerReady(t, ctx, r, ns)
}

// Test_CollidingInstall asserts a second `helm install` into the same
// namespace as an existing release fails with a clear error instead of
// silently overwriting it.
func (s *HelmLifecycle) Test_CollidingInstall() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "collide")

	args := helmInstallArgs(r, ns)
	_, stderr, err := s.CLI().Run(ctx, args...)
	s.Require().NoError(err, "helm install: %s", stderr)
	t.Cleanup(func() {
		_, _, _ = s.CLI().Run(ctx, "helm", "uninstall", "--manager-namespace", ns)
	})
	requireManagerReady(t, ctx, r, ns)

	_, stderr, err = s.CLI().Run(ctx, args...)
	s.Error(err, "a second helm install into the same namespace should fail")
	s.Contains(stderr, "already installed")
}

// Test_BrokenInstallThenCorrected installs with a bogus image tag, expects
// the failure to name the pod's not-ready reason (helm's --atomic rolls the
// failed release back entirely, leaving nothing to upgrade), then installs
// again with the correct image, which succeeds.
func (s *HelmLifecycle) Test_BrokenInstallThenCorrected() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "broken")

	brokenArgs := append([]string{"helm", "install", "--manager-namespace", ns},
		helmSetArgs(r, ns, "9.9.9")...) // a version whose image doesn't exist
	_, stderr, err := s.CLI().Run(ctx, brokenArgs...)
	s.Error(err, "install with a bogus image tag should fail")
	s.Contains(stderr, "traffic-manager pod is not ready")

	_, stderr, err = s.CLI().Run(ctx, helmInstallArgs(r, ns)...)
	s.Require().NoError(err, "corrected helm install: %s", stderr)
	t.Cleanup(func() {
		_, _, _ = s.CLI().Run(ctx, "helm", "uninstall", "--manager-namespace", ns)
	})
	requireManagerReady(t, ctx, r, ns)
}
