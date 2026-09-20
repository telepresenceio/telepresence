package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func TestLoadInputValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(path, []byte("nodeAgent:\n  enabled: true\n"), 0o644))
	values, err := LoadInputValues(path)
	require.NoError(t, err)
	assert.Equal(t, &helm.Values{NodeAgent: helm.NodeAgent{Enabled: new(true)}}, values)

	_, err = LoadInputValues(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte(":not yaml"), 0o644))
	_, err = LoadInputValues(bad)
	require.Error(t, err)
}

func TestPinAnswers(t *testing.T) {
	tests := []struct {
		name  string
		in    *helm.Values
		check func(t *testing.T, a *Answers, pre *Preset)
	}{
		{
			name: "both disabled pins attach false",
			in: &helm.Values{
				AgentInjector: helm.AgentInjector{Enabled: new(false)},
				NodeAgent:     helm.NodeAgent{Enabled: new(false)},
			},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.False(t, a.Attach)
				assert.True(t, pre.Attach)
				assert.False(t, pre.Replace)
			},
		},
		{
			name: "node-agent only mode pins attach and replace false",
			in: &helm.Values{
				AgentInjector: helm.AgentInjector{Enabled: new(false)},
				NodeAgent:     helm.NodeAgent{Enabled: new(true)},
			},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.Attach)
				assert.True(t, pre.Attach)
				assert.False(t, a.Replace)
				assert.True(t, pre.Replace)
			},
		},
		{
			name: "node-agent with injector pins replace true",
			in: &helm.Values{
				AgentInjector: helm.AgentInjector{Enabled: new(true)},
				NodeAgent:     helm.NodeAgent{Enabled: new(true)},
			},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.Attach)
				assert.True(t, a.Replace)
				assert.True(t, pre.Replace)
			},
		},
		{
			name: "injector-only mode makes the replace question moot",
			in: &helm.Values{
				AgentInjector: helm.AgentInjector{Enabled: new(true)},
				NodeAgent:     helm.NodeAgent{Enabled: new(false)},
			},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.Attach)
				assert.False(t, a.Replace)
				assert.True(t, pre.Replace)
			},
		},
		{
			name: "node-agent enabled alone pins attach but not replace",
			in:   &helm.Values{NodeAgent: helm.NodeAgent{Enabled: new(true)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.Attach)
				assert.True(t, pre.Attach)
				assert.False(t, pre.Replace)
			},
		},
		{
			name: "node-agent disabled alone pins nothing",
			in:   &helm.Values{NodeAgent: helm.NodeAgent{Enabled: new(false)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.False(t, pre.Attach)
			},
		},
		{
			name: "injector enabled alone pins attach with replace moot",
			in:   &helm.Values{AgentInjector: helm.AgentInjector{Enabled: new(true)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.Attach)
				assert.True(t, pre.Attach)
				assert.False(t, a.Replace)
				assert.True(t, pre.Replace)
			},
		},
		{
			name: "injector disabled alone pins nothing",
			in:   &helm.Values{AgentInjector: helm.AgentInjector{Enabled: new(false)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.False(t, pre.Attach)
			},
		},
		{
			name: "quic enabled pins on",
			in:   &helm.Values{QuicTunnel: helm.QuicTunnel{Enabled: new(true)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, TriOn, a.Quic)
			},
		},
		{
			name: "quic disabled pins off",
			in:   &helm.Values{QuicTunnel: helm.QuicTunnel{Enabled: new(false)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, TriOff, a.Quic)
			},
		},
		{
			name: "namespaces pin the scope",
			in:   &helm.Values{Namespaces: []string{"foo", "bar"}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, ManagedScopeNamespaces, a.ManagedScope)
				assert.True(t, pre.ManagedScope)
				assert.Equal(t, []string{"foo", "bar"}, a.ManagedNamespaces)
				assert.True(t, pre.ManagedNamespaces)
			},
		},
		{
			name: "namespaceSelector pins the selector scope",
			in:   &helm.Values{NamespaceSelector: meta.LabelSelector{MatchLabels: map[string]string{"team": "dev"}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, ManagedScopeSelector, a.ManagedScope)
				assert.True(t, pre.ManagedScope)
				assert.Equal(t, map[string]string{"team": "dev"}, a.SelectorLabels)
			},
		},
		{
			name: "client mapped-namespaces default pins the mapped-namespaces setting",
			in:   &helm.Values{Client: helm.Client{Cluster: helm.ClientCluster{MappedNamespaces: []string{"foo", "bar"}}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, []string{"foo", "bar"}, a.MappedNamespaces)
				assert.True(t, pre.MappedNamespaces)
			},
		},
		{
			name: "namespaces and the mapped-namespaces default pin independently",
			in: &helm.Values{
				Namespaces: []string{"foo"},
				Client:     helm.Client{Cluster: helm.ClientCluster{MappedNamespaces: []string{"bar"}}},
			},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, ManagedScopeNamespaces, a.ManagedScope)
				assert.Equal(t, []string{"foo"}, a.ManagedNamespaces)
				assert.Equal(t, []string{"bar"}, a.MappedNamespaces)
			},
		},
		{
			name: "allowConflictingSubnets pins the conflicts question to allow",
			in:   &helm.Values{Client: helm.Client{Routing: helm.ClientRouting{AllowConflictingSubnets: []string{"10.244.0.0/16"}}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, ConflictsAllow, a.Conflicts)
				assert.True(t, pre.Conflicts)
			},
		},
		{
			name: "autoResolveConflicts true pins the conflicts question to virtual",
			in:   &helm.Values{Client: helm.Client{Routing: helm.ClientRouting{AutoResolveConflicts: new(true)}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, ConflictsVirtual, a.Conflicts)
				assert.True(t, pre.Conflicts)
			},
		},
		{
			name: "autoResolveConflicts false pins nothing",
			in:   &helm.Values{Client: helm.Client{Routing: helm.ClientRouting{AutoResolveConflicts: new(false)}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Empty(t, a.Conflicts)
				assert.False(t, pre.Conflicts)
			},
		},
		{
			name: "allowConflictingSubnets wins over autoResolveConflicts",
			in: &helm.Values{Client: helm.Client{Routing: helm.ClientRouting{
				AllowConflictingSubnets: []string{"10.244.0.0/16"},
				AutoResolveConflicts:    new(true),
			}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, ConflictsAllow, a.Conflicts)
			},
		},
		{
			name: "clientRbac is a pure input passthrough and pins nothing",
			in:   &helm.Values{ClientRbac: helm.ClientRbac{Create: new(true)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.False(t, pre.LegacyAccess)
			},
		},
		{
			name: "unrelated keys pin nothing",
			in:   &helm.Values{Image: helm.Image{Registry: new("ghcr.io/telepresenceio")}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, Preset{}, *pre)
			},
		},
		{
			name: "authentication mode enforcing pins enforce-auth true",
			in:   &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new("enforcing")}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.EnforceAuth)
				assert.True(t, pre.EnforceAuth)
			},
		},
		{
			name: "authentication mode permissive pins enforce-auth false",
			in:   &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new("permissive")}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.False(t, a.EnforceAuth)
				assert.True(t, pre.EnforceAuth)
			},
		},
		{
			name: "a valid requiredGrant pins the grant",
			in:   &helm.Values{Security: helm.Security{Authorization: helm.Authorization{RequiredGrant: new("portforward")}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.Equal(t, "portforward", a.RequiredGrant)
				assert.True(t, pre.RequiredGrant)
			},
		},
		{
			name: "an invalid requiredGrant pins nothing",
			in:   &helm.Values{Security: helm.Security{Authorization: helm.Authorization{RequiredGrant: new("bogus")}}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.False(t, pre.RequiredGrant)
			},
		},
		{
			name: "externalEndpoint disabled pins false",
			in:   &helm.Values{ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(false)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.False(t, a.ExternalEndpoint)
				assert.True(t, pre.ExternalEndpoint)
			},
		},
		{
			name: "externalEndpoint enabled with a Secret carries the secret name",
			in: &helm.Values{ExternalEndpoint: helm.ExternalEndpoint{
				Enabled: new(true),
				TLS:     helm.ExternalTLS{SecretName: new("my-secret")},
			}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.ExternalEndpoint)
				assert.Equal(t, "my-secret", a.ExternalTLSSecret)
			},
		},
		{
			name: "externalEndpoint enabled with cert-manager carries the issuer and DNS names",
			in: &helm.Values{ExternalEndpoint: helm.ExternalEndpoint{
				Enabled: new(true),
				TLS: helm.ExternalTLS{CertManager: helm.CertManager{
					Enabled:   new(true),
					IssuerRef: helm.IssuerRef{Name: new("letsencrypt"), Kind: new("ClusterIssuer")},
					DNSNames:  []string{"tm.example.com"},
				}},
			}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				require.NotNil(t, a.ExternalCertManager)
				assert.Equal(t, "letsencrypt", a.ExternalCertManager.IssuerName)
				assert.Equal(t, "ClusterIssuer", a.ExternalCertManager.IssuerKind)
				assert.Equal(t, []string{"tm.example.com"}, a.ExternalCertManager.DNSNames)
			},
		},
		{
			name: "clientRbac legacyAccess pins the legacy-access answer",
			in:   &helm.Values{ClientRbac: helm.ClientRbac{LegacyAccess: new(true)}},
			check: func(t *testing.T, a *Answers, pre *Preset) {
				assert.True(t, a.LegacyAccess)
				assert.True(t, pre.LegacyAccess)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Answers{}
			pre := &Preset{}
			PinAnswers(tt.in, a, pre)
			tt.check(t, a, pre)
		})
	}
}

