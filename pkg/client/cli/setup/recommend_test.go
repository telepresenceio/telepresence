package setup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rbac "k8s.io/api/rbac/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
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
		Release:    ReleaseFacts{Values: &helm.Values{}},
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
			assert.Equal(t, tt.wantNodeAgent, *p.Values.NodeAgent.Enabled)
			assert.Equal(t, tt.wantInjector, *p.Values.AgentInjector.Enabled)
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

func TestRecommend_UnreadableInstalledValues(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{Installed: true, ValuesError: "unable to convert map to Values: bogus", Values: &helm.Values{}}
	})
	_, err := Recommend(facts, recAnswers())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be read")
	assert.Contains(t, err.Error(), "bogus")
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
		assert.True(t, *p.Values.QuicTunnel.Enabled)
		assert.Zero(t, p.Values.QuicTunnel.Service)
	})
	t.Run("nodePort with cluster-wide scope", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Quic.LoadBalancer = Finding{Verdict: VerdictNo}
		})
		p, err := Recommend(facts, recAnswers())
		require.NoError(t, err)
		assert.True(t, *p.Values.QuicTunnel.Enabled)
		assert.Equal(t, "NodePort", *p.Values.QuicTunnel.Service.Type)
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
		assert.False(t, *p.Values.QuicTunnel.Enabled)
		require.NotEmpty(t, p.Notes)
		assert.Contains(t, p.notesText(), "QUIC disabled")
	})
}

func TestRecommend_QuicSuppressedByReplicaCount(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true,
			Version:   version.Structured.String(),
			Namespace: "ambassador",
			Values:    &helm.Values{ReplicaCount: new(int32(2))},
		}
	})
	p, err := Recommend(facts, recAnswers())
	require.NoError(t, err)
	assert.False(t, *p.Values.QuicTunnel.Enabled)
	assert.Contains(t, p.notesText(), "replicaCount")
}

func TestRecommend_UpgradeMerge(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true,
			Version:   olderVersion(),
			Namespace: "ambassador",
			Values: &helm.Values{
				LogLevel:      new("debug"),
				AgentInjector: helm.AgentInjector{Enabled: new(true)},
			},
		}
	})
	answers := recAnswers(func(a *Answers) { a.UpgradeManager = true })
	p, err := Recommend(facts, answers)
	require.NoError(t, err)

	assert.Equal(t, ActionUpgrade, p.Action)
	assert.Empty(t, p.Version)
	assert.Equal(t, facts.Release.Values, p.BaseValues)
	assert.Equal(t, "debug", *p.Values.LogLevel)
	assert.False(t, *p.Values.AgentInjector.Enabled)
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
			Values:    &helm.Values{},
		}
	})
	p, err := Recommend(facts, recAnswers())
	require.NoError(t, err)
	assert.Equal(t, ActionUpgrade, p.Action)
	assert.Equal(t, olderVersion(), p.Version)
	assert.Contains(t, p.notesText(), "version "+olderVersion()+" is kept")
}

func TestRecommend_NewerRelease(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true,
			Version:   newerVersion(),
			Namespace: "ambassador",
			Values:    &helm.Values{},
		}
	})
	p, err := Recommend(facts, recAnswers())
	require.NoError(t, err)
	assert.Equal(t, ActionNone, p.Action)
	assert.Nil(t, p.Values)
	assert.Contains(t, p.notesText(), "newer than this client")
}

