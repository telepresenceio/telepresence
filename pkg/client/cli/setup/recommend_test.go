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
	a := &Answers{Attach: true, ManagedScope: ManagedScopeAll, Quic: TriAuto, NodeAgent: TriAuto}
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
	assert.Contains(t, err.Error(), "--managed-scope=namespaces")
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
			a.ManagedScope = ManagedScopeNamespaces
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
	assert.Empty(t, p.Version)
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
	assert.Equal(t, olderVersion(), p.Version)
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

func TestRecommend_MappedNamespaces(t *testing.T) {
	answers := recAnswers(func(a *Answers) {
		a.MappedNamespaces = []string{"foo", "bar"}
	})
	p, err := Recommend(recFacts(), answers)
	require.NoError(t, err)
	assert.Equal(t, []any{"foo", "bar"}, val(t, p.Values, "client", "cluster", "mappedNamespaces"))
	assert.NotContains(t, p.Values, "namespaces")
	assert.Contains(t, notesText(p), "mapped-namespaces default")

	t.Run("no mapped namespaces sets no client value regardless of managed scope", func(t *testing.T) {
		for _, scope := range []ManagedScope{ManagedScopeAll, ManagedScopeNamespaces, ManagedScopeSelector} {
			answers := recAnswers(func(a *Answers) {
				a.ManagedScope = scope
				a.ManagedNamespaces = []string{"foo"}
				a.SelectorLabels = map[string]string{"team": "dev"}
			})
			p, err := Recommend(recFacts(), answers)
			require.NoError(t, err)
			assert.NotContains(t, p.Values, "client", "scope %s", scope)
		}
	})

	// This is the case the old three-way ScopeChoice model could not
	// express: a namespace-limited managed scope and an independent
	// mapped-namespaces client default, both rendered in the same proposal.
	t.Run("managed scope namespaces combined with mapped namespaces renders both", func(t *testing.T) {
		answers := recAnswers(func(a *Answers) {
			a.ManagedScope = ManagedScopeNamespaces
			a.ManagedNamespaces = []string{"foo", "bar"}
			a.MappedNamespaces = []string{"baz"}
		})
		p, err := Recommend(recFacts(), answers)
		require.NoError(t, err)
		assert.Equal(t, []any{"foo", "bar"}, val(t, p.Values, "namespaces"))
		assert.Equal(t, []any{"baz"}, val(t, p.Values, "client", "cluster", "mappedNamespaces"))
	})
}

func TestRecommend_SelectorScope(t *testing.T) {
	answers := recAnswers(func(a *Answers) {
		a.ManagedScope = ManagedScopeSelector
		a.SelectorLabels = map[string]string{"team": "dev"}
	})
	p, err := Recommend(recFacts(), answers)
	require.NoError(t, err)
	assert.Equal(t, "dev", val(t, p.Values, "namespaceSelector", "matchLabels", "team"))
	assert.Contains(t, notesText(p), "maxNamespaceSpecificWatchers")
}

