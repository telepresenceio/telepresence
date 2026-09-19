package setup

import (
	"bytes"
	"encoding/json/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
)

func renderSummary() *Summary {
	return &Summary{
		Facts:   recFacts(),
		Answers: recAnswers(),
		Proposal: &Proposal{
			Action: ActionInstall,
			Values: &helm.Values{
				AgentInjector: helm.AgentInjector{Enabled: new(false)},
				NodeAgent:     helm.NodeAgent{Enabled: new(true)},
			},
			Notes: []Note{
				{Level: NoteInfo, Text: "an informational note"},
				{Level: NoteWarning, Text: "a cautionary note"},
			},
		},
		Action: ActionWord(ActionInstall, false),
	}
}

func TestPrintReport_Text(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)

	require.NoError(t, PrintReport(cmd, renderSummary()))
	text := out.String()
	assert.Contains(t, text, "Findings:")
	assert.Contains(t, text, "  privileges: cluster-wide install yes")
	assert.Contains(t, text, "Proposed configuration:")
	assert.Contains(t, text, "  nodeAgent:")
	assert.Contains(t, text, "Notes:")
	assert.Contains(t, text, "  note: an informational note")
	assert.Contains(t, text, "  warning: a cautionary note")
	assert.Contains(t, text, "Action: would-install")
}

func TestPrintReport_ReleaseValuesError(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)

	s := renderSummary()
	s.Facts.Release = ReleaseFacts{
		Installed:   true,
		Namespace:   "ambassador",
		Version:     "2.31.0",
		ValuesError: "unable to convert map to Values: bogus",
		Values:      &helm.Values{},
	}
	require.NoError(t, PrintReport(cmd, s))
	text := out.String()
	assert.Contains(t, text, "release: traffic-manager 2.31.0 installed in namespace ambassador")
	assert.Contains(t, text, "values: unable to convert map to Values: bogus")
}

func TestActionWord(t *testing.T) {
	assert.Equal(t, "would-install", ActionWord(ActionInstall, false))
	assert.Equal(t, "would-upgrade", ActionWord(ActionUpgrade, false))
	assert.Equal(t, "install", ActionWord(ActionInstall, true))
	assert.Equal(t, "none", ActionWord(ActionNone, false))
	assert.Equal(t, "none", ActionWord(ActionNone, true))
}

// TestPrintReport_LocalOutputFlagDoesNotConfuseFormatting pins the deliberate
// shadowing of the deprecated hidden global --output format flag by setup's
// local --output FILE flag: the output package recognizes the global flag by
// its "default" default value, so a set local flag with default "" must not
// switch the report into formatted mode.
func TestPrintReport_LocalOutputFlagDoesNotConfuseFormatting(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.Flags().String("output", "", "")
	require.NoError(t, cmd.Flags().Set("output", "/tmp/values.yaml"))

	assert.False(t, output.WantsFormatted(cmd))
	require.NoError(t, PrintReport(cmd, renderSummary()))
	assert.Contains(t, out.String(), "Findings:")
}

func TestSummary_MarshalsCleanJSON(t *testing.T) {
	data, err := json.Marshal(renderSummary())
	require.NoError(t, err)

	text := string(data)
	assert.Contains(t, text, `"facts":`)
	assert.Contains(t, text, `"answers":`)
	assert.Contains(t, text, `"proposal":`)
	assert.Contains(t, text, `"action":"would-install"`)
}

func TestPrintReport_PlannedObjectsSection(t *testing.T) {
	s := renderSummary()
	s.PlannedObjects = []string{"ClusterRole traffic-manager-ambassador", "Deployment traffic-manager.ambassador"}

	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s))
	text := out.String()
	assert.Contains(t, text, "This install will create:")
	assert.Contains(t, text, "  - Deployment traffic-manager.ambassador")
	assert.Contains(t, text, "removed by 'telepresence helm uninstall'")

	// Anything but an install renders no will-create section.
	s.Proposal.Action = ActionUpgrade
	out.Reset()
	require.NoError(t, PrintReport(cmd, s))
	assert.NotContains(t, out.String(), "This install will create:")
}

func TestPrintReport_ClusterLine(t *testing.T) {
	s := renderSummary()
	s.Facts.Context = "kind-dev"
	s.Facts.Server = "https://127.0.0.1:6443"

	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s))
	assert.Contains(t, out.String(), "  cluster: context kind-dev, server https://127.0.0.1:6443")
}

