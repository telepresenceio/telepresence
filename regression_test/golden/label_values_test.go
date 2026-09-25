package golden

import (
	"io"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// buildMetaVersion is a chart version carrying SemVer build metadata, the
// shape a tool such as Flux's helm-controller produces when it appends an
// OCI artifact digest to the chart version of an OCI chart source.
const buildMetaVersion = "2.32.0+abc123"

// renderChartVersion is renderChart with the chart's Metadata.Version
// replaced, without mutating the chart loadChart memoizes.
func renderChartVersion(t *testing.T, vals map[string]any, version string) map[string]string {
	t.Helper()
	chrt, err := loadChart()
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	versioned := *chrt
	meta := *chrt.Metadata
	meta.Version = version
	versioned.Metadata = &meta

	rv, err := chartutil.ToRenderValues(&versioned, vals,
		chartutil.ReleaseOptions{Name: releaseName, Namespace: releaseNamespace, IsInstall: true},
		chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("coalesce values: %v", err)
	}
	out, err := (engine.Engine{}).Render(&versioned, rv)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}

// labelValueCombos are chart value overlays chosen so that, together, they
// render every optional workload and hook alongside the always-on
// StatefulSet and hook Jobs.
func labelValueCombos() []struct {
	name string
	vals map[string]any
} {
	return []struct {
		name string
		vals map[string]any
	}{
		{"defaults", map[string]any{}},
		{"quic-forwarder", map[string]any{
			"quicTunnel": map[string]any{"enabled": true},
		}},
		{"node-agent", map[string]any{
			"nodeAgent": map[string]any{"enabled": true},
		}},
		{"agent-injector", map[string]any{
			"agentInjector": map[string]any{
				"enabled":      true,
				"injectPolicy": "WhenEnabled",
			},
		}},
		{"external-endpoint", map[string]any{
			"security": map[string]any{
				"authentication": map[string]any{"mode": "enforcing"},
			},
			"externalEndpoint": map[string]any{
				"enabled": true,
				"tls": map[string]any{
					"secretName": "rtest-external-tls",
				},
			},
		}},
	}
}

// optionalStringMap is quicForwarderStringMap but reports absence via ok
// instead of failing the test, since not every resource kind has every
// label field checked by TestLabelValuesValidWithBuildMetadata.
func optionalStringMap(t *testing.T, resource *unstructured.Unstructured, fields ...string) (map[string]string, bool) {
	t.Helper()
	values, found, err := unstructured.NestedStringMap(resource.Object, fields...)
	if err != nil {
		t.Fatalf("read %s %q %s: %v", resource.GetKind(), resource.GetName(), strings.Join(fields, "."), err)
	}
	return values, found
}

// checkLabelValues validates every value in labels with
// validation.IsValidLabelValue, reporting the template and resource for
// any offender.
func checkLabelValues(t *testing.T, tpl, kind, name string, labels map[string]string) {
	t.Helper()
	for key, value := range labels {
		for _, msg := range validation.IsValidLabelValue(value) {
			t.Errorf("%s: %s %q label %q = %q: %s", tpl, kind, name, key, value, msg)
		}
	}
}

// checkResourceLabels validates the label maps a Kubernetes API server
// would reject an invalid value in: object labels, pod-template labels,
// selector labels, and a CronJob's nested job-template labels.
func checkResourceLabels(t *testing.T, tpl string, resource *unstructured.Unstructured) {
	t.Helper()
	kind, name := resource.GetKind(), resource.GetName()
	for _, fields := range [][]string{
		{"metadata", "labels"},
		{"spec", "template", "metadata", "labels"},
		{"spec", "selector", "matchLabels"},
		{"spec", "jobTemplate", "spec", "template", "metadata", "labels"},
	} {
		if labels, found := optionalStringMap(t, resource, fields...); found {
			checkLabelValues(t, tpl, kind, name, labels)
		}
	}
}

// TestLabelValuesValidWithBuildMetadata renders the chart with a version
// carrying SemVer build metadata over labelValueCombos and asserts that
// every label value in every rendered document is a valid Kubernetes label
// value, catching any template that builds a label from the raw chart
// version instead of the sanitizing telepresence.chart helper.
func TestLabelValuesValidWithBuildMetadata(t *testing.T) {
	seen := map[string]bool{}
	for _, combo := range labelValueCombos() {
		t.Run(combo.name, func(t *testing.T) {
			out := renderChartVersion(t, combo.vals, buildMetaVersion)
			for tpl, doc := range out {
				if !strings.HasSuffix(tpl, ".yaml") || strings.TrimSpace(doc) == "" {
					continue
				}
				decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(doc), 4096)
				for {
					resource := &unstructured.Unstructured{}
					err := decoder.Decode(resource)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("decode %s: %v", tpl, err)
					}
					if resource.Object == nil || resource.GetKind() == "" {
						continue
					}
					seen[resource.GetKind()+"/"+resource.GetName()] = true
					checkResourceLabels(t, tpl, resource)
				}
			}
		})
	}

	for _, want := range []string{
		"Job/traffic-manager-migrate-statefulset",
		"Job/uninstall-agents",
		"StatefulSet/traffic-manager",
	} {
		if !seen[want] {
			t.Errorf("labelValueCombos never rendered %s; test would pass vacuously", want)
		}
	}
}

// TestHookJobChartLabelSanitized asserts that the pre-upgrade and
// pre-delete hook Jobs' pod-template helm.sh/chart label is built through
// the sanitizing telepresence.chart helper, both for a version carrying
// build metadata and for the chart's own version.
func TestHookJobChartLabelSanitized(t *testing.T) {
	hookJobs := []struct {
		tpl  string
		name string
	}{
		{preUpgradeHookTpl, "traffic-manager-migrate-statefulset"},
		{preDeleteHookTpl, "uninstall-agents"},
	}

	buildMeta := renderChartVersion(t, map[string]any{}, buildMetaVersion)
	for _, hj := range hookJobs {
		job := quicForwarderResource(t, buildMeta, hj.tpl, "Job", hj.name)
		labels := quicForwarderStringMap(t, job, "spec", "template", "metadata", "labels")
		if want, got := "telepresence-oss-2.32.0_abc123", labels["helm.sh/chart"]; got != want {
			t.Errorf("%s: Job %q helm.sh/chart = %q, want %q", hj.tpl, hj.name, got, want)
		}
	}

	chrt, err := loadChart()
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	wantPlain := "telepresence-oss-" + chrt.Metadata.Version
	plain := renderChart(t, map[string]any{})
	for _, hj := range hookJobs {
		job := quicForwarderResource(t, plain, hj.tpl, "Job", hj.name)
		labels := quicForwarderStringMap(t, job, "spec", "template", "metadata", "labels")
		if got := labels["helm.sh/chart"]; got != wantPlain {
			t.Errorf("%s: Job %q helm.sh/chart = %q, want %q", hj.tpl, hj.name, got, wantPlain)
		}
	}
}
