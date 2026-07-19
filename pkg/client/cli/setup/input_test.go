package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool { return &b }

func triPtr(t Tri) *Tri { return &t }

func scopePtr(s ScopeChoice) *ScopeChoice { return &s }

func TestLoadInputValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(path, []byte("nodeAgent:\n  enabled: true\n"), 0o644))
	values, err := LoadInputValues(path)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"nodeAgent": map[string]any{"enabled": true}}, values)

	_, err = LoadInputValues(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte(":not yaml"), 0o644))
	_, err = LoadInputValues(bad)
	require.Error(t, err)
}

func TestDerivePins(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
		want   Pins
	}{
		{
			name: "both disabled pins attach false",
			values: map[string]any{
				"agentInjector": map[string]any{"enabled": false},
				"nodeAgent":     map[string]any{"enabled": false},
			},
			want: Pins{Attach: boolPtr(false)},
		},
		{
			name: "node-agent only mode pins attach and replace false",
			values: map[string]any{
				"agentInjector": map[string]any{"enabled": false},
				"nodeAgent":     map[string]any{"enabled": true},
			},
			want: Pins{Attach: boolPtr(true), ReplaceDetermined: true},
		},
		{
			name: "node-agent with injector pins replace true",
			values: map[string]any{
				"agentInjector": map[string]any{"enabled": true},
				"nodeAgent":     map[string]any{"enabled": true},
			},
			want: Pins{Attach: boolPtr(true), ReplaceDetermined: true, Replace: true},
		},
		{
			name: "injector-only mode makes the replace question moot",
			values: map[string]any{
				"agentInjector": map[string]any{"enabled": true},
				"nodeAgent":     map[string]any{"enabled": false},
			},
			want: Pins{Attach: boolPtr(true), ReplaceDetermined: true},
		},
		{
			name:   "node-agent enabled alone pins attach but not replace",
			values: map[string]any{"nodeAgent": map[string]any{"enabled": true}},
			want:   Pins{Attach: boolPtr(true)},
		},
		{
			name:   "node-agent disabled alone pins nothing",
			values: map[string]any{"nodeAgent": map[string]any{"enabled": false}},
			want:   Pins{},
		},
		{
			name:   "injector enabled alone pins attach with replace moot",
			values: map[string]any{"agentInjector": map[string]any{"enabled": true}},
			want:   Pins{Attach: boolPtr(true), ReplaceDetermined: true},
		},
		{
			name:   "injector disabled alone pins nothing",
			values: map[string]any{"agentInjector": map[string]any{"enabled": false}},
			want:   Pins{},
		},
		{
			name:   "quic enabled pins on",
			values: map[string]any{"quicTunnel": map[string]any{"enabled": true}},
			want:   Pins{Quic: triPtr(TriOn)},
		},
		{
			name:   "quic disabled pins off",
			values: map[string]any{"quicTunnel": map[string]any{"enabled": false}},
			want:   Pins{Quic: triPtr(TriOff)},
		},
		{
			name:   "namespaces pin the scope",
			values: map[string]any{"namespaces": []any{"foo", "bar"}},
			want:   Pins{Scope: scopePtr(ScopeNamespaces), ManagedNamespaces: []string{"foo", "bar"}},
		},
		{
			name:   "namespaceSelector pins the selector scope",
			values: map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"team": "dev"}}},
			want:   Pins{Scope: scopePtr(ScopeSelector), SelectorLabels: map[string]string{"team": "dev"}},
		},
		{
			name: "client mapped-namespaces default pins the mapped scope",
			values: map[string]any{
				"client": map[string]any{"cluster": map[string]any{"mappedNamespaces": []any{"foo", "bar"}}},
			},
			want: Pins{Scope: scopePtr(ScopeMapped), ManagedNamespaces: []string{"foo", "bar"}},
		},
		{
			name: "namespaces win over the mapped-namespaces default",
			values: map[string]any{
				"namespaces": []any{"foo"},
				"client":     map[string]any{"cluster": map[string]any{"mappedNamespaces": []any{"bar"}}},
			},
			want: Pins{Scope: scopePtr(ScopeNamespaces), ManagedNamespaces: []string{"foo"}},
		},
		{
			name: "allowConflictingSubnets pins the conflicts question",
			values: map[string]any{
				"client": map[string]any{"routing": map[string]any{"allowConflictingSubnets": []any{"10.244.0.0/16"}}},
			},
			want: Pins{AllowConflictsDetermined: true},
		},
		{
			name:   "clientRbac.create true pins the question yes",
			values: map[string]any{"clientRbac": map[string]any{"create": true}},
			want:   Pins{ClientRbacDetermined: true, ClientRbac: true},
		},
		{
			name:   "clientRbac.create false pins the question no",
			values: map[string]any{"clientRbac": map[string]any{"create": false}},
			want:   Pins{ClientRbacDetermined: true},
		},
		{
			name:   "unrelated keys pin nothing",
			values: map[string]any{"image": map[string]any{"registry": "ghcr.io/telepresenceio"}},
			want:   Pins{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DerivePins(tt.values))
		})
	}
}