func TestPrintReport_RoutingLine(t *testing.T) {
	t.Run("no conflicts", func(t *testing.T) {
		s := renderSummary()
		s.Facts.Routing = RoutingFacts{Summary: Finding{Verdict: VerdictYes}}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		assert.Contains(t, out.String(), "  routing: no conflicts")
	})
	t.Run("conflicts with evidence", func(t *testing.T) {
		s := renderSummary()
		s.Facts.Routing = RoutingFacts{
			Summary: Finding{Verdict: VerdictNo, Evidence: []string{"10.244.0.0/16 (pod CIDR) overlaps local route 10.0.0.0/8 dev tun0"}},
			Conflicts: []RoutingConflict{
				{ClusterSubnet: "10.244.0.0/16", Source: sourcePodCIDR, LocalRoute: "10.0.0.0/8", Interface: "tun0"},
			},
		}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		text := out.String()
		assert.Contains(t, text, "  routing: 1 conflicts")
		assert.Contains(t, text, "    - 10.244.0.0/16 (pod CIDR) overlaps local route 10.0.0.0/8 dev tun0")
	})
	t.Run("unknown", func(t *testing.T) {
		s := renderSummary()
		s.Facts.Routing = RoutingFacts{Summary: Finding{Verdict: VerdictUnknown, Evidence: []string{"the workstation's routing table could not be read: nope"}}}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		assert.Contains(t, out.String(), "  routing: unknown")
	})
}

func TestPrintReport_AuthenticationLine(t *testing.T) {
	cases := []struct {
		name   string
		auth   ClientAuthFacts
		verd   Verdict
		expect string
	}{
		{"bearer only", ClientAuthFacts{Bearer: true}, VerdictYes, "this client has bearer token; kube-system x509 RoleBinding yes"},
		{"x509 only", ClientAuthFacts{X509: true}, VerdictNo, "this client has client certificate; kube-system x509 RoleBinding no"},
		{"both", ClientAuthFacts{Bearer: true, X509: true}, VerdictYes, "this client has bearer token and client certificate; kube-system x509 RoleBinding yes"},
		{"neither", ClientAuthFacts{}, VerdictUnknown, "this client has no usable credential; kube-system x509 RoleBinding unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := renderSummary()
			s.Facts.ClientAuth = c.auth
			s.Facts.Privileges.X509KubeSystem = Finding{Verdict: c.verd}
			out := &bytes.Buffer{}
			cmd := &cobra.Command{}
			cmd.SetOut(out)
			require.NoError(t, PrintReport(cmd, s))
			assert.Contains(t, out.String(), "  authentication: "+c.expect)
		})
	}
}

func TestPrintReport_ExternalEndpointArea(t *testing.T) {
	t.Run("no secrets, cert-manager absent", func(t *testing.T) {
		s := renderSummary()
		s.Facts.External = ExternalFacts{CertManager: Finding{Verdict: VerdictNo}}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		assert.Contains(t, out.String(), "  external endpoint: cert-manager no, 0 TLS secrets")
	})
	t.Run("secrets with expiry annotations", func(t *testing.T) {
		s := renderSummary()
		s.Facts.External = ExternalFacts{
			CertManager: Finding{Verdict: VerdictYes},
			TLSSecrets: []TLSSecretFacts{
				{Name: "tm-external-tls", DNSNames: []string{"tm.example.com"}, NotAfter: "2027-03-01T00:00:00Z"},
				{Name: "expiring-soon", NotAfter: "2026-09-20T00:00:00Z", ExpiringSoon: true},
				{Name: "expired-one", NotAfter: "2020-01-01T00:00:00Z", Expired: true},
				{Name: "unreadable", ReadError: "could not read tls.crt: denied"},
			},
		}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		text := out.String()
		assert.Contains(t, text, "  external endpoint: cert-manager yes, 4 TLS secrets")
		assert.Contains(t, text, "    - tm-external-tls  tm.example.com  expires 2027-03-01")
		assert.Contains(t, text, "    - expiring-soon  expires 2026-09-20 (expires within 30 days)")
		assert.Contains(t, text, "    - expired-one  expires 2020-01-01 (expired)")
		assert.Contains(t, text, "    - unreadable  could not read tls.crt: denied")
	})
	t.Run("listing denied", func(t *testing.T) {
		s := renderSummary()
		s.Facts.External = ExternalFacts{CertManager: Finding{Verdict: VerdictNo}, SecretsListDenied: true}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		assert.Contains(t, out.String(), "  external endpoint: cert-manager no, TLS secrets: listing denied")
	})
	t.Run("unknown on error", func(t *testing.T) {
		s := renderSummary()
		s.Facts.External = ExternalFacts{CertManager: Finding{Verdict: VerdictUnknown}, SecretsListError: "boom"}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		assert.Contains(t, out.String(), "  external endpoint: cert-manager unknown, TLS secrets: unknown")
	})
}