func TestRecommend_MappedNamespaces(t *testing.T) {
	answers := recAnswers(func(a *Answers) {
		a.MappedNamespaces = []string{"foo", "bar"}
	})
	p, err := Recommend(recFacts(), answers)
	require.NoError(t, err)
	assert.Equal(t, []string{"foo", "bar"}, p.Values.Client.Cluster.MappedNamespaces)
	assert.Nil(t, p.Values.Namespaces)
	assert.Contains(t, p.notesText(), "mapped-namespaces default")

	t.Run("no mapped namespaces sets no client value regardless of managed scope", func(t *testing.T) {
		for _, scope := range []ManagedScope{ManagedScopeAll, ManagedScopeNamespaces, ManagedScopeSelector} {
			answers := recAnswers(func(a *Answers) {
				a.ManagedScope = scope
				a.ManagedNamespaces = []string{"foo"}
				a.SelectorLabels = map[string]string{"team": "dev"}
			})
			p, err := Recommend(recFacts(), answers)
			require.NoError(t, err)
			assert.Zero(t, p.Values.Client, "scope %s", scope)
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
		assert.Equal(t, []string{"foo", "bar"}, p.Values.Namespaces)
		assert.Equal(t, []string{"baz"}, p.Values.Client.Cluster.MappedNamespaces)
	})
}

func TestRecommend_SelectorScope(t *testing.T) {
	answers := recAnswers(func(a *Answers) {
		a.ManagedScope = ManagedScopeSelector
		a.SelectorLabels = map[string]string{"team": "dev"}
	})
	p, err := Recommend(recFacts(), answers)
	require.NoError(t, err)
	assert.Equal(t, "dev", p.Values.NamespaceSelector.MatchLabels["team"])
	assert.Contains(t, p.notesText(), "maxNamespaceSpecificWatchers")
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

	t.Run("virtual strategy resolves the conflicts through a VNAT", func(t *testing.T) {
		p, err := Recommend(conflictFacts(), recAnswers(func(a *Answers) { a.Conflicts = ConflictsVirtual }))
		require.NoError(t, err)
		assert.True(t, *p.Values.Client.Routing.AutoResolveConflicts)
		text := p.notesText()
		assert.Contains(t, text, "10.244.0.0/16")
		assert.Contains(t, text, "no --vnat flag is needed")
	})
	t.Run("allow strategy sends the conflicts to the cluster", func(t *testing.T) {
		p, err := Recommend(conflictFacts(), recAnswers(func(a *Answers) { a.Conflicts = ConflictsAllow }))
		require.NoError(t, err)
		assert.Equal(t, []string{"10.244.0.0/16", "10.96.0.0/16"}, p.Values.Client.Routing.AllowConflictingSubnets)
		assert.NotContains(t, p.notesText(), "--vnat")
	})
	t.Run("no conflicts set nothing", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) { a.Conflicts = ConflictsAllow }))
		require.NoError(t, err)
		assert.Zero(t, p.Values.Client)
	})
	t.Run("mapped namespaces and the allow strategy share the client value", func(t *testing.T) {
		answers := recAnswers(func(a *Answers) {
			a.MappedNamespaces = []string{"foo"}
			a.Conflicts = ConflictsAllow
		})
		p, err := Recommend(conflictFacts(), answers)
		require.NoError(t, err)
		assert.Equal(t, []string{"foo"}, p.Values.Client.Cluster.MappedNamespaces)
		assert.Equal(t, []string{"10.244.0.0/16", "10.96.0.0/16"}, p.Values.Client.Routing.AllowConflictingSubnets)
	})
}

