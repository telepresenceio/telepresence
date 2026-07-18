package setup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func recFacts(mods ...func(*ClusterFacts)) *ClusterFacts {
	f := &ClusterFacts{
		ManagerNamespace: "ambassador",
		NamespaceExists:  true,
		Privileges: PrivilegeFacts{
			ClusterWide: Finding{Verdict: VerdictYes},
			Namespaced:  Finding{Verdict: VerdictYes},
		},
		Quic: QuicFacts{
			Provider:     "gke",
			LoadBalancer: Finding{Verdict: VerdictYes},
			NodePort:     Finding{Verdict: VerdictProbable},
		},
		NodeAgent: NodeAgentFacts{
			Viable:     Finding{Verdict: VerdictYes},
			LinuxNodes: 1,
			TotalNodes: 1,
			Runtimes:   []string{"containerd"},
		},
		Webhook:    WebhookFacts{CanCreate: Finding{Verdict: VerdictYes}},
		Namespaces: NamespaceFacts{Count: 5},
	}
	for _, m := range mods {
		m(f)
	}
	return f
}

func recAnswers(mods ...func(*Answers)) *Answers {
	a := &Answers{Attach: true, Scope: ScopeAll, Quic: TriAuto, NodeAgent: TriAuto}
	for _, m := range mods {
		m(a)
	}
	return a
}

func val(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		require.True(t, ok, "no map at %v", path)
		cur, ok = mm[p]
		require.True(t, ok, "missing key at %v", path)
	}
	return cur
}

func olderVersion() string {
	return "0.0.0-0"
}

func newerVersion() string {
	v := version.Structured
	v.Major++
	return v.String()
}

func TestRecommend_DecisionTable(t *testing.T) {
	tests := []struct {
		name          string
		facts         *ClusterFacts
		answers       *Answers
		wantNodeAgent bool
		wantInjector  bool
	}{
		{
			name:    "no attach disables all agent machinery",
			facts:   recFacts(),
			answers: recAnswers(func(a *Answers) { a.Attach = false }),
		},
		{
			name:          "attach with viable node-agent and no replace",
			facts:         recFacts(),
			answers:       recAnswers(),
			wantNodeAgent: true,
		},
		{
			name:          "attach with viable node-agent and replace keeps the webhook",
			facts:         recFacts(),
			answers:       recAnswers(func(a *Answers) { a.Replace = true }),
			wantNodeAgent: true,
			wantInjector:  true,
		},
		{
			name:         "attach without viable node-agent requires the webhook",
			facts:        recFacts(func(f *ClusterFacts) { f.NodeAgent.Viable = Finding{Verdict: VerdictNo} }),
			answers:      recAnswers(),
			wantInjector: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Recommend(tt.facts, tt.answers)
			require.NoError(t, err)
			assert.Equal(t, tt.wantNodeAgent, val(t, p.Values, "nodeAgent", "enabled"))
			assert.Equal(t, tt.wantInjector, val(t, p.Values, "agentInjector", "enabled"))
			assert.Equal(t, ActionInstall, p.Action)
		})
	}
}

func TestRecommend_WebhookRequiredButDenied(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.NodeAgent.Viable = Finding{Verdict: VerdictNo}
		f.Webhook.CanCreate = Finding{Verdict: VerdictNo}
	})
	_, err := Recommend(facts, recAnswers())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutatingwebhookconfigurations")
}

func TestRecommend_ClusterWidePrivilegesMissing(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
		f.Privileges.Missing = []string{"create clusterroles.rbac.authorization.k8s.io"}
	})
	_, err := Recommend(facts, recAnswers())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create clusterroles.rbac.authorization.k8s.io")
	assert.Contains(t, err.Error(), "--scope=namespaces")
}