func TestRecommend_RoutingConflicts(t *testing.T) {
	conflictFacts := func() *ClusterFacts {
		return recFacts(func(f *ClusterFacts) {
			f.Routing = RoutingFacts{
				Summary: Finding{Verdict: VerdictNo},
				Conflicts: []RoutingConflict{
					{ClusterSubnet: "10.244.0.0/16", Source: sourcePodCIDR, LocalRoute: "10.0.0.0/8", Interface: "tun0"},
					{ClusterSubnet: "10.96.0.0/16", Source: sourceServiceCIDR, LocalRoute: "10.0.0.0/8", Interface: "tun0"},
				},
			}
		})
	}

	t.Run("accepted conflicts become a cluster-wide value", func(t *testing.T) {
		p, err := Recommend(conflictFacts(), recAnswers(func(a *Answers) { a.AllowConflicts = true }))
		require.NoError(t, err)
		assert.Equal(t, []any{"10.244.0.0/16", "10.96.0.0/16"}, val(t, p.Values, "client", "routing", "allowConflictingSubnets"))
		assert.NotContains(t, notesText(p), "--vnat")
	})
	t.Run("declined conflicts leave no value and warn with both remedies", func(t *testing.T) {
		p, err := Recommend(conflictFacts(), recAnswers())
		require.NoError(t, err)
		assert.NotContains(t, p.Values, "client")
		text := notesText(p)
		assert.Contains(t, text, "10.244.0.0/16")
		assert.Contains(t, text, "client.routing.allowConflictingSubnets")
		assert.Contains(t, text, "--vnat")
	})
	t.Run("no conflicts set nothing", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) { a.AllowConflicts = true }))
		require.NoError(t, err)
		assert.NotContains(t, p.Values, "client")
	})
	t.Run("mapped namespaces and accepted conflicts share the client value", func(t *testing.T) {
		answers := recAnswers(func(a *Answers) {
			a.MappedNamespaces = []string{"foo"}
			a.AllowConflicts = true
		})
		p, err := Recommend(conflictFacts(), answers)
		require.NoError(t, err)
		assert.Equal(t, []any{"foo"}, val(t, p.Values, "client", "cluster", "mappedNamespaces"))
		assert.Equal(t, []any{"10.244.0.0/16", "10.96.0.0/16"}, val(t, p.Values, "client", "routing", "allowConflictingSubnets"))
	})
}

// TestRecommend_ClientRbacPassthrough proves that client RBAC is now purely
// an --input concern: the engine has no opinion about clientRbac.* at all,
// so an input file's clientRbac.create/subjects/namespaces round-trip
// unchanged into the proposal via ReconcileWithInput's deep clone.
func TestRecommend_ClientRbacPassthrough(t *testing.T) {
	t.Run("no input sets no clientRbac value", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers())
		require.NoError(t, err)
		assert.NotContains(t, p.Values, "clientRbac")
	})
	t.Run("an input's clientRbac block round-trips unchanged", func(t *testing.T) {
		input := map[string]any{
			"clientRbac": map[string]any{
				"create":     true,
				"namespaces": []any{"foo", "bar"},
				"subjects": []any{
					map[string]any{"kind": "User", "name": "alice", "apiGroup": "rbac.authorization.k8s.io"},
					map[string]any{"kind": "ServiceAccount", "name": "sa", "namespace": "ns"},
				},
			},
		}
		p, err := RecommendWithInput(recFacts(), recAnswers(), input, nil, true)
		require.NoError(t, err)
		assert.Equal(t, input["clientRbac"], val(t, p.Values, "clientRbac"))
		for _, n := range p.Notes {
			assert.NotEqual(t, NoteWarning, n.Level, "unexpected warning: %s", n.Text)
		}
	})
}

// TestRecommendWithInput_ValidationDoesNotAbortOnPrivilegeDenial covers the
// non-admin handoff: a validation-only run (applying=false) still computes
// and returns the proposal on a privilege denial, downgrading the error to a
// warning plus handoff instructions; an --apply run keeps the hard error.
func TestRecommendWithInput_ValidationDoesNotAbortOnPrivilegeDenial(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
		f.Privileges.Missing = []string{"create clusterroles.rbac.authorization.k8s.io"}
	})

	t.Run("validation mode warns and hands off instead of aborting", func(t *testing.T) {
		p, err := RecommendWithInput(facts, recAnswers(), nil, nil, false)
		require.NoError(t, err)
		require.NotNil(t, p)
		text := notesText(p)
		assert.Contains(t, text, "create clusterroles.rbac.authorization.k8s.io")
		assert.Contains(t, text, "--input FILE --apply")
		assert.Contains(t, text, "missing privileges listed above")
	})

	t.Run("apply mode still errors", func(t *testing.T) {
		_, err := RecommendWithInput(facts, recAnswers(), nil, nil, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create clusterroles.rbac.authorization.k8s.io")
	})
}

