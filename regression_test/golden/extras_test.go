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

// TestLogStreamingEnv pins the LOG_STREAM_* env vars the traffic-manager
// reads to bound the StreamLogs RPC (`telepresence gather-logs`): a custom
// logStreaming block overrides every value, and an absent block (values.yaml
// isn't touched by this render) falls back to the chart's documented
// defaults.
func TestLogStreamingEnv(t *testing.T) {
	t.Run("custom", func(t *testing.T) {
		out := renderChart(t, map[string]any{
			"logStreaming": map[string]any{
				"chunkSize":      "128Ki",
				"podConcurrency": 8,
				"podByteLimit":   "20Mi",
				"deadline":       "2m",
			},
		})
		env := parseEnv(out[deploymentTpl])
		want := map[string]string{
			"LOG_STREAM_CHUNK_SIZE":      "128Ki",
			"LOG_STREAM_POD_CONCURRENCY": "8",
			"LOG_STREAM_POD_BYTE_LIMIT":  "20Mi",
			"LOG_STREAM_DEADLINE":        "2m",
		}
		for name, wantVal := range want {
			if v := env[name]; v != wantVal {
				t.Errorf("%s = %q, want %q", name, v, wantVal)
			}
		}
	})

	t.Run("absent", func(t *testing.T) {
		out := renderChart(t, map[string]any{})
		env := parseEnv(out[deploymentTpl])
		want := map[string]string{
			"LOG_STREAM_CHUNK_SIZE":      "64Ki",
			"LOG_STREAM_POD_CONCURRENCY": "4",
			"LOG_STREAM_POD_BYTE_LIMIT":  "10Mi",
			"LOG_STREAM_DEADLINE":        "5m",
		}
		for name, wantVal := range want {
			if v := env[name]; v != wantVal {
				t.Errorf("%s = %q, want %q", name, v, wantVal)
			}
		}
	})
}
