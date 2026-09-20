package setup

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
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
	assert.Equal(t, ManagedScopeAll, a.ManagedScope)
	assert.Equal(t, TriAuto, a.Quic)
	assert.Equal(t, TriAuto, a.NodeAgent)
	assert.Contains(t, out, "attach to workloads")
	assert.Contains(t, out, "replace command")
	assert.Contains(t, out, "Choose 1-3 [1]")
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
			f.Release = ReleaseFacts{Installed: true, Version: olderVersion(), Namespace: "ambassador", Values: &helm.Values{}}
		})
		a, out, err := runInterview(t, facts, "\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.True(t, a.UpgradeManager)
		assert.Contains(t, out, "Upgrade the traffic-manager?")
	})
	t.Run("newer release is advisory only", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{Installed: true, Version: newerVersion(), Namespace: "ambassador", Values: &helm.Values{}}
		})
		a, out, err := runInterview(t, facts, "\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.False(t, a.UpgradeManager)
		assert.NotContains(t, out, "Upgrade the traffic-manager?")
		assert.Contains(t, out, "newer than this client")
	})
}

func TestInterview_ManagedScopeNamespaces(t *testing.T) {
	a, out, err := runInterview(t, recFacts(), "\n\n2\nfoo,bar\n", Answers{}, Preset{}, false)
	require.NoError(t, err)
	assert.Equal(t, ManagedScopeNamespaces, a.ManagedScope)
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
		assert.Equal(t, ManagedScopeAll, a.ManagedScope)
		assert.Empty(t, out)
	})
	t.Run("missing cluster-wide privileges default to a namespace scope", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
		})
		a, _, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.Equal(t, ManagedScopeNamespaces, a.ManagedScope)
		assert.Equal(t, []string{"ambassador"}, a.ManagedNamespaces)
	})
	t.Run("selector scope requires an interactive session", func(t *testing.T) {
		_, _, err := runInterview(t, recFacts(), "", Answers{ManagedScope: ManagedScopeSelector}, Preset{ManagedScope: true}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--managed-scope=selector")
	})
}

func TestInterview_PresetSkipsPrompts(t *testing.T) {
	a, out, err := runInterview(t, recFacts(), "\n", Answers{Attach: false}, Preset{Attach: true}, false)
	require.NoError(t, err)
	assert.False(t, a.Attach)
	assert.NotContains(t, out, "attach to workloads")
	assert.NotContains(t, out, "replace command")
	assert.Contains(t, out, "Choose 1-3")
	assert.Equal(t, ManagedScopeAll, a.ManagedScope)
}

// TestInterview_InputPinsSkipQuestions verifies that answers pinned by an
// input values document are not asked, while unpinned questions still are.
func TestInterview_InputPinsSkipQuestions(t *testing.T) {
	t.Run("pinned attach and scope are not asked", func(t *testing.T) {
		in := &helm.Values{
			AgentInjector: helm.AgentInjector{Enabled: new(false)},
			NodeAgent:     helm.NodeAgent{Enabled: new(true)},
			QuicTunnel:    helm.QuicTunnel{Enabled: new(false)},
			Namespaces:    []string{"foo"},
		}
		a := Answers{Attach: true, Quic: TriAuto}
		pre := Preset{}
		PinAnswers(in, &a, &pre)

		got, out, err := runInterview(t, recFacts(), "", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "attach to workloads")
		assert.NotContains(t, out, "replace command")
		assert.NotContains(t, out, "Choose 1-3")
		assert.True(t, got.Attach)
		assert.False(t, got.Replace)
		assert.Equal(t, TriOff, got.Quic)
		assert.Equal(t, ManagedScopeNamespaces, got.ManagedScope)
		assert.Equal(t, []string{"foo", "ambassador"}, got.ManagedNamespaces)
	})
	t.Run("VPN-only input pins attach=false and skips the question", func(t *testing.T) {
		in := &helm.Values{
			AgentInjector: helm.AgentInjector{Enabled: new(false)},
			NodeAgent:     helm.NodeAgent{Enabled: new(false)},
		}
		a := Answers{Attach: true, Quic: TriAuto}
		pre := Preset{}
		PinAnswers(in, &a, &pre)

		got, out, err := runInterview(t, recFacts(), "\n", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "attach to workloads")
		assert.NotContains(t, out, "replace command")
		assert.Contains(t, out, "Choose 1-3")
		assert.False(t, got.Attach)
	})
	t.Run("unpinned questions are still asked", func(t *testing.T) {
		in := &helm.Values{NodeAgent: helm.NodeAgent{Enabled: new(true)}}
		a := Answers{Quic: TriAuto}
		pre := Preset{}
		PinAnswers(in, &a, &pre)

		got, out, err := runInterview(t, recFacts(), "\n\n", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "attach to workloads")
		assert.Contains(t, out, "replace command")
		assert.Contains(t, out, "Choose 1-3")
		assert.True(t, got.Attach)
	})
}