func TestPrintReport_ReleaseWorkloadLine(t *testing.T) {
	t.Run("deployment appends the migration note", func(t *testing.T) {
		s := renderSummary()
		s.Facts.Release = ReleaseFacts{Installed: true, Version: "2.31.0", Namespace: "ambassador", Workload: "Deployment", Values: &helm.Values{}}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		assert.Contains(t, out.String(), "  release: traffic-manager 2.31.0 installed in namespace ambassador, running as a Deployment")
	})
	t.Run("statefulset renders no extra note", func(t *testing.T) {
		s := renderSummary()
		s.Facts.Release = ReleaseFacts{Installed: true, Version: "2.32.0", Namespace: "ambassador", Workload: "StatefulSet", Values: &helm.Values{}}
		out := &bytes.Buffer{}
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		require.NoError(t, PrintReport(cmd, s))
		text := out.String()
		assert.Contains(t, text, "  release: traffic-manager 2.32.0 installed in namespace ambassador")
		assert.NotContains(t, text, "running as a Deployment")
	})
}

func TestPrintReport_ExternalEndpointHealthLine(t *testing.T) {
	s := renderSummary()
	ext := Finding{Verdict: VerdictYes, Evidence: []string{"the traffic-manager-external service is LoadBalancer, address 1.2.3.4"}}
	s.Facts.Health = &HealthFacts{
		ManagerReady:     Finding{Verdict: VerdictYes},
		ExternalEndpoint: &ext,
		VersionSkew:      Finding{Verdict: VerdictYes},
	}
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s))
	text := out.String()
	assert.Contains(t, text, "  health: external endpoint yes")
	assert.Contains(t, text, "    - the traffic-manager-external service is LoadBalancer, address 1.2.3.4")
}

func TestBanner(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Context = "kind-dev"
		f.Server = "https://127.0.0.1:6443"
	})
	assert.Equal(t, `Configuring cluster "kind-dev" (server https://127.0.0.1:6443, manager namespace ambassador)`, facts.Banner())
}

func TestPrintReport_HealthLines(t *testing.T) {
	s := renderSummary()
	quic := Finding{Verdict: VerdictYes, Evidence: []string{"QUIC endpoint allocated node port 31234"}}
	s.Facts.Health = &HealthFacts{
		ManagerReady: Finding{Verdict: VerdictNo, Evidence: []string{"0 of 1 replicas ready", "BackOff: image pull failure"}},
		Quic:         &quic,
		VersionSkew:  Finding{Verdict: VerdictYes, Evidence: []string{"client and traffic-manager are both 2.31.0"}},
	}
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s))
	text := out.String()
	assert.Contains(t, text, "  health: traffic-manager no")
	assert.Contains(t, text, "    - BackOff: image pull failure")
	assert.Contains(t, text, "  health: quic endpoint yes")
	assert.Contains(t, text, "  health: version skew yes")
	assert.NotContains(t, text, "agent-injector webhook", "absent optional findings render no line")
}

func TestWriteValues_ProvenanceHeader(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Context = "kind-dev"
		f.Server = "https://127.0.0.1:6443"
	})
	header := facts.ProvenanceHeader(time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC))
	values := &helm.Values{NodeAgent: helm.NodeAgent{Enabled: new(true)}}

	buf := &bytes.Buffer{}
	require.NoError(t, WriteValues(buf, values, header...))
	text := buf.String()
	assert.Contains(t, text, "# Generated by telepresence setup ")
	assert.Contains(t, text, "# Cluster: kind-dev (https://127.0.0.1:6443)")
	assert.Contains(t, text, "# Manager namespace: ambassador")
	assert.Contains(t, text, "# Date: 2026-07-19T12:00:00Z")

	// The header must survive an --input round trip: YAML comments are ignored.
	path := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, WriteValuesFile(path, values, header...))
	got, err := LoadInputValues(path)
	require.NoError(t, err)
	assert.Equal(t, values, got)
}

func TestWriteValues_RoundTrip(t *testing.T) {
	values := &helm.Values{
		NodeAgent: helm.NodeAgent{Enabled: new(true)},
		Client:    helm.Client{Cluster: helm.ClientCluster{MappedNamespaces: []string{"foo"}}},
	}
	buf := &bytes.Buffer{}
	require.NoError(t, WriteValues(buf, values))

	got, err := helm.ParseValues(buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, values, got)
}

func TestWriteValuesFile_RoundTrip(t *testing.T) {
	values := &helm.Values{
		NodeAgent:  helm.NodeAgent{Enabled: new(true)},
		Namespaces: []string{"foo", "ambassador"},
	}
	path := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, WriteValuesFile(path, values))

	got, err := LoadInputValues(path)
	require.NoError(t, err)
	assert.Equal(t, values, got)
}
