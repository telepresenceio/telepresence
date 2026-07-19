package setup

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runInterview(t *testing.T, facts *ClusterFacts, input string, answers Answers, preset Preset, nonInteractive bool) (*Answers, string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	iv := &Interviewer{
		Facts:          facts,
		In:             strings.NewReader(input),
		Out:            out,
		NonInteractive: nonInteractive,
		Answers:        answers,
		Preset:         preset,
	}
	a, err := iv.Interview(context.Background())
	return a, out.String(), err
}

func TestInterview_Defaults(t *testing.T) {
	a, out, err := runInterview(t, recFacts(), "\n\n\n", Answers{}, Preset{}, false)
	require.NoError(t, err)
	assert.True(t, a.Attach)
	assert.False(t, a.Replace)
	assert.Equal(t, ScopeAll, a.Scope)
	assert.Equal(t, TriAuto, a.Quic)
	assert.Equal(t, TriAuto, a.NodeAgent)
	assert.Contains(t, out, "attach to workloads")
	assert.Contains(t, out, "replace command")
	assert.Contains(t, out, "Choose 1-4 [1]")
}

func TestInterview_ReplaceGating(t *testing.T) {
	t.Run("skipped when node-agent is not viable", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.NodeAgent.Viable = Finding{Verdict: VerdictNo} })
		a, out, err := runInterview(t, facts, "\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.False(t, a.Replace)
		assert.NotContains(t, out, "replace command")
	})
	t.Run("skipped when node-agent is forced off", func(t *testing.T) {
		a, out, err := runInterview(t, recFacts(), "\n\n", Answers{NodeAgent: TriOff}, Preset{}, false)
		require.NoError(t, err)
		assert.False(t, a.Replace)
		assert.NotContains(t, out, "replace command")
	})
}

func TestInterview_UpgradeGating(t *testing.T) {
	t.Run("older release is asked about", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{Installed: true, Version: olderVersion(), Namespace: "ambassador"}
		})
		a, out, err := runInterview(t, facts, "\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.True(t, a.UpgradeManager)
		assert.Contains(t, out, "Upgrade the traffic-manager?")
	})
	t.Run("newer release is advisory only", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{Installed: true, Version: newerVersion(), Namespace: "ambassador"}
		})
		a, out, err := runInterview(t, facts, "\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.False(t, a.UpgradeManager)
		assert.NotContains(t, out, "Upgrade the traffic-manager?")
		assert.Contains(t, out, "newer than this client")
	})
}

func TestInterview_ScopeNamespaces(t *testing.T) {
	a, out, err := runInterview(t, recFacts(), "\n\n2\nfoo,bar\n", Answers{}, Preset{}, false)
	require.NoError(t, err)
	assert.Equal(t, ScopeNamespaces, a.Scope)
	assert.Equal(t, []string{"foo", "bar", "ambassador"}, a.ManagedNamespaces)
	assert.Contains(t, out, "Namespaces to manage")
}

func TestInterview_InvalidInputReprompts(t *testing.T) {
	_, out, err := runInterview(t, recFacts(), "x\nx\nx\n", Answers{}, Preset{}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many invalid answers")
	assert.Contains(t, out, "Please answer y or n.")
}

func TestInterview_NonInteractive(t *testing.T) {
	t.Run("defaults without prompting", func(t *testing.T) {
		a, out, err := runInterview(t, recFacts(), "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.True(t, a.Attach)
		assert.False(t, a.Replace)
		assert.Equal(t, ScopeAll, a.Scope)
		assert.Empty(t, out)
	})
	t.Run("missing cluster-wide privileges default to a namespace scope", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
		})
		a, _, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.Equal(t, ScopeNamespaces, a.Scope)
		assert.Equal(t, []string{"ambassador"}, a.ManagedNamespaces)
	})
	t.Run("mapped scope requires a namespace list", func(t *testing.T) {
		_, _, err := runInterview(t, recFacts(), "", Answers{Scope: ScopeMapped}, Preset{Scope: true}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--managed-namespaces")
	})
}

func TestInterview_PresetSkipsPrompts(t *testing.T) {
	a, out, err := runInterview(t, recFacts(), "\n", Answers{Attach: false}, Preset{Attach: true}, false)
	require.NoError(t, err)
	assert.False(t, a.Attach)
	assert.NotContains(t, out, "attach to workloads")
	assert.NotContains(t, out, "replace command")
	assert.Contains(t, out, "Choose 1-4")
	assert.Equal(t, ScopeAll, a.Scope)
}