func TestInterview_ConflictStrategy(t *testing.T) {
	withConflicts := func(f *ClusterFacts) {
		f.Routing = RoutingFacts{
			Summary: Finding{Verdict: VerdictNo},
			Conflicts: []RoutingConflict{
				{ClusterSubnet: "10.244.0.0/16", Source: sourcePodCIDR, LocalRoute: "10.0.0.0/8", Interface: "tun0"},
			},
		}
	}
	conflictFacts := recFacts(withConflicts)

	t.Run("asked only when conflicts exist, default virtual", func(t *testing.T) {
		a, out, err := runInterview(t, conflictFacts, "\n\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Local routes overlap the cluster's subnets (10.244.0.0/16). How should clients handle those ranges?")
		assert.Contains(t, out, "Choose 1-2 [1]")
		assert.Equal(t, ConflictsVirtual, a.Conflicts)
	})
	t.Run("choice 2 sends the conflicts to the cluster", func(t *testing.T) {
		a, _, err := runInterview(t, conflictFacts, "\n\n\n\n\n2\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.Equal(t, ConflictsAllow, a.Conflicts)
	})
	t.Run("not asked without conflicts", func(t *testing.T) {
		_, out, err := runInterview(t, recFacts(), "\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "Local routes overlap")
	})
	t.Run("non-interactive defaults to virtual", func(t *testing.T) {
		a, out, err := runInterview(t, conflictFacts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.Equal(t, ConflictsVirtual, a.Conflicts)
		assert.Empty(t, out)
	})
	t.Run("default follows the installed release's allowConflictingSubnets", func(t *testing.T) {
		facts := recFacts(withConflicts, func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{Client: helm.Client{Routing: helm.ClientRouting{AllowConflictingSubnets: []string{"10.244.0.0/16"}}}},
			}
		})
		a, out, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.Equal(t, ConflictsAllow, a.Conflicts)
		assert.Empty(t, out)
	})
	t.Run("release settings for other subnets do not change the virtual default", func(t *testing.T) {
		facts := recFacts(withConflicts, func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{Client: helm.Client{Routing: helm.ClientRouting{AllowConflictingSubnets: []string{"192.168.0.0/16"}}}},
			}
		})
		a, _, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.Equal(t, ConflictsVirtual, a.Conflicts)
	})
	t.Run("preset skips the question", func(t *testing.T) {
		a, out, err := runInterview(t, conflictFacts, "\n\n\n", Answers{Conflicts: ConflictsAllow}, Preset{Conflicts: true}, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "Local routes overlap")
		assert.Equal(t, ConflictsAllow, a.Conflicts)
	})
}