func TestPinAnswers_ScopeIndependentDefaults(t *testing.T) {
	t.Run("a pinned mapped-namespaces default fills the answer independently of the managed scope", func(t *testing.T) {
		in := &helm.Values{
			Namespaces: []string{"foo"},
			Client:     helm.Client{Cluster: helm.ClientCluster{MappedNamespaces: []string{"bar", "baz"}}},
		}
		a := &Answers{}
		pre := &Preset{}
		PinAnswers(in, a, pre)
		assert.Equal(t, ManagedScopeNamespaces, a.ManagedScope)
		assert.Equal(t, []string{"foo"}, a.ManagedNamespaces)
		assert.Equal(t, []string{"bar", "baz"}, a.MappedNamespaces)
		assert.True(t, pre.MappedNamespaces)
	})
	t.Run("a flag-preset mapped-namespaces wins over a pin", func(t *testing.T) {
		in := &helm.Values{Client: helm.Client{Cluster: helm.ClientCluster{MappedNamespaces: []string{"bar"}}}}
		a := &Answers{MappedNamespaces: []string{"foo"}}
		pre := &Preset{MappedNamespaces: true}
		PinAnswers(in, a, pre)
		assert.Equal(t, []string{"foo"}, a.MappedNamespaces)
	})
	t.Run("an already-preset answer wins over a pin", func(t *testing.T) {
		in := &helm.Values{
			AgentInjector: helm.AgentInjector{Enabled: new(false)},
			QuicTunnel:    helm.QuicTunnel{Enabled: new(false)},
		}
		a := &Answers{Attach: true, Quic: TriOn}
		pre := &Preset{Attach: true}
		PinAnswers(in, a, pre)
		assert.True(t, a.Attach)
		assert.Equal(t, TriOn, a.Quic)
	})
	t.Run("already-preset security answers win over their pins", func(t *testing.T) {
		in := &helm.Values{
			Security: helm.Security{
				Authentication: helm.Authentication{Mode: new("enforcing")},
				Authorization:  helm.Authorization{RequiredGrant: new("portforward")},
			},
			ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(true)},
			ClientRbac:       helm.ClientRbac{LegacyAccess: new(true)},
		}
		a := &Answers{EnforceAuth: false, RequiredGrant: RequiredGrantAny, ExternalEndpoint: false, LegacyAccess: false}
		pre := &Preset{EnforceAuth: true, RequiredGrant: true, ExternalEndpoint: true, LegacyAccess: true}
		PinAnswers(in, a, pre)
		assert.False(t, a.EnforceAuth)
		assert.Equal(t, RequiredGrantAny, a.RequiredGrant)
		assert.False(t, a.ExternalEndpoint)
		assert.False(t, a.LegacyAccess)
	})
}