func TestX509AuthEnabled(t *testing.T) {
	sec := func(mode string, x509 map[string]any) map[string]any {
		auth := map[string]any{"mode": mode}
		if x509 != nil {
			auth["x509"] = x509
		}
		return map[string]any{"security": map[string]any{"authentication": auth}}
	}
	tests := []struct {
		name   string
		values map[string]any
		want   bool
	}{
		{"no values", nil, false},
		{"no security block", map[string]any{}, false},
		{"permissive mode", sec("permissive", nil), false},
		{"enforcing, x509 block absent", sec("enforcing", nil), true},
		{"enforcing, x509 enabled true", sec("enforcing", map[string]any{"enabled": true}), true},
		{"enforcing, x509 enabled false", sec("enforcing", map[string]any{"enabled": false}), false},
		{"enforcing, x509 present without enabled key", sec("enforcing", map[string]any{}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, x509AuthEnabled(tt.values))
		})
	}
}

// x509Input pins security.authentication.mode to "enforcing" via the input
// values document, since the decision engine itself never sets it.
func x509Input() map[string]any {
	return map[string]any{"security": map[string]any{"authentication": map[string]any{"mode": "enforcing"}}}
}

func TestRecommendWithInput_X509PrivilegeDenial(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.X509KubeSystem = Finding{
			Verdict:  VerdictNo,
			Evidence: []string{"create rolebindings.rbac.authorization.k8s.io in namespace kube-system"},
		}
	})

	t.Run("apply mode errors", func(t *testing.T) {
		_, err := RecommendWithInput(facts, recAnswers(), x509Input(), nil, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rolebindings.rbac.authorization.k8s.io")
	})

	t.Run("validation mode warns and hands off instead of aborting", func(t *testing.T) {
		p, err := RecommendWithInput(facts, recAnswers(), x509Input(), nil, false)
		require.NoError(t, err)
		text := notesText(p)
		assert.Contains(t, text, "rolebindings.rbac.authorization.k8s.io")
		assert.Contains(t, text, "missing privileges listed above")
	})
}

func TestRecommendWithInput_X509PrivilegeUnknown(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.X509KubeSystem = Finding{Verdict: VerdictUnknown, Evidence: []string{"denied listing"}}
	})
	p, err := RecommendWithInput(facts, recAnswers(), x509Input(), nil, true)
	require.NoError(t, err)
	assert.Contains(t, notesText(p), "could not be verified")
}

// TestRecommend_X509NotEnabledSkipsPrivilegeCheck covers the case where the
// values never enable x509 auth: a denied kube-system grant then has no
// effect, since the decision engine's own values never request it.
func TestRecommend_X509NotEnabledSkipsPrivilegeCheck(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.X509KubeSystem = Finding{
			Verdict:  VerdictNo,
			Evidence: []string{"create rolebindings.rbac.authorization.k8s.io in namespace kube-system"},
		}
	})
	p, err := Recommend(facts, recAnswers())
	require.NoError(t, err)
	for _, n := range p.Notes {
		assert.NotContains(t, n.Text, "x509")
	}
}

func TestPrivilegeDenial(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
		f.Privileges.MissingAttributes = []DeniedAttribute{{Verb: "create", Resource: "clusterroles"}}
		f.Privileges.Namespaced = Finding{Verdict: VerdictYes}
		f.Privileges.MissingNamespacedAttributes = nil
	})
	for _, scope := range []ManagedScope{ManagedScopeAll, ""} {
		finding, attrs := PrivilegeDenial(facts, scope)
		assert.Equal(t, VerdictNo, finding.Verdict, "scope %s", scope)
		assert.Equal(t, facts.Privileges.MissingAttributes, attrs, "scope %s", scope)
	}
	for _, scope := range []ManagedScope{ManagedScopeNamespaces, ManagedScopeSelector} {
		finding, attrs := PrivilegeDenial(facts, scope)
		assert.Equal(t, VerdictYes, finding.Verdict, "scope %s", scope)
		assert.Empty(t, attrs, "scope %s", scope)
	}
}

func notesText(p *Proposal) string {
	var sb strings.Builder
	for _, n := range p.Notes {
		sb.WriteString(n.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}