func TestInterview_EnforceAuth(t *testing.T) {
	t.Run("fresh install defaults to no without client credentials", func(t *testing.T) {
		a, out, err := runInterview(t, recFacts(), "\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.False(t, a.EnforceAuth)
		assert.Contains(t, out, "Enforce caller authentication? [y/N] ")
	})
	t.Run("fresh install defaults to yes with a bearer token", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.ClientAuth = ClientAuthFacts{Bearer: true} })
		a, out, err := runInterview(t, facts, "\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.True(t, a.EnforceAuth)
		assert.Contains(t, out, "[Y/n] ")
	})
	t.Run("fresh install defaults to yes with a usable client certificate", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.ClientAuth = ClientAuthFacts{X509: true}
			f.Privileges.X509KubeSystem = Finding{Verdict: VerdictYes}
		})
		a, _, err := runInterview(t, facts, "\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.True(t, a.EnforceAuth)
	})
	t.Run("fresh install defaults to yes with a client certificate even without kube-system privilege", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.ClientAuth = ClientAuthFacts{X509: true}
			f.Privileges.X509KubeSystem = Finding{Verdict: VerdictNo}
		})
		a, _, err := runInterview(t, facts, "\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.True(t, a.EnforceAuth)
	})
	t.Run("installed release defaults to its own enforcing setting", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}}},
			}
		})
		a, _, err := runInterview(t, facts, "\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.True(t, a.EnforceAuth)
	})
	t.Run("installed release defaults to its own permissive setting", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new("permissive")}}},
			}
		})
		a, _, err := runInterview(t, facts, "\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.False(t, a.EnforceAuth)
	})
	t.Run("preset skips the question", func(t *testing.T) {
		a, out, err := runInterview(t, recFacts(), "\n\n\n\n", Answers{EnforceAuth: true}, Preset{EnforceAuth: true}, false)
		require.NoError(t, err)
		assert.True(t, a.EnforceAuth)
		assert.NotContains(t, out, "Enforce caller authentication?")
	})
	t.Run("non-interactive takes the default", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.ClientAuth = ClientAuthFacts{Bearer: true} })
		a, out, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.True(t, a.EnforceAuth)
		assert.Empty(t, out)
	})
}

// TestInterview_ExplainEnforceAuth covers the one caller-verdict line printed
// ahead of the enforce-auth prompt, and that it is skipped non-interactively.
func TestInterview_ExplainEnforceAuth(t *testing.T) {
	bearerLine := "Your own client will keep working: your kubeconfig has a bearer token."
	x509OKLine := "Your own client will keep working: your kubeconfig has a client certificate."
	x509NoKubeSystemLine := "Your own client will keep working once an admin applies this: verifying your client certificate needs a " +
		"RoleBinding in kube-system that you are not allowed to create."
	neitherLine := "Your own client would be locked out: your kubeconfig has neither a token nor a client certificate."
	alreadyEnforcesLine := "The installed traffic-manager already enforces authentication."

	for name, tc := range map[string]struct {
		mod  func(*ClusterFacts)
		want string
	}{
		"bearer token": {
			mod:  func(f *ClusterFacts) { f.ClientAuth = ClientAuthFacts{Bearer: true} },
			want: bearerLine,
		},
		"x509 with kube-system privilege": {
			mod: func(f *ClusterFacts) {
				f.ClientAuth = ClientAuthFacts{X509: true}
				f.Privileges.X509KubeSystem = Finding{Verdict: VerdictYes}
			},
			want: x509OKLine,
		},
		"x509 without kube-system privilege": {
			mod: func(f *ClusterFacts) {
				f.ClientAuth = ClientAuthFacts{X509: true}
				f.Privileges.X509KubeSystem = Finding{Verdict: VerdictNo}
			},
			want: x509NoKubeSystemLine,
		},
		"neither credential": {
			mod:  func(f *ClusterFacts) {},
			want: neitherLine,
		},
	} {
		t.Run(name, func(t *testing.T) {
			facts := recFacts(tc.mod)
			_, out, err := runInterview(t, facts, "\n\n\n\n\n", Answers{}, Preset{}, false)
			require.NoError(t, err)
			assert.Contains(t, out, "Enforcing means the traffic-manager refuses telepresence clients older than")
			assert.Contains(t, out, tc.want)
			assert.NotContains(t, out, alreadyEnforcesLine)
		})
	}

	t.Run("installed release already enforcing prints the extra line", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.ClientAuth = ClientAuthFacts{Bearer: true}
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}}},
			}
		})
		_, out, err := runInterview(t, facts, "\n\n\n\n\n", Answers{}, Preset{}, false)
		require.NoError(t, err)
		assert.Contains(t, out, alreadyEnforcesLine)
	})

	t.Run("non-interactive prints none of it", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.ClientAuth = ClientAuthFacts{Bearer: true} })
		_, out, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.Empty(t, out)
	})
}