func TestValidateValues(t *testing.T) {
	t.Run("webhook denied", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.Webhook.CanCreate = Finding{Verdict: VerdictNo} })
		err := facts.ValidateValues(&helm.Values{AgentInjector: helm.AgentInjector{Enabled: new(true)}}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutatingwebhookconfigurations")

		assert.NoError(t, facts.ValidateValues(&helm.Values{AgentInjector: helm.AgentInjector{Enabled: new(false)}}, true))
	})
	t.Run("webhook denied is a hard error even without --apply", func(t *testing.T) {
		// Hard incompatibilities are not downgraded: only the missing
		// install-privilege check softens for validation-only runs.
		facts := recFacts(func(f *ClusterFacts) { f.Webhook.CanCreate = Finding{Verdict: VerdictNo} })
		err := facts.ValidateValues(&helm.Values{AgentInjector: helm.AgentInjector{Enabled: new(true)}}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutatingwebhookconfigurations")
	})
	t.Run("cluster-wide denied", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
			f.Privileges.Missing = []string{"create clusterroles.rbac.authorization.k8s.io"}
		})
		err := facts.ValidateValues(&helm.Values{}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cluster-wide")

		assert.NoError(t, facts.ValidateValues(&helm.Values{Namespaces: []string{"foo"}}, true))
	})
	t.Run("cluster-wide denial does not abort a validation-only run", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
			f.Privileges.Missing = []string{"create clusterroles.rbac.authorization.k8s.io"}
		})
		assert.NoError(t, facts.ValidateValues(&helm.Values{}, false))
	})
	t.Run("quic with multiple replicas", func(t *testing.T) {
		err := recFacts().ValidateValues(&helm.Values{
			QuicTunnel:   helm.QuicTunnel{Enabled: new(true)},
			ReplicaCount: new(int32(2)),
		}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "replicaCount")
	})
	t.Run("clean pass", func(t *testing.T) {
		assert.NoError(t, recFacts().ValidateValues(&helm.Values{
			AgentInjector: helm.AgentInjector{Enabled: new(false)},
			NodeAgent:     helm.NodeAgent{Enabled: new(true)},
			QuicTunnel:    helm.QuicTunnel{Enabled: new(true)},
		}, true))
	})
	t.Run("external endpoint requires enforcing mode", func(t *testing.T) {
		err := recFacts().ValidateValues(&helm.Values{
			Security:         helm.Security{Authentication: helm.Authentication{Mode: new("permissive")}},
			ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(true), TLS: helm.ExternalTLS{SecretName: new("my-secret")}},
		}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "security.authentication.mode: enforcing")
	})
	t.Run("external endpoint requires exactly one of secretName or certManager.enabled", func(t *testing.T) {
		enforcing := helm.Security{Authentication: helm.Authentication{Mode: new("enforcing")}}
		neither := helm.ExternalEndpoint{Enabled: new(true)}
		both := helm.ExternalEndpoint{
			Enabled: new(true),
			TLS: helm.ExternalTLS{
				SecretName:  new("my-secret"),
				CertManager: helm.CertManager{Enabled: new(true)},
			},
		}
		for _, extra := range []helm.ExternalEndpoint{neither, both} {
			vals := &helm.Values{Security: enforcing, ExternalEndpoint: extra}
			err := recFacts().ValidateValues(vals, true)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "externalEndpoint.tls: set exactly one of")
		}
	})
	t.Run("external endpoint with a valid Secret and enforcing mode passes", func(t *testing.T) {
		assert.NoError(t, recFacts().ValidateValues(&helm.Values{
			Security:         helm.Security{Authentication: helm.Authentication{Mode: new("enforcing")}},
			ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(true), TLS: helm.ExternalTLS{SecretName: new("my-secret")}},
		}, true))
	})
	t.Run("external endpoint with cert-manager and enforcing mode passes", func(t *testing.T) {
		assert.NoError(t, recFacts().ValidateValues(&helm.Values{
			Security: helm.Security{Authentication: helm.Authentication{Mode: new("enforcing")}},
			ExternalEndpoint: helm.ExternalEndpoint{
				Enabled: new(true),
				TLS:     helm.ExternalTLS{CertManager: helm.CertManager{Enabled: new(true)}},
			},
		}, true))
	})
}

// TestRecommendWithInput_RoundTrip covers the losslessness contract: an input
// with keys the engine has no opinion about survives into the final values.
func TestRecommendWithInput_RoundTrip(t *testing.T) {
	input := &helm.Values{
		Image:         helm.Image{Registry: new("ghcr.io/other"), Tag: new("2.30.0")},
		LogLevel:      new("debug"),
		AgentInjector: helm.AgentInjector{Enabled: new(false)},
		NodeAgent:     helm.NodeAgent{Enabled: new(true)},
		QuicTunnel:    helm.QuicTunnel{Enabled: new(true)},
	}
	answers := recAnswers()
	pre := Preset{}
	PinAnswers(input, answers, &pre)

	p, err := RecommendWithInput(recFacts(), answers, input, nil, true)
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/other", *p.Values.Image.Registry)
	assert.Equal(t, "2.30.0", *p.Values.Image.Tag)
	assert.Equal(t, "debug", *p.Values.LogLevel)
	assert.True(t, *p.Values.NodeAgent.Enabled)
	assert.False(t, *p.Values.AgentInjector.Enabled)
	for _, n := range p.Notes {
		assert.NotEqual(t, NoteWarning, n.Level, "unexpected warning: %s", n.Text)
	}
}
