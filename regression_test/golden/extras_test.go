package golden

import (
	"strings"
	"testing"
)

// TestExtraValuesReachManagerAndForwarder pins the contract that the
// top-level extraEnv/extraVolumes/extraVolumeMounts values render into both
// the traffic-manager and, when quicTunnel is enabled, the quic-forwarder
// workload (values.yaml documents them as applying to both).
func TestExtraValuesReachManagerAndForwarder(t *testing.T) {
	out := renderChart(t, map[string]any{
		"quicTunnel": map[string]any{"enabled": true},
		"extraEnv":   []map[string]any{{"name": "EXTRA_ENV_RTEST", "value": "x"}},
		"extraVolumes": []map[string]any{{
			"name":     "extra-vol-rtest",
			"hostPath": map[string]any{"path": "/rtest-extra", "type": "DirectoryOrCreate"},
		}},
		"extraVolumeMounts": []map[string]any{{
			"name":      "extra-vol-rtest",
			"mountPath": "/rtest-extra",
		}},
	})
	for _, tpl := range []string{deploymentTpl, quicFwdTpl} {
		doc := out[tpl]
		for _, want := range []string{"EXTRA_ENV_RTEST", "extra-vol-rtest", "/rtest-extra"} {
			if !strings.Contains(doc, want) {
				t.Errorf("%s: %q not rendered", tpl, want)
			}
		}
	}
}