// extEndpointHarness freezes every question except "external endpoint", so
// the input stream only needs to answer that one.
func extEndpointHarness(enforceAuth bool) (Answers, Preset) {
	return Answers{Attach: false, ManagedScope: ManagedScopeAll, EnforceAuth: enforceAuth, RequiredGrant: RequiredGrantAny, LegacyAccess: false},
		Preset{Attach: true, ManagedScope: true, EnforceAuth: true, RequiredGrant: true, LegacyAccess: true}
}

func TestInterview_ExternalEndpoint(t *testing.T) {
	t.Run("skipped when not enforcing", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.External.CertManager = Finding{Verdict: VerdictYes} })
		a, pre := extEndpointHarness(false)
		got, out, err := runInterview(t, facts, "", a, pre, false)
		require.NoError(t, err)
		assert.False(t, got.ExternalEndpoint)
		assert.NotContains(t, out, "Enable Direct Connect")
	})
	t.Run("skipped when neither cert-manager nor a TLS Secret is available", func(t *testing.T) {
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, recFacts(), "", a, pre, false)
		require.NoError(t, err)
		assert.False(t, got.ExternalEndpoint)
		assert.NotContains(t, out, "Enable Direct Connect")
	})
	t.Run("cert-manager alone is auto-picked", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.External.CertManager = Finding{Verdict: VerdictYes} })
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "y\nissuer1\n\nexample.com\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Enable Direct Connect, so clients reach the traffic-manager at a published address instead of through the Kubernetes API? [y/N] ")
		assert.True(t, got.ExternalEndpoint)
		assert.NotContains(t, out, "Which one should it use?")
		require.NotNil(t, got.ExternalCertManager)
		assert.Equal(t, "issuer1", got.ExternalCertManager.IssuerName)
		assert.Equal(t, "ClusterIssuer", got.ExternalCertManager.IssuerKind)
		assert.Equal(t, []string{"example.com"}, got.ExternalCertManager.DNSNames)
	})
	t.Run("a single TLS Secret is auto-picked", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) { f.External.TLSSecrets = []TLSSecretFacts{{Name: "my-secret"}} })
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "y\n", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "Which one should it use?")
		assert.Equal(t, "my-secret", got.ExternalTLSSecret)
		assert.Nil(t, got.ExternalCertManager)
	})
	t.Run("the chart's own Secret name defaults when present and valid", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.External.TLSSecrets = []TLSSecretFacts{{Name: certManagerSecretName}, {Name: "other"}}
		})
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "y\n\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Choose 1-2 [1]: ")
		assert.Equal(t, certManagerSecretName, got.ExternalTLSSecret)
	})
	t.Run("several unnamed Secrets have no default and require an explicit choice", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.External.TLSSecrets = []TLSSecretFacts{{Name: "a-secret"}, {Name: "b-secret"}}
		})
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "y\n2\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "The endpoint needs a TLS certificate that clients can trust. Which one should it use?")
		assert.Contains(t, out, `Secret "a-secret"`)
		assert.Contains(t, out, `Secret "b-secret"`)
		assert.Contains(t, out, "Choose 1-2: ")
		assert.Equal(t, "b-secret", got.ExternalTLSSecret)
	})
	t.Run("cert-manager is listed after the Secrets", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.External.CertManager = Finding{Verdict: VerdictProbable}
			f.External.TLSSecrets = []TLSSecretFacts{{Name: "a-secret"}}
		})
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "y\n2\nissuer1\n\nexample.com\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "  1) Secret \"a-secret\"")
		assert.Contains(t, out, "  2) a new one issued by cert-manager")
		require.NotNil(t, got.ExternalCertManager)
		assert.Equal(t, "issuer1", got.ExternalCertManager.IssuerName)
	})
	t.Run("non-interactive with no default errors", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{
					Security:         helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}},
					ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(true)},
				},
			}
			f.External.TLSSecrets = []TLSSecretFacts{{Name: "a-secret"}, {Name: "b-secret"}}
		})
		_, _, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "externalEndpoint.tls.secretName must be pinned in the input")
	})
}

