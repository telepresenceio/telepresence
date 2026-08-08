// Package golden renders charts/telepresence-oss's local chart source over
// a value matrix and asserts structural invariants on the output, with no
// cluster and no dependency on the regression_test framework's runtime
// (rt.Main): this directory holds only _test.go files, so `go test
// ./regression_test/golden` builds and runs standalone.
package golden

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// chartDir is charts/telepresence-oss, resolved relative to this source
// file so the test runs from any working directory (`go test ./...` from
// the repo root, `go test .` from this package, an IDE's per-file runner).
func chartDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("golden: runtime.Caller(0) failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "charts", "telepresence-oss")
}

// loadChart loads the chart once per test binary run: parsing ~20 template
// files on every combination would dominate the matrix's runtime for no
// benefit, since the chart source doesn't change mid-run.
//
//nolint:gochecknoglobals // memoized chart load, see loadChart
var loadChart = sync.OnceValues(func() (*chart.Chart, error) {
	return loader.Load(chartDir())
})

// releaseName/releaseNamespace are the fixed Release.Name/Namespace every
// render in this package uses; nothing in the matrix depends on them.
const (
	releaseName      = "traffic-manager"
	releaseNamespace = "rtest-golden"
)

// renderChart renders the chart with vals layered over its own values.yaml
// defaults (chartutil.CoalesceValues, via ToRenderValues) plus the built-in
// Capabilities/Release values templates reference (KubeVersion in
// particular: _helpers.tpl and trafficManagerRbac/namespace-scope.yaml both
// branch on it). Loading the bare directory rather than a packaged archive
// means there is no values.schema.json to validate against, so this
// exercises exactly the templates' own guards -- the same surface `helm
// template charts/telepresence-oss` would reach.
//
// The chart's overlap-validation template (agentInjectorWebhook.yaml) calls
// `lookup`, which is a documented no-op with the zero-value engine.Engine
// used here (no Kubernetes client configured): it always sees zero existing
// namespaces/configmaps, so the overlap check never fires offline.
func renderChart(t *testing.T, vals map[string]any) map[string]string {
	t.Helper()
	chrt, err := loadChart()
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	rv, err := chartutil.ToRenderValues(chrt, vals,
		chartutil.ReleaseOptions{Name: releaseName, Namespace: releaseNamespace, IsInstall: true},
		chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("coalesce values: %v", err)
	}
	out, err := (engine.Engine{}).Render(chrt, rv)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}

// rendered reports whether template name produced non-whitespace output.
// engine.Render always includes a map entry for every template file, even
// ones whose top-level `{{- if }}` guard suppressed all content (the
// leftover value is whitespace from the surrounding template markers), so
// map-key presence alone is not a signal -- only the trimmed content is.
func rendered(out map[string]string, name string) bool {
	return strings.TrimSpace(out[name]) != ""
}

// Template file paths within the rendered map, relative to the chart name
// (engine.Render keys every entry "<chart.Name>/<template.Name>").
const (
	statefulsetTpl        = "telepresence-oss/templates/statefulset.yaml"
	headlessServiceTpl    = "telepresence-oss/templates/service.yaml"
	webhookTpl            = "telepresence-oss/templates/agentInjectorWebhook.yaml"
	quicFwdTpl            = "telepresence-oss/templates/quicforwarder.yaml"
	nodeAgentTpl          = "telepresence-oss/templates/trafficManagerRbac/node-agent.yaml"
	x509Tpl               = "telepresence-oss/templates/trafficManagerRbac/x509-auth.yaml"
	clientConnectTpl      = "telepresence-oss/templates/clientRbac/connect.yaml"
	clientClusterScopeTpl = "telepresence-oss/templates/clientRbac/cluster-scope.yaml"
	clientNamespaceTpl    = "telepresence-oss/templates/clientRbac/namespace-scope.yaml"
	preUpgradeHookTpl     = "telepresence-oss/templates/pre-upgrade-hook.yaml"
)
