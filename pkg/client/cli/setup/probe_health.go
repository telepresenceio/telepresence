package setup

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/eventwatch"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// managerDeploymentName is the chart's fixed traffic-manager Deployment name.
const managerDeploymentName = "traffic-manager"

// webhookConfigurationPrefix combined with the manager namespace names the
// chart's MutatingWebhookConfiguration (agentInjector.webhook.name).
const webhookConfigurationPrefix = "agent-injector-webhook-"

// certExpiryWarning is how close to its NotAfter the webhook certificate may
// get before the health check flags it.
const certExpiryWarning = 30 * 24 * time.Hour

// healthEventMax caps how many Warning events a single finding carries.
const healthEventMax = 5

// HealthFacts are the read-only doctor checks over an existing installation.
// Optional findings are present only when the release values enable the
// feature they check.
type HealthFacts struct {
	ManagerReady      Finding  `json:"managerReady"`
	Webhook           *Finding `json:"webhook,omitempty"`
	Certificate       *Finding `json:"certificate,omitempty"`
	InjectorEndpoints *Finding `json:"injectorEndpoints,omitempty"`
	Quic              *Finding `json:"quic,omitempty"`
	X509ClientAuth    *Finding `json:"x509ClientAuth,omitempty"`
	VersionSkew       Finding  `json:"versionSkew"`
}

// Clean reports whether no health check concluded "no"; a nil receiver (no
// installed release, hence no checks) is clean.
func (h *HealthFacts) Clean() bool {
	if h == nil {
		return true
	}
	for _, f := range []*Finding{&h.ManagerReady, h.Webhook, h.Certificate, h.InjectorEndpoints, h.Quic, h.X509ClientAuth, &h.VersionSkew} {
		if f != nil && f.Verdict == VerdictNo {
			return false
		}
	}
	return true
}

// probeHealth runs the read-only doctor checks over the installed release.
// Every check is one-shot and tolerates denials as VerdictUnknown.
func (p *Prober) probeHealth(ctx context.Context, rel *ReleaseFacts, auth ClientAuthFacts) *HealthFacts {
	h := &HealthFacts{
		ManagerReady: p.healthManager(ctx),
		VersionSkew:  healthVersionSkew(rel),
	}
	if enabled, present := boolAt(rel.Values, "agentInjector", "enabled"); present && enabled {
		webhook, certificate := p.healthWebhook(ctx)
		h.Webhook = &webhook
		if certificate != nil {
			h.Certificate = certificate
		}
		endpoints := injectorEndpointsFinding(ctx, p.KubeClient, p.ManagerNamespace)
		h.InjectorEndpoints = &endpoints
	}
	if enabled, present := boolAt(rel.Values, "quicTunnel", "enabled"); present && enabled {
		quic, _, _ := quicServiceFinding(ctx, p.KubeClient, p.ManagerNamespace)
		h.Quic = &quic
	}
	if mode, _ := valueAt(rel.Values, "security", "authentication", "mode"); mode == "enforcing" {
		x509 := healthX509ClientAuth(rel, auth)
		h.X509ClientAuth = &x509
	}
	return h
}

// healthManager checks the traffic-manager Deployment's readiness and, when
// it is unready, attaches the Warning events the API server still retains for
// the release.
func (p *Prober) healthManager(ctx context.Context) Finding {
	dep, err := p.KubeClient.AppsV1().Deployments(p.ManagerNamespace).Get(ctx, managerDeploymentName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the %s deployment was not found in namespace %s", managerDeploymentName, p.ManagerNamespace)}}
	case err != nil:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the %s deployment could not be read: %v", managerDeploymentName, err)}}
	}
	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	if desired == 0 {
		return Finding{Verdict: VerdictNo, Evidence: []string{"the deployment is scaled to zero replicas"}}
	}
	ready := dep.Status.ReadyReplicas
	if ready >= desired {
		return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("%d of %d replicas ready", ready, desired)}}
	}
	evidence := []string{fmt.Sprintf("%d of %d replicas ready", ready, desired)}
	if es, err := eventwatch.ListWarnings(ctx, p.KubeClient, p.ManagerNamespace, managerDeploymentName); err == nil {
		for _, e := range es {
			evidence = append(evidence, fmt.Sprintf("%s: %s", e.Reason, e.Note))
			if len(evidence) > healthEventMax {
				break
			}
		}
	}
	return Finding{Verdict: VerdictNo, Evidence: evidence}
}

