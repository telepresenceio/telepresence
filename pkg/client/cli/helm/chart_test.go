package helm

import (
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// renderCoreChart renders the embedded telepresence-oss chart with vals coalesced
// over the chart's own defaults and returns the render error (nil when every template
// renders). withSchema selects whether the chart's values schema validates vals
// first, as it does for every packaged install; rendering the bare chart directory
// (helm template charts/telepresence-oss) has no values.schema.json and skips it,
// which is the path that reaches the templates' own guards.
func renderCoreChart(t *testing.T, vals map[string]any, withSchema bool) error {
	t.Helper()
	chrt, err := loadCoreChart(semver.MustParse("2.31.0"))
	require.NoError(t, err)
	if !withSchema {
		chrt.Schema = nil
	}
	rv, err := chartutil.ToRenderValues(chrt, vals,
		chartutil.ReleaseOptions{Name: "traffic-manager", Namespace: "ambassador", IsInstall: true},
		chartutil.DefaultCapabilities)
	if err != nil {
		return err
	}
	_, err = engine.Engine{}.Render(chrt, rv)
	return err
}

// TestQuicTunnelRequiresSingleManagerReplica pins both lines of defense against
// running more than one traffic-manager replica (unsupported: the CA and
// session state are process-local, and the known-name connect path dials pod
// traffic-manager-0 specifically):
//
//   - The values schema pins replicaCount to exactly 1 (const), so a packaged
//     install rejects any scaling attempt before a template renders.
//   - The StatefulSet template refuses replicaCount > 1 unconditionally, which
//     is what protects the schema-less render paths (the bare chart directory)
//     and stays load-bearing if the schema's replicaCount pin is ever relaxed.
func TestQuicTunnelRequiresSingleManagerReplica(t *testing.T) {
	require.NoError(t, renderCoreChart(t, map[string]any{
		"quicTunnel": map[string]any{"enabled": true},
	}, true), "quicTunnel.enabled with the default single replica must render")

	err := renderCoreChart(t, map[string]any{"replicaCount": 2}, true)
	require.Error(t, err, "the values schema must reject scaling the traffic-manager")
	require.ErrorContains(t, err, "replicaCount")

	err = renderCoreChart(t, map[string]any{"replicaCount": 2}, false)
	require.Error(t, err, "scaling must refuse to render even without the schema")
	require.ErrorContains(t, err, "replicaCount must be 1")
}

func TestQuicForwarderPodLabelsSchema(t *testing.T) {
	t.Run("accepts string-valued pod labels", func(t *testing.T) {
		err := renderCoreChart(t, map[string]any{
			"quicTunnel": map[string]any{
				"enabled": true,
				"forwarder": map[string]any{
					"podLabels": map[string]any{"example.com/mesh-injection": "enabled"},
				},
			},
		}, true)
		require.NoError(t, err)
	})

	t.Run("rejects non-object pod labels", func(t *testing.T) {
		err := renderCoreChart(t, map[string]any{
			"quicTunnel": map[string]any{
				"enabled":   true,
				"forwarder": map[string]any{"podLabels": "enabled"},
			},
		}, true)
		require.Error(t, err)
		require.ErrorContains(t, err, "podLabels")
	})

	t.Run("rejects non-string pod label values", func(t *testing.T) {
		err := renderCoreChart(t, map[string]any{
			"quicTunnel": map[string]any{
				"enabled": true,
				"forwarder": map[string]any{
					"podLabels": map[string]any{"example.com/mesh-injection": true},
				},
			},
		}, true)
		require.Error(t, err)
		require.ErrorContains(t, err, "podLabels")
	})

	t.Run("rejects unknown forwarder properties", func(t *testing.T) {
		err := renderCoreChart(t, map[string]any{
			"quicTunnel": map[string]any{
				"enabled":   true,
				"forwarder": map[string]any{"unknownProperty": "value"},
			},
		}, true)
		require.Error(t, err)
		require.ErrorContains(t, err, "unknownProperty")
	})
}
