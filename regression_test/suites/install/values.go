package install

import (
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// HelmValues validates --reuse-values vs the default (reset-values) upgrade
// semantics against a SecondaryManager release in a PrivateUnmanagedNamespace,
// via raw `telepresence helm upgrade` CLI calls: never the shared release.
type HelmValues struct {
	rt.Suite
}

func init() {
	rt.Register(&HelmValues{}, rt.InArea("install"))
}

// secondaryBaselineValues reconstructs the managers.Values a SecondaryManager
// installs with in ns (managers.Default plus a namespaceSelector scoped to
// ns; see framework/rt/fixture_manager2.go's provisionSecondaryManager), used
// to write a full values file for a plain (reset-values) upgrade back to
// that baseline.
func secondaryBaselineValues(r *rt.Runtime, ns string) managers.Values {
	base := managers.Baseline(r.Registry(), r.Version().String(), pullPolicyFor(r.Registry()), "true")
	values := managers.Merge(base, managers.Default.Values)
	values.NamespaceSelector = &labels.Selector{MatchLabels: map[string]string{labels.NameLabelKey: ns}}
	return values
}

// Test_ReuseThenResetValues upgrades a SecondaryManager release with
// --set logLevel=info --reuse-values (merging on top of the currently
// deployed values), then with a plain `-f <baseline>` upgrade (the default,
// reset-values, semantics), which restores the baseline's logLevel.
func (s *HelmValues) Test_ReuseThenResetValues() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "helm-values")

	h := rt.Get(t, rt.SecondaryManager(managers.Default, ns))
	s.Equal(ns, h.Namespace)
	requireManagerReady(t, ctx, r, ns)
	s.Equal("debug", managerLogLevel(t, ctx, r, ns), "baseline logLevel")

	_, stderr, err := s.CLI().Run(ctx, "helm", "upgrade", "--manager-namespace", ns,
		"--set", "logLevel=info", "--reuse-values")
	s.Require().NoError(err, "helm upgrade --reuse-values: %s", stderr)
	requireManagerReady(t, ctx, r, ns)
	s.Equal("info", managerLogLevel(t, ctx, r, ns), "logLevel after --reuse-values upgrade")

	baseline := secondaryBaselineValues(r, ns)
	data, err := yaml.Marshal(baseline)
	s.Require().NoError(err)
	path := filepath.Join(t.TempDir(), "baseline-values.yaml")
	s.Require().NoError(os.WriteFile(path, data, 0o644))

	_, stderr, err = s.CLI().Run(ctx, "helm", "upgrade", "--manager-namespace", ns, "-f", path)
	s.Require().NoError(err, "plain helm upgrade -f: %s", stderr)
	requireManagerReady(t, ctx, r, ns)
	s.Equal("debug", managerLogLevel(t, ctx, r, ns), "logLevel after the plain (reset-values) upgrade")
}
