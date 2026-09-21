package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrivilegesSummary(t *testing.T) {
	tests := []struct {
		name    string
		facts   PrivilegeFacts
		verdict Verdict
		summary string
	}{
		{
			name: "cluster-wide yes",
			facts: PrivilegeFacts{
				ClusterWide: Finding{Verdict: VerdictYes},
				Namespaced:  Finding{Verdict: VerdictYes},
			},
			verdict: VerdictYes,
			summary: "cluster-wide install yes, namespaced install yes",
		},
		{
			name: "cluster-wide denied but namespaced viable",
			facts: PrivilegeFacts{
				ClusterWide: Finding{Verdict: VerdictNo},
				Namespaced:  Finding{Verdict: VerdictYes},
			},
			verdict: VerdictYes,
			summary: "cluster-wide install no, namespaced install yes",
		},
		{
			name: "neither viable",
			facts: PrivilegeFacts{
				ClusterWide: Finding{Verdict: VerdictNo, Evidence: []string{"denied: create namespaces"}},
				Namespaced:  Finding{Verdict: VerdictNo},
			},
			verdict: VerdictNo,
			summary: "cluster-wide install no, namespaced install no",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, s, _ := privilegesSummary(&tt.facts)
			assert.Equal(t, tt.verdict, v)
			assert.Equal(t, tt.summary, s)
		})
	}
}

func TestQuicSummary(t *testing.T) {
	q := QuicFacts{
		Provider:     "gke",
		LoadBalancer: Finding{Verdict: VerdictYes},
		NodePort:     Finding{Verdict: VerdictNo},
	}
	v, s, _ := quicSummary(&q)
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "provider GKE, loadBalancer yes, nodePort no", s)

	q2 := QuicFacts{
		LoadBalancer: Finding{Verdict: VerdictNo},
		NodePort:     Finding{Verdict: VerdictNo},
	}
	v2, s2, _ := quicSummary(&q2)
	assert.Equal(t, VerdictNo, v2)
	assert.Equal(t, "provider unknown, loadBalancer no, nodePort no", s2)
}

func TestNodeAgentSummary_HappyPath(t *testing.T) {
	na := NodeAgentFacts{
		Viable:     Finding{Verdict: VerdictYes},
		LinuxNodes: 3,
		TotalNodes: 3,
		Runtimes:   []string{"containerd"},
	}
	v, s, e := nodeAgentSummary(&na)
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "yes (3 of 3 nodes linux, runtimes: containerd)", s)
	assert.Empty(t, e)
}

func TestNodeAgentSummary_AutopilotForbidsHostPID(t *testing.T) {
	na := NodeAgentFacts{
		Viable:     Finding{Verdict: VerdictNo, Evidence: []string{"GKE Autopilot forbids hostPID"}},
		LinuxNodes: 0,
		TotalNodes: 3,
		Autopilot:  true,
	}
	v, s, e := nodeAgentSummary(&na)
	assert.Equal(t, VerdictNo, v)
	assert.Equal(t, "no (0 of 3 nodes linux)", s)
	assert.Contains(t, e, "GKE Autopilot forbids hostPID")

	// The Outcome callback appends the first evidence line to a "no" verdict.
	var gotPhase, gotSummary string
	var gotVerdict Verdict
	p := &Prober{Outcome: func(phase string, verdict Verdict, summary string) {
		gotPhase, gotVerdict, gotSummary = phase, verdict, summary
	}}
	p.outcome("Probing node-agent viability", v, s, e)
	assert.Equal(t, "Probing node-agent viability", gotPhase)
	assert.Equal(t, VerdictNo, gotVerdict)
	assert.Equal(t, "no (0 of 3 nodes linux): GKE Autopilot forbids hostPID", gotSummary)
}

func TestWebhookSummary(t *testing.T) {
	wh := WebhookFacts{CanCreate: Finding{Verdict: VerdictYes}}
	v, s, e := webhookSummary(&wh)
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "create yes", s)
	assert.Empty(t, e)

	wh2 := WebhookFacts{
		CanCreate:           Finding{Verdict: VerdictYes},
		ReachabilityConcern: "EKS with non-VPC CNI may not reach the webhook",
	}
	_, _, e2 := webhookSummary(&wh2)
	assert.Contains(t, e2, "EKS with non-VPC CNI may not reach the webhook")
}

func TestNamespacesSummary(t *testing.T) {
	v, s, _ := namespacesSummary(&NamespaceFacts{Count: 14})
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "14", s)

	v2, s2, _ := namespacesSummary(&NamespaceFacts{ListDenied: true})
	assert.Equal(t, VerdictUnknown, v2)
	assert.Equal(t, "listing denied", s2)

	v3, s3, e3 := namespacesSummary(&NamespaceFacts{ListError: "boom"})
	assert.Equal(t, VerdictUnknown, v3)
	assert.Equal(t, "unknown", s3)
	assert.Contains(t, e3, "boom")
}