// healthWebhook checks that the agent-injector's MutatingWebhookConfiguration
// exists, and when it does, that its CA bundle certificate is not expired or
// about to expire.
func (p *Prober) healthWebhook(ctx context.Context) (Finding, *Finding) {
	name := webhookConfigurationPrefix + p.ManagerNamespace
	mwc, err := p.KubeClient.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the MutatingWebhookConfiguration %s was not found although the release enables the agent-injector", name)}}, nil
	case err != nil:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the MutatingWebhookConfiguration %s could not be read: %v", name, err)}}, nil
	}
	presence := Finding{Verdict: VerdictYes, Evidence: []string{"MutatingWebhookConfiguration " + name + " is present"}}
	if len(mwc.Webhooks) == 0 {
		cert := Finding{Verdict: VerdictUnknown, Evidence: []string{"the configuration contains no webhooks"}}
		return presence, &cert
	}
	cert := certificateFinding(mwc.Webhooks[0].ClientConfig.CABundle, time.Now())
	return presence, &cert
}

// certificateFinding classifies the webhook CA bundle's expiry.
func certificateFinding(caBundle []byte, now time.Time) Finding {
	block, _ := pem.Decode(caBundle)
	if block == nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{"the webhook CA bundle contains no PEM certificate"}}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf("the webhook CA bundle certificate could not be parsed: %v", err)}}
	}
	notAfter := cert.NotAfter.Format(time.RFC3339)
	switch {
	case now.After(cert.NotAfter):
		return Finding{Verdict: VerdictNo, Evidence: []string{"the webhook certificate expired " + notAfter}}
	case now.Add(certExpiryWarning).After(cert.NotAfter):
		return Finding{Verdict: VerdictNo, Evidence: []string{"the webhook certificate expires within 30 days: " + notAfter}}
	default:
		return Finding{Verdict: VerdictYes, Evidence: []string{"the webhook certificate is valid until " + notAfter}}
	}
}

// healthX509ClientAuth checks, under enforcing mode, whether this client's
// own credentials remain usable: a bearer token always is, a client
// certificate needs the manager's x509 listener enabled, and a kubeconfig
// producing neither is rejected outright.
func healthX509ClientAuth(rel *ReleaseFacts, auth ClientAuthFacts) Finding {
	x509Enabled, present := boolAt(rel.Values, "security", "authentication", "x509", "enabled")
	switch {
	case auth.Bearer:
		return Finding{Verdict: VerdictYes}
	case auth.X509 && (!present || x509Enabled):
		return Finding{Verdict: VerdictYes}
	case auth.X509:
		return Finding{Verdict: VerdictNo, Evidence: []string{
			"this kubeconfig authenticates with a client certificate, but the installed traffic-manager enforces " +
				"authentication with x509 client authentication disabled; this client will be rejected",
		}}
	default:
		return Finding{Verdict: VerdictNo, Evidence: []string{
			"this kubeconfig produces neither a bearer token nor a client certificate, and the installed traffic-manager enforces authentication; this client will be rejected",
		}}
	}
}

// healthVersionSkew relates the installed release's version to the client's:
// the two are meant to be kept in lockstep, and an upgrade always moves the
// older side forward.
func healthVersionSkew(rel *ReleaseFacts) Finding {
	facts := &ClusterFacts{Release: *rel}
	switch compareRelease(facts, version.Structured) {
	case releaseSame:
		return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("client and traffic-manager are both %s", rel.Version)}}
	case releaseOlder:
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"traffic-manager %s is older than this client (%s); run 'telepresence setup --apply' or 'telepresence helm upgrade' to upgrade it",
			rel.Version, version.Version)}}
	case releaseNewer:
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the installed traffic-manager %s is newer than this client (%s); upgrade the client instead of downgrading the traffic-manager",
			rel.Version, version.Version)}}
	default:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the installed traffic-manager version %q could not be parsed", rel.Version)}}
	}
}