func TestPinsApplyTo(t *testing.T) {
	t.Run("pins fill unset answers", func(t *testing.T) {
		pins := Pins{
			Attach:            boolPtr(false),
			Quic:              triPtr(TriOff),
			Scope:             scopePtr(ScopeNamespaces),
			ManagedNamespaces: []string{"foo"},
		}
		a := Answers{Attach: true, Quic: TriAuto}
		pre := Preset{}
		pins.ApplyTo(&a, &pre)
		assert.False(t, a.Attach)
		assert.True(t, pre.Attach)
		assert.Equal(t, TriOff, a.Quic)
		assert.Equal(t, ScopeNamespaces, a.Scope)
		assert.True(t, pre.Scope)
		assert.Equal(t, []string{"foo"}, a.ManagedNamespaces)
	})
	t.Run("a pinned conflicts value skips the question and lets reconcile guard it", func(t *testing.T) {
		pins := Pins{AllowConflictsDetermined: true}
		a := Answers{}
		pre := Preset{}
		pins.ApplyTo(&a, &pre)
		assert.True(t, a.AllowConflicts)
		assert.True(t, pre.AllowConflicts)
	})
	t.Run("flag presets win over pins", func(t *testing.T) {
		pins := Pins{Attach: boolPtr(false), Quic: triPtr(TriOff)}
		a := Answers{Attach: true, Quic: TriOn}
		pre := Preset{Attach: true}
		pins.ApplyTo(&a, &pre)
		assert.True(t, a.Attach)
		assert.Equal(t, TriOn, a.Quic)
	})
	t.Run("a pinned clientRbac.create skips the question", func(t *testing.T) {
		pins := Pins{ClientRbacDetermined: true, ClientRbac: true}
		a := Answers{}
		pre := Preset{}
		pins.ApplyTo(&a, &pre)
		assert.True(t, a.ClientRbac)
		assert.True(t, pre.ClientRbac)
	})
	t.Run("a flag-preset clientRbac wins over a pin", func(t *testing.T) {
		pins := Pins{ClientRbacDetermined: true, ClientRbac: true}
		a := Answers{ClientRbac: false}
		pre := Preset{ClientRbac: true}
		pins.ApplyTo(&a, &pre)
		assert.False(t, a.ClientRbac)
	})
}

func TestReconcileWithInput(t *testing.T) {
	rec := map[string]any{
		"agentInjector": map[string]any{"enabled": false},
		"nodeAgent":     map[string]any{"enabled": true},
		"quicTunnel":    map[string]any{"enabled": true},
	}
	t.Run("absent keys adopt the recommendation, extras pass through", func(t *testing.T) {
		input := map[string]any{
			"image": map[string]any{"registry": "ghcr.io/other", "tag": "2.30.0"},
			"grpc":  map[string]any{"maxReceiveSize": "8Mi"},
		}
		final, notes, err := ReconcileWithInput(rec, input, nil)
		require.NoError(t, err)
		assert.Empty(t, notes)
		assert.Equal(t, true, val(t, final, "nodeAgent", "enabled"))
		assert.Equal(t, "ghcr.io/other", val(t, final, "image", "registry"))
		assert.Equal(t, "2.30.0", val(t, final, "image", "tag"))
		assert.Equal(t, "8Mi", val(t, final, "grpc", "maxReceiveSize"))
	})
	t.Run("equal keys need nothing", func(t *testing.T) {
		input := map[string]any{"quicTunnel": map[string]any{"enabled": true}}
		final, notes, err := ReconcileWithInput(rec, input, nil)
		require.NoError(t, err)
		assert.Empty(t, notes)
		assert.Equal(t, true, val(t, final, "quicTunnel", "enabled"))
	})
	t.Run("conflict kept by consult", func(t *testing.T) {
		input := map[string]any{"quicTunnel": map[string]any{"enabled": false}}
		var consulted string
		final, notes, err := ReconcileWithInput(rec, input, func(key string, inputVal, recVal any) (bool, error) {
			consulted = key
			assert.Equal(t, false, inputVal)
			assert.Equal(t, true, recVal)
			return true, nil
		})
		require.NoError(t, err)
		assert.Equal(t, "quicTunnel.enabled", consulted)
		assert.Empty(t, notes)
		assert.Equal(t, false, val(t, final, "quicTunnel", "enabled"))
	})
	t.Run("conflict overridden by consult", func(t *testing.T) {
		input := map[string]any{"quicTunnel": map[string]any{"enabled": false}}
		final, _, err := ReconcileWithInput(rec, input, func(string, any, any) (bool, error) {
			return false, nil
		})
		require.NoError(t, err)
		assert.Equal(t, true, val(t, final, "quicTunnel", "enabled"))
	})
	t.Run("nil consult keeps the input and warns", func(t *testing.T) {
		input := map[string]any{"quicTunnel": map[string]any{"enabled": false}}
		final, notes, err := ReconcileWithInput(rec, input, nil)
		require.NoError(t, err)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "quicTunnel.enabled")
		assert.Equal(t, false, val(t, final, "quicTunnel", "enabled"))
	})
	t.Run("input is not mutated", func(t *testing.T) {
		input := map[string]any{"image": map[string]any{"registry": "ghcr.io/other"}}
		_, _, err := ReconcileWithInput(rec, input, nil)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"image": map[string]any{"registry": "ghcr.io/other"}}, input)
	})
}