// TestRecommend_ClientRbacPassthrough proves that clientRbac.create,
// namespaces, and subjects stay a pure --input concern, while
// clientRbac.legacyAccess is the engine's own opinion merged into the same
// block.
func TestRecommend_ClientRbacPassthrough(t *testing.T) {
	t.Run("no input still sets legacyAccess from the answer", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers())
		require.NoError(t, err)
		assert.False(t, *p.Values.ClientRbac.LegacyAccess)
	})
	t.Run("an input's clientRbac block round-trips with legacyAccess merged in", func(t *testing.T) {
		input := &helm.Values{
			ClientRbac: helm.ClientRbac{
				Create:     new(true),
				Namespaces: []string{"foo", "bar"},
				Subjects: []rbac.Subject{
					{Kind: "User", Name: "alice", APIGroup: "rbac.authorization.k8s.io"},
					{Kind: "ServiceAccount", Name: "sa", Namespace: "ns"},
				},
			},
		}
		p, err := RecommendWithInput(recFacts(), recAnswers(), input, nil, true)
		require.NoError(t, err)
		want := helm.ClientRbac{
			Create:       new(true),
			Namespaces:   []string{"foo", "bar"},
			LegacyAccess: new(false),
			Subjects: []rbac.Subject{
				{Kind: "User", Name: "alice", APIGroup: "rbac.authorization.k8s.io"},
				{Kind: "ServiceAccount", Name: "sa", Namespace: "ns"},
			},
		}
		assert.Equal(t, want, p.Values.ClientRbac)
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
		p, err := RecommendWithInput(facts, recAnswers(), &helm.Values{}, nil, false)
		require.NoError(t, err)
		require.NotNil(t, p)
		text := p.notesText()
		assert.Contains(t, text, "create clusterroles.rbac.authorization.k8s.io")
		assert.Contains(t, text, "--input FILE --apply")
		assert.Contains(t, text, "missing privileges listed above")
	})

	t.Run("apply mode still errors", func(t *testing.T) {
		_, err := RecommendWithInput(facts, recAnswers(), &helm.Values{}, nil, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create clusterroles.rbac.authorization.k8s.io")
	})
}

func TestX509AuthEnabled(t *testing.T) {
	sec := func(mode string, x509 helm.X509) *helm.Values {
		return &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new(mode), X509: x509}}}
	}
	tests := []struct {
		name   string
		values *helm.Values
		want   bool
	}{
		{"no security block", &helm.Values{}, false},
		{"permissive mode", sec("permissive", helm.X509{}), false},
		// an absent x509 block and a present-but-empty one are the same value.
		{"enforcing, x509 absent", sec("enforcing", helm.X509{}), true},
		{"enforcing, x509 enabled true", sec("enforcing", helm.X509{Enabled: new(true)}), true},
		{"enforcing, x509 enabled false", sec("enforcing", helm.X509{Enabled: new(false)}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, x509AuthEnabled(tt.values))
		})
	}
}

// x509Input pins security.authentication.mode to "enforcing" via the input
// values document, since the decision engine itself never sets it.
func x509Input() *helm.Values {
	return &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}}}
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
		text := p.notesText()
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
	assert.Contains(t, p.notesText(), "could not be verified")
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
		finding, attrs := facts.PrivilegeDenial(scope)
		assert.Equal(t, VerdictNo, finding.Verdict, "scope %s", scope)
		assert.Equal(t, facts.Privileges.MissingAttributes, attrs, "scope %s", scope)
	}
	for _, scope := range []ManagedScope{ManagedScopeNamespaces, ManagedScopeSelector} {
		finding, attrs := facts.PrivilegeDenial(scope)
		assert.Equal(t, VerdictYes, finding.Verdict, "scope %s", scope)
		assert.Empty(t, attrs, "scope %s", scope)
	}
}