func TestRecommend_QuicAuto(t *testing.T) {
	t.Run("loadBalancer yes", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers())
		require.NoError(t, err)
		assert.Equal(t, true, val(t, p.Values, "quicTunnel", "enabled"))
		quic := val(t, p.Values, "quicTunnel").(map[string]any)
		assert.NotContains(t, quic, "service")
	})
	t.Run("nodePort with cluster-wide scope", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Quic.LoadBalancer = Finding{Verdict: VerdictNo}
		})
		p, err := Recommend(facts, recAnswers())
		require.NoError(t, err)
		assert.Equal(t, true, val(t, p.Values, "quicTunnel", "enabled"))
		assert.Equal(t, "NodePort", val(t, p.Values, "quicTunnel", "service", "type"))
	})
	t.Run("disabled when namespace-scoped without loadBalancer", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Quic.LoadBalancer = Finding{Verdict: VerdictNo}
		})
		answers := recAnswers(func(a *Answers) {
			a.Scope = ScopeNamespaces
			a.ManagedNamespaces = []string{"ambassador"}
		})
		p, err := Recommend(facts, answers)
		require.NoError(t, err)
		assert.Equal(t, false, val(t, p.Values, "quicTunnel", "enabled"))
		require.NotEmpty(t, p.Notes)
		assert.Contains(t, notesText(p), "QUIC disabled")
	})
}

func TestRecommend_QuicSuppressedByReplicaCount(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true,
			Version:   version.Structured.String(),
			Namespace: "ambassador",
			Values:    map[string]any{"replicaCount": float64(2)},
		}
	})
	p, err := Recommend(facts, recAnswers())
	require.NoError(t, err)
	assert.Equal(t, false, val(t, p.Values, "quicTunnel", "enabled"))
	assert.Contains(t, notesText(p), "replicaCount")
}

func TestRecommend_UpgradeMerge(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true,
			Version:   olderVersion(),
			Namespace: "ambassador",
			Values: map[string]any{
				"logLevel":      "debug",
				"agentInjector": map[string]any{"enabled": true},
			},
		}
	})
	answers := recAnswers(func(a *Answers) { a.UpgradeManager = true })
	p, err := Recommend(facts, answers)
	require.NoError(t, err)

	assert.Equal(t, ActionUpgrade, p.Action)
	assert.Equal(t, facts.Release.Values, p.BaseValues)
	assert.Equal(t, "debug", val(t, p.Values, "logLevel"))
	assert.Equal(t, false, val(t, p.Values, "agentInjector", "enabled"))
	assert.Contains(t, p.ChangedKeys, "agentInjector.enabled")
	assert.Contains(t, p.ChangedKeys, "nodeAgent.enabled")
	assert.NotContains(t, p.ChangedKeys, "logLevel")
}

func TestRecommend_KeepVersionWhenUpgradeDeclined(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true,
			Version:   olderVersion(),
			Namespace: "ambassador",
		}
	})
	p, err := Recommend(facts, recAnswers())
	require.NoError(t, err)
	assert.Equal(t, ActionUpgrade, p.Action)
	assert.Contains(t, notesText(p), "version "+olderVersion()+" is kept")
}

func TestRecommend_NewerRelease(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true,
			Version:   newerVersion(),
			Namespace: "ambassador",
		}
	})
	p, err := Recommend(facts, recAnswers())
	require.NoError(t, err)
	assert.Equal(t, ActionNone, p.Action)
	assert.Empty(t, p.Values)
	assert.Contains(t, notesText(p), "newer than this client")
}

func TestRecommend_MappedScope(t *testing.T) {
	answers := recAnswers(func(a *Answers) {
		a.Scope = ScopeMapped
		a.ManagedNamespaces = []string{"foo", "bar"}
	})
	p, err := Recommend(recFacts(), answers)
	require.NoError(t, err)
	assert.Equal(t, []string{"foo", "bar"}, p.MappedNamespaces)
	assert.NotContains(t, p.Values, "namespaces")
}

func TestRecommend_SelectorScope(t *testing.T) {
	answers := recAnswers(func(a *Answers) {
		a.Scope = ScopeSelector
		a.SelectorLabels = map[string]string{"team": "dev"}
	})
	p, err := Recommend(recFacts(), answers)
	require.NoError(t, err)
	assert.Equal(t, "dev", val(t, p.Values, "namespaceSelector", "matchLabels", "team"))
	assert.Contains(t, notesText(p), "maxNamespaceSpecificWatchers")
}

func notesText(p *Proposal) string {
	var sb strings.Builder
	for _, n := range p.Notes {
		sb.WriteString(n.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}