func TestRoutingSummary(t *testing.T) {
	v, s, _ := routingSummary(&RoutingFacts{Summary: Finding{Verdict: VerdictYes}})
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "no conflicts", s)

	v2, s2, _ := routingSummary(&RoutingFacts{
		Summary:   Finding{Verdict: VerdictNo},
		Conflicts: []RoutingConflict{{ClusterSubnet: "10.0.0.0/16"}, {ClusterSubnet: "10.1.0.0/16"}},
	})
	assert.Equal(t, VerdictNo, v2)
	assert.Equal(t, "2 conflicts", s2)

	v3, s3, _ := routingSummary(&RoutingFacts{Summary: Finding{Verdict: VerdictUnknown}})
	assert.Equal(t, VerdictUnknown, v3)
	assert.Equal(t, "unknown", s3)
}

func TestReleaseSummary(t *testing.T) {
	v, s, _ := releaseSummary(&ReleaseFacts{Installed: false})
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "not installed", s)

	v2, s2, _ := releaseSummary(&ReleaseFacts{
		Installed: true, Version: "2.31.0", Namespace: "ambassador",
	})
	assert.Equal(t, VerdictYes, v2)
	assert.Equal(t, "traffic-manager 2.31.0 installed in namespace ambassador", s2)

	v3, s3, _ := releaseSummary(&ReleaseFacts{
		Installed: true, Version: "2.31.0", Namespace: "ambassador", Workload: "Deployment",
	})
	assert.Equal(t, VerdictYes, v3)
	assert.Equal(t, "traffic-manager 2.31.0 installed in namespace ambassador, running as a Deployment", s3)
}

func TestClientUpdateSummary(t *testing.T) {
	v, s, _ := clientUpdateSummary(&UpdateFacts{})
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "up to date", s)

	v2, s2, _ := clientUpdateSummary(&UpdateFacts{UpdateAvailable: true, Latest: "2.32.0"})
	assert.Equal(t, VerdictProbable, v2)
	assert.Contains(t, s2, "2.32.0 available")

	v3, s3, e3 := clientUpdateSummary(&UpdateFacts{CheckError: "network unreachable"})
	assert.Equal(t, VerdictUnknown, v3)
	assert.Equal(t, "check failed", s3)
	assert.Contains(t, e3, "network unreachable")
}

func TestExternalSummary(t *testing.T) {
	v, s, _ := externalSummary(&ExternalFacts{CertManager: Finding{Verdict: VerdictNo}})
	assert.Equal(t, VerdictNo, v)
	assert.Equal(t, "cert-manager no, 0 TLS secrets", s)

	v2, s2, _ := externalSummary(&ExternalFacts{
		CertManager: Finding{Verdict: VerdictYes},
		TLSSecrets:  []TLSSecretFacts{{Name: "my-cert"}},
	})
	assert.Equal(t, VerdictYes, v2)
	assert.Equal(t, "cert-manager yes, 1 TLS secrets", s2)

	v3, s3, _ := externalSummary(&ExternalFacts{
		CertManager:       Finding{Verdict: VerdictNo},
		SecretsListDenied: true,
	})
	assert.Equal(t, VerdictNo, v3)
	assert.Equal(t, "cert-manager no, TLS secrets: listing denied", s3)
}

func TestHealthSummary(t *testing.T) {
	v, s, _ := healthSummary(&ReleaseFacts{Installed: false}, nil)
	assert.Equal(t, VerdictYes, v)
	assert.Equal(t, "not installed", s)

	allReady := &HealthFacts{
		ManagerReady:   Finding{Verdict: VerdictYes},
		VersionSkew:    Finding{Verdict: VerdictYes},
		Webhook:        &Finding{Verdict: VerdictYes},
		Certificate:    &Finding{Verdict: VerdictYes},
		X509ClientAuth: &Finding{Verdict: VerdictYes},
	}
	v2, s2, _ := healthSummary(&ReleaseFacts{Installed: true}, allReady)
	assert.Equal(t, VerdictYes, v2)
	assert.Equal(t, "ready", s2)

	unready := &HealthFacts{
		ManagerReady: Finding{Verdict: VerdictNo, Evidence: []string{"0 of 1 replicas ready"}},
		VersionSkew:  Finding{Verdict: VerdictYes},
	}
	v3, s3, e3 := healthSummary(&ReleaseFacts{Installed: true}, unready)
	assert.Equal(t, VerdictNo, v3)
	assert.Equal(t, "traffic-manager no", s3)
	assert.Contains(t, e3, "0 of 1 replicas ready")

	skewed := &HealthFacts{
		ManagerReady: Finding{Verdict: VerdictYes},
		VersionSkew:  Finding{Verdict: VerdictUnknown, Evidence: []string{"could not compare versions"}},
	}
	v4, s4, _ := healthSummary(&ReleaseFacts{Installed: true}, skewed)
	assert.Equal(t, VerdictUnknown, v4)
	assert.Equal(t, "version skew unknown", s4)
}