func TestRecommend_SecurityValues(t *testing.T) {
	t.Run("permissive mode emits no requiredGrant", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers())
		require.NoError(t, err)
		assert.Equal(t, "permissive", *p.Values.Security.Authentication.Mode)
		assert.Zero(t, p.Values.Security.Authorization)
	})
	t.Run("enforcing mode defaults requiredGrant to any", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) { a.EnforceAuth = true }))
		require.NoError(t, err)
		assert.Equal(t, "enforcing", *p.Values.Security.Authentication.Mode)
		assert.Equal(t, "any", *p.Values.Security.Authorization.RequiredGrant)
	})
	t.Run("enforcing mode honors an explicit requiredGrant", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.RequiredGrant = RequiredGrantPortForward
		}))
		require.NoError(t, err)
		assert.Equal(t, RequiredGrantPortForward, *p.Values.Security.Authorization.RequiredGrant)
	})
	t.Run("enforcing without an external endpoint and no prerequisites notes it", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) { a.EnforceAuth = true }))
		require.NoError(t, err)
		assert.Contains(t, p.notesText(), "Direct Connect not proposed: it needs")
	})
	t.Run("denied Secret listing is reported instead of a missing certificate", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.External.SecretsListDenied = true })
		p, err := Recommend(facts, recAnswers(func(a *Answers) { a.EnforceAuth = true }))
		require.NoError(t, err)
		assert.Contains(t, p.notesText(), "Direct Connect not proposed: setup may not list Secrets")
		assert.NotContains(t, p.notesText(), "add one and run setup again")
	})
	t.Run("cert-manager available suppresses the no-prerequisites note", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.External.CertManager = Finding{Verdict: VerdictYes} })
		p, err := Recommend(facts, recAnswers(func(a *Answers) { a.EnforceAuth = true }))
		require.NoError(t, err)
		assert.NotContains(t, p.notesText(), "Direct Connect not proposed")
	})
	t.Run("an existing TLS Secret suppresses the no-prerequisites note", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.External.TLSSecrets = []TLSSecretFacts{{Name: "my-secret"}} })
		p, err := Recommend(facts, recAnswers(func(a *Answers) { a.EnforceAuth = true }))
		require.NoError(t, err)
		assert.NotContains(t, p.notesText(), "Direct Connect not proposed")
	})
}

func TestRecommend_ExternalEndpointValues(t *testing.T) {
	t.Run("LoadBalancer service type when QUIC's LoadBalancer probe is viable", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = true
			a.ExternalTLSSecret = "my-secret"
		}))
		require.NoError(t, err)
		assert.True(t, *p.Values.ExternalEndpoint.Enabled)
		assert.Equal(t, "LoadBalancer", *p.Values.ExternalEndpoint.Service.Type)
		assert.Equal(t, "my-secret", *p.Values.ExternalEndpoint.TLS.SecretName)
		assert.NotContains(t, p.notesText(), "node address")
	})
	t.Run("NodePort service type notes the DNS name requirement", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.Quic.LoadBalancer = Finding{Verdict: VerdictNo} })
		p, err := Recommend(facts, recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = true
			a.ExternalTLSSecret = "my-secret"
		}))
		require.NoError(t, err)
		assert.Equal(t, "NodePort", *p.Values.ExternalEndpoint.Service.Type)
		assert.Contains(t, p.notesText(), "must resolve to a node address")
	})
	t.Run("a cert-manager answer emits the certManager block", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = true
			a.ExternalCertManager = &CertManagerAnswer{IssuerName: "letsencrypt", IssuerKind: "ClusterIssuer", DNSNames: []string{"tm.example.com"}}
		}))
		require.NoError(t, err)
		assert.True(t, *p.Values.ExternalEndpoint.TLS.CertManager.Enabled)
		assert.Equal(t, "letsencrypt", *p.Values.ExternalEndpoint.TLS.CertManager.IssuerRef.Name)
		assert.Equal(t, "ClusterIssuer", *p.Values.ExternalEndpoint.TLS.CertManager.IssuerRef.Kind)
		assert.Equal(t, []string{"tm.example.com"}, p.Values.ExternalEndpoint.TLS.CertManager.DNSNames)
		assert.Nil(t, p.Values.ExternalEndpoint.TLS.SecretName)
	})
	t.Run("warns when QUIC ends up disabled alongside the endpoint", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.Quic.LoadBalancer = Finding{Verdict: VerdictNo} })
		answers := recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = true
			a.ExternalTLSSecret = "my-secret"
			a.ManagedScope = ManagedScopeNamespaces
			a.ManagedNamespaces = []string{"ambassador"}
		})
		p, err := Recommend(facts, answers)
		require.NoError(t, err)
		assert.False(t, *p.Values.QuicTunnel.Enabled)
		assert.Contains(t, p.notesText(), "attachments need the QUIC endpoint alongside Direct Connect")
	})
	t.Run("no QUIC warning when QUIC stays enabled", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = true
			a.ExternalTLSSecret = "my-secret"
		}))
		require.NoError(t, err)
		assert.NotContains(t, p.notesText(), "attachments need the QUIC endpoint")
	})
}