// TestInterview_InputPinsSkipQuestions verifies that answers pinned by an
// input values document are not asked, while unpinned questions still are.
func TestInterview_InputPinsSkipQuestions(t *testing.T) {
	t.Run("pinned attach and scope are not asked", func(t *testing.T) {
		pins := DerivePins(map[string]any{
			"agentInjector": map[string]any{"enabled": false},
			"nodeAgent":     map[string]any{"enabled": true},
			"quicTunnel":    map[string]any{"enabled": false},
			"namespaces":    []any{"foo"},
		})
		a := Answers{Attach: true, Quic: TriAuto}
		pre := Preset{}
		pins.ApplyTo(&a, &pre)

		got, out, err := runInterview(t, recFacts(), "", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "attach to workloads")
		assert.NotContains(t, out, "replace command")
		assert.NotContains(t, out, "Choose 1-4")
		assert.True(t, got.Attach)
		assert.False(t, got.Replace)
		assert.Equal(t, TriOff, got.Quic)
		assert.Equal(t, ScopeNamespaces, got.Scope)
		assert.Equal(t, []string{"foo", "ambassador"}, got.ManagedNamespaces)
	})
	t.Run("VPN-only input pins attach=false and skips the question", func(t *testing.T) {
		pins := DerivePins(map[string]any{
			"agentInjector": map[string]any{"enabled": false},
			"nodeAgent":     map[string]any{"enabled": false},
		})
		a := Answers{Attach: true, Quic: TriAuto}
		pre := Preset{}
		pins.ApplyTo(&a, &pre)

		got, out, err := runInterview(t, recFacts(), "\n", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "attach to workloads")
		assert.NotContains(t, out, "replace command")
		assert.Contains(t, out, "Choose 1-4")
		assert.False(t, got.Attach)
	})
	t.Run("unpinned questions are still asked", func(t *testing.T) {
		pins := DerivePins(map[string]any{"nodeAgent": map[string]any{"enabled": true}})
		a := Answers{Quic: TriAuto}
		pre := Preset{}
		pins.ApplyTo(&a, &pre)

		got, out, err := runInterview(t, recFacts(), "\n\n", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "attach to workloads")
		assert.Contains(t, out, "replace command")
		assert.Contains(t, out, "Choose 1-4")
		assert.True(t, got.Attach)
	})
}

func TestInterview_AllowConflicts(t *testing.T) {
	conflictFacts := recFacts(func(f *ClusterFacts) {
		f.Routing = RoutingFacts{
			Summary: Finding{Verdict: VerdictNo},
			Conflicts: []RoutingConflict{
				{ClusterSubnet: "10.244.0.0/16", Source: sourcePodCIDR, LocalRoute: "10.0.0.0/8", Interface: "tun0"},
			},
		}
	})
	t.Run("asked only when conflicts exist, default no", func(t *testing.T) {
		a, out, err := runInterview(t, conflictFacts, "\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Local routes overlap the cluster's subnets (10.244.0.0/16).")
		assert.False(t, a.AllowConflicts)
	})
	t.Run("yes accepts the conflicts", func(t *testing.T) {
		a, _, err := runInterview(t, conflictFacts, "\n\n\ny\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.True(t, a.AllowConflicts)
	})
	t.Run("not asked without conflicts", func(t *testing.T) {
		_, out, err := runInterview(t, recFacts(), "\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "Local routes overlap")
	})
	t.Run("non-interactive defaults to no", func(t *testing.T) {
		a, out, err := runInterview(t, conflictFacts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.False(t, a.AllowConflicts)
		assert.Empty(t, out)
	})
	t.Run("preset skips the question", func(t *testing.T) {
		a, out, err := runInterview(t, conflictFacts, "\n\n\n", Answers{AllowConflicts: true}, Preset{AllowConflicts: true}, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "Local routes overlap")
		assert.True(t, a.AllowConflicts)
	})
}

func TestInterview_ScopeRecommendation(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
	})
	a, out, err := runInterview(t, facts, "\n\n\nfoo\n", Answers{}, Preset{}, false)
	require.NoError(t, err)
	assert.Contains(t, out, "cluster-wide install looks impossible")
	assert.Contains(t, out, "Choose 1-4 [2]")
	assert.Equal(t, ScopeNamespaces, a.Scope)
	assert.Equal(t, []string{"foo", "ambassador"}, a.ManagedNamespaces)
}