func TestInterview_RequiredGrant(t *testing.T) {
	base := func(externalEndpoint bool) (Answers, Preset) {
		return Answers{
				Attach: false, ManagedScope: ManagedScopeAll, EnforceAuth: true,
				ExternalEndpoint: externalEndpoint, LegacyAccess: false,
			},
			Preset{Attach: true, ManagedScope: true, EnforceAuth: true, ExternalEndpoint: true, LegacyAccess: true}
	}
	t.Run("skipped when not enforcing", func(t *testing.T) {
		a := Answers{Attach: false, ManagedScope: ManagedScopeAll, EnforceAuth: false, LegacyAccess: false}
		pre := Preset{Attach: true, ManagedScope: true, EnforceAuth: true, LegacyAccess: true}
		got, out, err := runInterview(t, recFacts(), "", a, pre, false)
		require.NoError(t, err)
		assert.Empty(t, got.RequiredGrant)
		assert.NotContains(t, out, "Which grant should the traffic-manager require")
	})
	t.Run("defaults to any without an external endpoint", func(t *testing.T) {
		a, pre := base(false)
		got, out, err := runInterview(t, recFacts(), "\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Choose 1-3 [3]: ")
		assert.Equal(t, RequiredGrantAny, got.RequiredGrant)
	})
	t.Run("defaults to telepresence with an external endpoint", func(t *testing.T) {
		a, pre := base(true)
		got, out, err := runInterview(t, recFacts(), "\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Choose 1-3 [1]: ")
		assert.Equal(t, RequiredGrantTelepresence, got.RequiredGrant)
	})
	t.Run("defaults to the installed release's own setting", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{Security: helm.Security{Authorization: helm.Authorization{RequiredGrant: new("portforward")}}},
			}
		})
		a, pre := base(false)
		got, out, err := runInterview(t, facts, "\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Choose 1-3 [2]: ")
		assert.Equal(t, RequiredGrantPortForward, got.RequiredGrant)
	})
	t.Run("an installed release that never set it keeps the chart default even with an external endpoint", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{Installed: true, Version: version.Structured.String(), Namespace: "ambassador", Values: &helm.Values{}}
		})
		a, pre := base(true)
		got, out, err := runInterview(t, facts, "\n", a, pre, false)
		require.NoError(t, err)
		assert.Contains(t, out, "Choose 1-3 [3]: ")
		assert.Equal(t, RequiredGrantAny, got.RequiredGrant)
	})
	t.Run("an explicit choice overrides the default", func(t *testing.T) {
		a, pre := base(false)
		got, _, err := runInterview(t, recFacts(), "1\n", a, pre, false)
		require.NoError(t, err)
		assert.Equal(t, RequiredGrantTelepresence, got.RequiredGrant)
	})
	t.Run("preset skips the question", func(t *testing.T) {
		a := Answers{
			Attach: false, ManagedScope: ManagedScopeAll, EnforceAuth: true,
			RequiredGrant: RequiredGrantPortForward, LegacyAccess: false,
		}
		pre := Preset{Attach: true, ManagedScope: true, EnforceAuth: true, RequiredGrant: true, LegacyAccess: true}
		got, out, err := runInterview(t, recFacts(), "", a, pre, false)
		require.NoError(t, err)
		assert.NotContains(t, out, "Which grant should the traffic-manager require")
		assert.Equal(t, RequiredGrantPortForward, got.RequiredGrant)
	})
}

