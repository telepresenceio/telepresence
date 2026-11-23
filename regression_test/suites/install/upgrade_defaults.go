package install

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// releasedChartVersion is a released telepresence-oss chart whose default
// authentication mode is permissive.
const releasedChartVersion = "2.33.4"

// HelmUpgradeDefaults upgrades a release installed from a released chart
// with `telepresence helm upgrade` and no value flags, against a
// PrivateUnmanagedNamespace of its own, never the shared release.
type HelmUpgradeDefaults struct {
	rt.Suite
}

func init() {
	rt.Register(&HelmUpgradeDefaults{}, rt.InArea("install"))
}

// managerAuthMode returns the AUTHENTICATION_MODE env var of the
// traffic-manager StatefulSet in ns, empty when the chart does not set it.
func managerAuthMode(t testing.TB, ctx context.Context, r *rt.Runtime, ns string) string {
	t.Helper()
	out, err := r.Kubectl(ctx, ns, "get", trafficManagerStatefulSet, "-o",
		`jsonpath={.spec.template.spec.containers[*].env[?(@.name=="AUTHENTICATION_MODE")].value}`)
	if err != nil {
		t.Fatalf("reading traffic-manager statefulset in %s: %v", ns, err)
	}
	return strings.TrimSpace(out)
}

// managerImage returns the image of the traffic-manager container of the
// StatefulSet in ns.
func managerImage(t testing.TB, ctx context.Context, r *rt.Runtime, ns string) string {
	t.Helper()
	out, err := r.Kubectl(ctx, ns, "get", trafficManagerStatefulSet, "-o", "jsonpath={.spec.template.spec.containers[0].image}")
	if err != nil {
		t.Fatalf("reading traffic-manager image in %s: %v", ns, err)
	}
	return strings.TrimSpace(out)
}

// installReleased installs releasedChartVersion into ns with its released
// image from ghcr.io/telepresenceio, as an existing installation has, and
// extra --set arguments. The release is uninstalled at the end of the test.
func (s *HelmUpgradeDefaults) installReleased(ns string, extra ...string) {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	args := slices.Concat([]string{
		"helm", "install", "--manager-namespace", ns, "--version", releasedChartVersion,
		"--set", "namespaces={" + ns + "}", "--set", "image.registry=ghcr.io/telepresenceio",
	}, extra)
	_, stderr, err := s.CLI().Run(ctx, args...)
	t.Cleanup(func() {
		_, _, _ = s.CLI().Run(ctx, "helm", "uninstall", "--manager-namespace", ns)
	})
	s.Require().NoError(err, "helm install of chart %s: %s", releasedChartVersion, stderr)
	requireManagerReady(t, ctx, r, ns)
}

// upgradeWithoutValues runs the upgrade with no value flags. The client
// supplies its own image registry and version.
func (s *HelmUpgradeDefaults) upgradeWithoutValues(ns string) {
	_, stderr, err := s.CLI().Run(s.Ctx(), "helm", "upgrade", "--manager-namespace", ns)
	s.Require().NoError(err, "helm upgrade without value flags: %s", stderr)
	requireManagerReady(s.T(), s.Ctx(), s.R(), ns)
}

// Test_UnsetModeBecomesEnforcing installs a chart that defaults to
// permissive, without setting the mode, and upgrades. The new chart's
// default applies, so the release ends up enforcing, with the image of the
// version under test.
func (s *HelmUpgradeDefaults) Test_UnsetModeBecomesEnforcing() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := rt.PrivateUnmanagedNamespace(rt.Env{Ctx: ctx, T: t, R: r}, "upgrade-mode")

	s.installReleased(ns)
	s.Equal("permissive", managerAuthMode(t, ctx, r, ns), "mode of the released chart before the upgrade")
	s.True(strings.HasSuffix(managerImage(t, ctx, r, ns), "/tel2:"+releasedChartVersion), "image before the upgrade")

	s.upgradeWithoutValues(ns)
	s.Equal("enforcing", managerAuthMode(t, ctx, r, ns), "mode after an upgrade without value flags")
	s.True(strings.HasSuffix(managerImage(t, ctx, r, ns), "/tel2:"+r.Version().String()), "image after the upgrade")
}

// Test_ExplicitPermissiveStaysPermissive sets the mode at install time. An
// upgrade without value flags keeps the value and moves to the image of the
// version under test.
func (s *HelmUpgradeDefaults) Test_ExplicitPermissiveStaysPermissive() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := rt.PrivateUnmanagedNamespace(rt.Env{Ctx: ctx, T: t, R: r}, "upgrade-keep")

	s.installReleased(ns, "--set", "security.authentication.mode=permissive")
	s.Equal("permissive", managerAuthMode(t, ctx, r, ns), "mode before the upgrade")
	s.True(strings.HasSuffix(managerImage(t, ctx, r, ns), "/tel2:"+releasedChartVersion), "image before the upgrade")

	s.upgradeWithoutValues(ns)
	s.Equal("permissive", managerAuthMode(t, ctx, r, ns), "mode after an upgrade without value flags")
	s.True(strings.HasSuffix(managerImage(t, ctx, r, ns), "/tel2:"+r.Version().String()), "image after the upgrade")
}