func TestRecommend_ExternalEndpointUpgradeMerge(t *testing.T) {
	t.Run("a no answer disables an already-enabled endpoint and shows up as changed", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: olderVersion(), Namespace: "ambassador",
				Values: &helm.Values{ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(true)}},
			}
		})
		p, err := Recommend(facts, recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = false
			a.UpgradeManager = true
		}))
		require.NoError(t, err)
		assert.False(t, *p.Values.ExternalEndpoint.Enabled)
		assert.Contains(t, p.ChangedKeys, "externalEndpoint.enabled")
	})
	t.Run("an already-set service type is not re-emitted", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{ExternalEndpoint: helm.ExternalEndpoint{Service: helm.ExternalService{Type: new("ClusterIP")}}},
			}
		})
		p, err := Recommend(facts, recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = true
			a.ExternalTLSSecret = "my-secret"
		}))
		require.NoError(t, err)
		assert.Equal(t, "ClusterIP", *p.Values.ExternalEndpoint.Service.Type)
	})
	t.Run("no certificate answer emits no tls key and the release's own tls survives the merge", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{ExternalEndpoint: helm.ExternalEndpoint{
					Enabled: new(true),
					TLS:     helm.ExternalTLS{SecretName: new("release-secret")},
				}},
			}
		})
		p, err := Recommend(facts, recAnswers(func(a *Answers) {
			a.EnforceAuth = true
			a.ExternalEndpoint = true
		}))
		require.NoError(t, err)
		assert.Equal(t, "release-secret", *p.Values.ExternalEndpoint.TLS.SecretName)
	})
}

func TestRecommend_LegacyAccess(t *testing.T) {
	t.Run("default false with no apiPort override", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers())
		require.NoError(t, err)
		assert.False(t, *p.Values.ClientRbac.LegacyAccess)
		assert.NotContains(t, p.notesText(), "apiPort")
	})
	t.Run("an explicit yes is honored", func(t *testing.T) {
		p, err := Recommend(recFacts(), recAnswers(func(a *Answers) { a.LegacyAccess = true }))
		require.NoError(t, err)
		assert.True(t, *p.Values.ClientRbac.LegacyAccess)
	})
	t.Run("an overridden apiPort in the release values forces legacyAccess and warns", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{APIPort: new(int32(9090))},
			}
		})
		p, err := Recommend(facts, recAnswers())
		require.NoError(t, err)
		assert.True(t, *p.Values.ClientRbac.LegacyAccess)
		assert.Contains(t, p.notesText(), "apiPort is overridden")
	})
	t.Run("an overridden apiPort in the input forces legacyAccess and warns", func(t *testing.T) {
		input := &helm.Values{APIPort: new(int32(9090))}
		p, err := RecommendWithInput(recFacts(), recAnswers(), input, nil, true)
		require.NoError(t, err)
		assert.True(t, *p.Values.ClientRbac.LegacyAccess)
		assert.Contains(t, p.notesText(), "apiPort is overridden")
	})
	t.Run("apiPort at its default does not force legacyAccess", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{APIPort: new(int32(8081))},
			}
		})
		p, err := Recommend(facts, recAnswers())
		require.NoError(t, err)
		assert.False(t, *p.Values.ClientRbac.LegacyAccess)
		assert.NotContains(t, p.notesText(), "apiPort")
	})
	t.Run("an overridden apiPort and an already-true legacyAccess does not warn", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{APIPort: new(int32(9090))},
			}
		})
		p, err := Recommend(facts, recAnswers(func(a *Answers) { a.LegacyAccess = true }))
		require.NoError(t, err)
		assert.True(t, *p.Values.ClientRbac.LegacyAccess)
		assert.NotContains(t, p.notesText(), "apiPort is overridden")
	})
}