func TestInterview_LegacyAccess(t *testing.T) {
	base := func() (Answers, Preset) {
		return Answers{Attach: false, ManagedScope: ManagedScopeAll, EnforceAuth: false},
			Preset{Attach: true, ManagedScope: true, EnforceAuth: true}
	}
	t.Run("fresh install defaults to no", func(t *testing.T) {
		a, pre := base()
		got, out, err := runInterview(t, recFacts(), "\n", a, pre, false)
		require.NoError(t, err)
		assert.False(t, got.LegacyAccess)
		assert.Contains(t, out, "Do clients older than 2.32 need to connect to this traffic-manager? [y/N] ")
	})
	t.Run("installed release defaults to its own setting", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{ClientRbac: helm.ClientRbac{LegacyAccess: new(true)}},
			}
		})
		a, pre := base()
		got, out, err := runInterview(t, facts, "\n", a, pre, false)
		require.NoError(t, err)
		assert.True(t, got.LegacyAccess)
		assert.Contains(t, out, "[Y/n] ")
	})
	t.Run("installed release without an explicit setting defaults to yes, matching the chart default", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{Installed: true, Version: version.Structured.String(), Namespace: "ambassador", Values: &helm.Values{}}
		})
		a, pre := base()
		got, out, err := runInterview(t, facts, "\n", a, pre, false)
		require.NoError(t, err)
		assert.True(t, got.LegacyAccess)
		assert.Contains(t, out, "[Y/n] ")
	})
	t.Run("installed release with an explicit no is not overridden", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{ClientRbac: helm.ClientRbac{LegacyAccess: new(false)}},
			}
		})
		a, pre := base()
		got, out, err := runInterview(t, facts, "\n", a, pre, false)
		require.NoError(t, err)
		assert.False(t, got.LegacyAccess)
		assert.Contains(t, out, "[y/N] ")
	})
	t.Run("an explicit answer overrides the default", func(t *testing.T) {
		facts := recFacts(func(f *ClusterFacts) {
			f.Release = ReleaseFacts{
				Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
				Values: &helm.Values{ClientRbac: helm.ClientRbac{LegacyAccess: new(true)}},
			}
		})
		a, pre := base()
		got, _, err := runInterview(t, facts, "n\n", a, pre, false)
		require.NoError(t, err)
		assert.False(t, got.LegacyAccess)
	})
	t.Run("preset skips the question", func(t *testing.T) {
		got, out, err := runInterview(t, recFacts(), "", Answers{LegacyAccess: true}, Preset{LegacyAccess: true}, false)
		require.NoError(t, err)
		assert.True(t, got.LegacyAccess)
		assert.NotContains(t, out, "Do clients older than 2.32")
	})
}

// TestInterview_SecurityQuestionOrder confirms the questions are asked in
// the order enforce-auth, external-endpoint, required-grant, legacy-access.
func TestInterview_SecurityQuestionOrder(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.ClientAuth = ClientAuthFacts{Bearer: true}
		f.External.CertManager = Finding{Verdict: VerdictYes}
	})
	_, out, err := runInterview(t, facts, "\n\n\n\nn\n\n\n", Answers{}, Preset{}, false)
	require.NoError(t, err)

	iEnforce := strings.Index(out, "Enforce caller authentication?")
	iExternal := strings.Index(out, "Enable Direct Connect")
	iGrant := strings.Index(out, "Which grant should the traffic-manager require")
	iLegacy := strings.Index(out, "Do clients older than 2.32")
	require.True(t, iEnforce >= 0 && iExternal >= 0 && iGrant >= 0 && iLegacy >= 0, out)
	assert.True(t, iEnforce < iExternal)
	assert.True(t, iExternal < iGrant)
	assert.True(t, iGrant < iLegacy)
}