func TestValidateValues(t *testing.T) {
	t.Run("webhook denied", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.Webhook.CanCreate = Finding{Verdict: VerdictNo} })
		err := ValidateValues(facts, map[string]any{"agentInjector": map[string]any{"enabled": true}}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutatingwebhookconfigurations")

		assert.NoError(t, ValidateValues(facts, map[string]any{"agentInjector": map[string]any{"enabled": false}}, true))
	})
	t.Run("webhook denied is a hard error even without --apply", func(t *testing.T) {
		// Hard incompatibilities are not downgraded: only the missing
		// install-privilege check softens for validation-only runs.
		facts := recFacts(func(f *ClusterFacts) { f.Webhook.CanCreate = Finding{Verdict: VerdictNo} })
		err := ValidateValues(facts, map[string]any{"agentInjector": map[string]any{"enabled": true}}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutatingwebhookconfigurations")
	})
	t.Run("cluster-wide denied", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
			f.Privileges.Missing = []string{"create clusterroles.rbac.authorization.k8s.io"}
		})
		err := ValidateValues(facts, map[string]any{}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cluster-wide")

		assert.NoError(t, ValidateValues(facts, map[string]any{"namespaces": []any{"foo"}}, true))
	})
	t.Run("cluster-wide denial does not abort a validation-only run", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
			f.Privileges.Missing = []string{"create clusterroles.rbac.authorization.k8s.io"}
		})
		assert.NoError(t, ValidateValues(facts, map[string]any{}, false))
	})
	t.Run("quic with multiple replicas", func(t *testing.T) {
		err := ValidateValues(recFacts(), map[string]any{
			"quicTunnel":   map[string]any{"enabled": true},
			"replicaCount": float64(2),
		}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "replicaCount")
	})
	t.Run("clean pass", func(t *testing.T) {
		assert.NoError(t, ValidateValues(recFacts(), map[string]any{
			"agentInjector": map[string]any{"enabled": false},
			"nodeAgent":     map[string]any{"enabled": true},
			"quicTunnel":    map[string]any{"enabled": true},
		}, true))
	})
}

// TestRecommendWithInput_RoundTrip covers the losslessness contract: an input
// with keys the engine has no opinion about survives into the final values.
func TestRecommendWithInput_RoundTrip(t *testing.T) {
	input := map[string]any{
		"image":         map[string]any{"registry": "ghcr.io/other", "tag": "2.30.0"},
		"logLevel":      "debug",
		"agentInjector": map[string]any{"enabled": false},
		"nodeAgent":     map[string]any{"enabled": true},
		"quicTunnel":    map[string]any{"enabled": true},
	}
	answers := recAnswers()
	pins := DerivePins(input)
	pre := Preset{}
	pins.ApplyTo(answers, &pre)

	p, err := RecommendWithInput(recFacts(), answers, input, nil, true)
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/other", val(t, p.Values, "image", "registry"))
	assert.Equal(t, "2.30.0", val(t, p.Values, "image", "tag"))
	assert.Equal(t, "debug", val(t, p.Values, "logLevel"))
	assert.Equal(t, true, val(t, p.Values, "nodeAgent", "enabled"))
	assert.Equal(t, false, val(t, p.Values, "agentInjector", "enabled"))
	for _, n := range p.Notes {
		assert.NotEqual(t, NoteWarning, n.Level, "unexpected warning: %s", n.Text)
	}
}