func TestDecideAction_DeploymentMigrationNote(t *testing.T) {
	t.Run("upgrading from a Deployment notes the migration", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: olderVersion(), Namespace: "ambassador", Workload: "Deployment",
				Values: &helm.Values{LogLevel: new("debug")},
			}
		})
		p, err := Recommend(facts, recAnswers(func(a *Answers) { a.UpgradeManager = true }))
		require.NoError(t, err)
		assert.Equal(t, ActionUpgrade, p.Action)
		assert.Contains(t, p.notesText(), "migrates the traffic-manager from a Deployment to a StatefulSet")
	})
	t.Run("upgrading from a StatefulSet does not note a migration", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: olderVersion(), Namespace: "ambassador", Workload: "StatefulSet",
				Values: &helm.Values{LogLevel: new("debug")},
			}
		})
		p, err := Recommend(facts, recAnswers(func(a *Answers) { a.UpgradeManager = true }))
		require.NoError(t, err)
		assert.Equal(t, ActionUpgrade, p.Action)
		assert.NotContains(t, p.notesText(), "migrates the traffic-manager")
	})
	t.Run("no action pending means no migration note even on a Deployment", func(t *testing.T) {
		answers := recAnswers()
		base, err := Recommend(recFacts(), answers)
		require.NoError(t, err)
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: base.Values, Workload: "Deployment",
			}
		})
		p, err := Recommend(facts, answers)
		require.NoError(t, err)
		assert.Equal(t, ActionNone, p.Action)
		assert.NotContains(t, p.notesText(), "migrates the traffic-manager")
	})
}

func certManagerInput() *helm.Values {
	return &helm.Values{
		Security: helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}},
		ExternalEndpoint: helm.ExternalEndpoint{
			Enabled: new(true),
			TLS:     helm.ExternalTLS{CertManager: helm.CertManager{Enabled: new(true)}},
		},
	}
}

func TestCheckCertManagerPrivileges(t *testing.T) {
	t.Run("not enabled is a no-op", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.CertManagerCertificate = Finding{Verdict: VerdictNo, Evidence: []string{"create certificates.cert-manager.io"}}
		})
		p, err := Recommend(facts, recAnswers())
		require.NoError(t, err)
		for _, n := range p.Notes {
			assert.NotContains(t, n.Text, "cert-manager certificate")
		}
	})
	t.Run("denied errors when applying", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.CertManagerCertificate = Finding{Verdict: VerdictNo, Evidence: []string{"create certificates.cert-manager.io"}}
		})
		_, err := RecommendWithInput(facts, recAnswers(), certManagerInput(), nil, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "certificates.cert-manager.io")
	})
	t.Run("denied warns and hands off when only validating", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.CertManagerCertificate = Finding{Verdict: VerdictNo, Evidence: []string{"create certificates.cert-manager.io"}}
		})
		p, err := RecommendWithInput(facts, recAnswers(), certManagerInput(), nil, false)
		require.NoError(t, err)
		text := p.notesText()
		assert.Contains(t, text, "certificates.cert-manager.io")
		assert.Contains(t, text, "missing privileges listed above")
	})
	t.Run("unknown is an info note", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.CertManagerCertificate = Finding{Verdict: VerdictUnknown, Evidence: []string{"denied listing"}}
		})
		p, err := RecommendWithInput(facts, recAnswers(), certManagerInput(), nil, true)
		require.NoError(t, err)
		assert.Contains(t, p.notesText(), "could not be verified")
	})
	t.Run("allowed is silent", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.Privileges.CertManagerCertificate = Finding{Verdict: VerdictYes} })
		p, err := RecommendWithInput(facts, recAnswers(), certManagerInput(), nil, true)
		require.NoError(t, err)
		for _, n := range p.Notes {
			assert.NotContains(t, n.Text, "cert-manager certificate")
		}
	})
}

func (p *Proposal) notesText() string {
	var sb strings.Builder
	for _, n := range p.Notes {
		sb.WriteString(n.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}