// releaseWithExternalTLS returns facts for an installed release whose values
// already enable the external endpoint with the given TLS source, plus one
// unrelated TLS Secret so the endpoint question's own prerequisite gate is
// satisfied.
func releaseWithExternalTLS(tls helm.ExternalTLS) *ClusterFacts {
	return recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
			Values: &helm.Values{
				Security:         helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}},
				ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(true), TLS: tls},
			},
		}
		f.External.TLSSecrets = []TLSSecretFacts{{Name: "unrelated-secret"}}
	})
}

func TestInterview_ReleaseConfiguresExternalTLS(t *testing.T) {
	t.Run("an explicit yes is not asked the certificate question", func(t *testing.T) {
		facts := releaseWithExternalTLS(helm.ExternalTLS{SecretName: new("release-secret")})
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "y\n", a, pre, false)
		require.NoError(t, err)
		assert.True(t, got.ExternalEndpoint)
		assert.NotContains(t, out, "Which one should it use?")
		assert.Empty(t, got.ExternalTLSSecret)
		assert.Nil(t, got.ExternalCertManager)
	})
	t.Run("an empty line takes the release's own yes default and skips the certificate question", func(t *testing.T) {
		facts := releaseWithExternalTLS(helm.ExternalTLS{CertManager: helm.CertManager{Enabled: new(true)}})
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "\n", a, pre, false)
		require.NoError(t, err)
		assert.True(t, got.ExternalEndpoint)
		assert.NotContains(t, out, "Which one should it use?")
		assert.Empty(t, got.ExternalTLSSecret)
		assert.Nil(t, got.ExternalCertManager)
	})
	t.Run("non-interactive does not error despite several TLS Secrets in the facts", func(t *testing.T) {
		facts := releaseWithExternalTLS(helm.ExternalTLS{SecretName: new("release-secret")})
		facts.External.TLSSecrets = []TLSSecretFacts{{Name: "a-secret"}, {Name: "b-secret"}, {Name: "c-secret"}}
		got, _, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.True(t, got.ExternalEndpoint)
		assert.Empty(t, got.ExternalTLSSecret)
		assert.Nil(t, got.ExternalCertManager)
	})
}

// releaseEnabledExternalTLSDiscoveryDenied returns facts for an installed
// release that enables the external endpoint and names its own TLS Secret,
// while this run's own certificate discovery found nothing: the Secrets list
// was denied and cert-manager's presence could not be determined.
func releaseEnabledExternalTLSDiscoveryDenied() *ClusterFacts {
	return recFacts(func(f *ClusterFacts) {
		f.Release = ReleaseFacts{
			Installed: true, Version: version.Structured.String(), Namespace: "ambassador",
			Values: &helm.Values{
				Security: helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}},
				ExternalEndpoint: helm.ExternalEndpoint{
					Enabled: new(true),
					TLS:     helm.ExternalTLS{SecretName: new("release-secret")},
				},
			},
		}
		f.External.SecretsListDenied = true
	})
}

func TestInterview_ExternalEndpointSurvivesInconclusiveDiscovery(t *testing.T) {
	t.Run("non-interactive keeps it enabled without a certificate prompt", func(t *testing.T) {
		facts := releaseEnabledExternalTLSDiscoveryDenied()
		got, out, err := runInterview(t, facts, "", Answers{}, Preset{}, true)
		require.NoError(t, err)
		assert.True(t, got.ExternalEndpoint)
		assert.Empty(t, out)
	})
	t.Run("interactive with an empty line keeps it enabled without a certificate prompt", func(t *testing.T) {
		facts := releaseEnabledExternalTLSDiscoveryDenied()
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, facts, "\n", a, pre, false)
		require.NoError(t, err)
		assert.True(t, got.ExternalEndpoint)
		assert.NotContains(t, out, "Which one should it use?")
	})
	t.Run("fresh install with nothing found still asks nothing", func(t *testing.T) {
		a, pre := extEndpointHarness(true)
		got, out, err := runInterview(t, recFacts(), "", a, pre, false)
		require.NoError(t, err)
		assert.False(t, got.ExternalEndpoint)
		assert.NotContains(t, out, "Enable Direct Connect")
	})
}

// TestInterview_AskExternalCertificate covers askExternalCertificate directly,
// including the single-candidate shortcut's expiry guard and the empty-list
// guard that askExternalEndpoint's own gating can no longer rule out.
func TestInterview_AskExternalCertificate(t *testing.T) {
	t.Run("a sole valid Secret is taken silently", func(t *testing.T) {
		iv := &Interviewer{}
		name, useCertManager, err := iv.askExternalCertificate([]TLSSecretFacts{{Name: "only"}}, false)
		require.NoError(t, err)
		assert.Equal(t, "only", name)
		assert.False(t, useCertManager)
	})
	t.Run("a sole cert-manager candidate is taken silently", func(t *testing.T) {
		iv := &Interviewer{}
		_, useCertManager, err := iv.askExternalCertificate(nil, true)
		require.NoError(t, err)
		assert.True(t, useCertManager)
	})
	t.Run("a sole expired Secret is shown and requires an explicit interactive choice", func(t *testing.T) {
		out := &bytes.Buffer{}
		iv := &Interviewer{In: strings.NewReader("1\n"), Out: out}
		notAfter := time.Now().Add(-time.Hour).Format(time.RFC3339)
		name, _, err := iv.askExternalCertificate([]TLSSecretFacts{{Name: "only", Expired: true, NotAfter: notAfter}}, false)
		require.NoError(t, err)
		assert.Equal(t, "only", name)
		assert.Contains(t, out.String(), "(expired)")
		assert.Contains(t, out.String(), "Choose 1-1: ")
	})
	t.Run("a sole expiring-soon Secret errors non-interactively", func(t *testing.T) {
		iv := &Interviewer{NonInteractive: true}
		notAfter := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
		_, _, err := iv.askExternalCertificate([]TLSSecretFacts{{Name: "only", ExpiringSoon: true, NotAfter: notAfter}}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the only TLS Secret found for the external endpoint is expired or expiring soon")
		assert.Contains(t, err.Error(), "externalEndpoint.tls.secretName must be pinned in the input")
	})
	t.Run("an empty candidate list errors instead of indexing an empty slice", func(t *testing.T) {
		iv := &Interviewer{NonInteractive: true}
		_, _, err := iv.askExternalCertificate(nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no TLS Secret or cert-manager")
	})
}

func TestExternalCertDefault(t *testing.T) {
	tests := []struct {
		name    string
		secrets []TLSSecretFacts
		want    int
	}{
		{"no secrets has no default", nil, 0},
		{"a single normal secret defaults to it", []TLSSecretFacts{{Name: "only"}}, 1},
		{"a single expired secret has no default", []TLSSecretFacts{{Name: "only", Expired: true}}, 0},
		{"a single expiring-soon secret has no default", []TLSSecretFacts{{Name: "only", ExpiringSoon: true}}, 0},
		{
			"the chart's own Secret name defaults when first and valid",
			[]TLSSecretFacts{{Name: certManagerSecretName}, {Name: "other"}},
			1,
		},
		{
			"the chart's own Secret name has no default when expired",
			[]TLSSecretFacts{{Name: certManagerSecretName, Expired: true}, {Name: "other"}},
			0,
		},
		{"several unnamed secrets have no default", []TLSSecretFacts{{Name: "a"}, {Name: "b"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, externalCertDefault(tt.secrets))
		})
	}
}

func TestInterview_ManagedScopeRecommendation(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Privileges.ClusterWide = Finding{Verdict: VerdictNo}
	})
	a, out, err := runInterview(t, facts, "\n\n\nfoo\n", Answers{}, Preset{}, false)
	require.NoError(t, err)
	assert.Contains(t, out, "cluster-wide install looks impossible")
	assert.Contains(t, out, "Choose 1-3 [2]")
	assert.Equal(t, ManagedScopeNamespaces, a.ManagedScope)
	assert.Equal(t, []string{"foo", "ambassador"}, a.ManagedNamespaces)
}
