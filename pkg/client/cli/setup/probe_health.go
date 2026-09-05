package setup

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/eventwatch"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// managerStatefulSetName is the chart's fixed traffic-manager StatefulSet name; older
// chart versions run the traffic-manager as a Deployment of the same name instead.
const managerStatefulSetName = "traffic-manager"

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
	ExternalEndpoint  *Finding `json:"externalEndpoint,omitempty"`
	VersionSkew       Finding  `json:"versionSkew"`
}

// probeHealth runs the read-only doctor checks over the installed release. mw is the
// single manager-workload fetch GatherFacts made when it saw the release installed;
// every other check is one-shot and tolerates denials as VerdictUnknown.
func (p *Prober) probeHealth(ctx context.Context, rel *ReleaseFacts, auth ClientAuthFacts, mw managerWorkloadResult) *HealthFacts {
	h := &HealthFacts{
		ManagerReady: p.managerReadyFinding(ctx, mw),
		VersionSkew:  rel.versionSkew(),
	}
	// The chart enables the agent-injector by default, so the checks run
	// unless the release's values disable it explicitly.
	if injEnabled := rel.Values.AgentInjector.Enabled; injEnabled == nil || *injEnabled {
		webhook, certificate := p.healthWebhook(ctx)
		h.Webhook = &webhook
		if certificate != nil {
			h.Certificate = certificate
		}
		endpoints := injectorEndpointsFinding(ctx, p.KubeClient, p.ManagerNamespace, rel.Values.InjectorName())
		h.InjectorEndpoints = &endpoints
	}
	if deref(rel.Values.QuicTunnel.Enabled) {
		quic, _, _ := quicServiceFinding(ctx, p.KubeClient, p.ManagerNamespace)
		h.Quic = &quic
	}
	if rel.Values.AuthEnforced() {
		x509 := rel.x509ClientAuth(auth)
		h.X509ClientAuth = &x509
	}
	if deref(rel.Values.ExternalEndpoint.Enabled) {
		ext := p.healthExternalEndpoint(ctx)
		h.ExternalEndpoint = &ext
	}
	return h
}

// managerWorkloadResult is a single read of whichever object hosts the
// traffic-manager: the StatefulSet the chart installs today, or the
// Deployment an older chart version left behind. GatherFacts fetches it once
// and derives both ReleaseFacts.Workload and the ManagerReady health finding
// from it.
type managerWorkloadResult struct {
	Kind      string // "StatefulSet" or "Deployment"; empty when neither was readable
	Desired   int32
	Ready     int32
	NotFound  bool
	ReadError error
}

// managerWorkload fetches the traffic-manager StatefulSet, falling back to the
// Deployment older chart versions used.
func (p *Prober) managerWorkload(ctx context.Context) managerWorkloadResult {
	sts, err := p.KubeClient.AppsV1().StatefulSets(p.ManagerNamespace).Get(ctx, managerStatefulSetName, meta.GetOptions{})
	switch {
	case err == nil:
		return managerWorkloadResult{Kind: "StatefulSet", Desired: replicaCount(sts.Spec.Replicas), Ready: sts.Status.ReadyReplicas}
	case !apierrors.IsNotFound(err):
		return managerWorkloadResult{ReadError: fmt.Errorf("the %s statefulset could not be read: %w", managerStatefulSetName, err)}
	}
	dep, err := p.KubeClient.AppsV1().Deployments(p.ManagerNamespace).Get(ctx, managerStatefulSetName, meta.GetOptions{})
	switch {
	case err == nil:
		return managerWorkloadResult{Kind: "Deployment", Desired: replicaCount(dep.Spec.Replicas), Ready: dep.Status.ReadyReplicas}
	case apierrors.IsNotFound(err):
		return managerWorkloadResult{NotFound: true}
	default:
		return managerWorkloadResult{ReadError: fmt.Errorf("the %s deployment could not be read: %w", managerStatefulSetName, err)}
	}
}

// replicaCount resolves a workload's desired replica count, defaulting to 1 when the
// spec leaves it unset.
func replicaCount(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
}

// managerReadyFinding classifies mw's replica readiness and, when it is
// unready, attaches the Warning events the API server still retains for the
// release; a Deployment result also carries the migration note.
func (p *Prober) managerReadyFinding(ctx context.Context, mw managerWorkloadResult) Finding {
	switch {
	case mw.ReadError != nil:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{mw.ReadError.Error()}}
	case mw.NotFound:
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the %s statefulset was not found in namespace %s", managerStatefulSetName, p.ManagerNamespace)}}
	}
	f := p.workloadReadiness(ctx, strings.ToLower(mw.Kind), mw.Desired, mw.Ready)
	if mw.Kind == "Deployment" {
		f.Evidence = append([]string{"the traffic-manager still runs as a Deployment; the upgrade migrates it to a StatefulSet"}, f.Evidence...)
	}
	return f
}

// workloadReadiness classifies a workload's replica readiness and, when it
// is unready, attaches the Warning events the API server still retains for
// the release.
func (p *Prober) workloadReadiness(ctx context.Context, kind string, desired, ready int32) Finding {
	if desired == 0 {
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf("the %s is scaled to zero replicas", kind)}}
	}
	if ready >= desired {
		return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("%d of %d replicas ready", ready, desired)}}
	}
	evidence := []string{fmt.Sprintf("%d of %d replicas ready", ready, desired)}
	if es, err := eventwatch.ListWarnings(ctx, p.KubeClient, p.ManagerNamespace, managerStatefulSetName); err == nil {
		for _, e := range es {
			evidence = append(evidence, fmt.Sprintf("%s: %s", e.Reason, e.Note))
			if len(evidence) > healthEventMax {
				break
			}
		}
	}
	return Finding{Verdict: VerdictNo, Evidence: evidence}
}

// healthExternalEndpoint reports whether the external control endpoint's
// Service is available, reusing the same single-look check the post-apply
// verification runs. Only a missing Service or an unallocated NodePort are
// "no"; anything else inconclusive (still provisioning, ClusterIP, a denied
// or failed read, an unsupported type) is "unknown", not "no".
func (p *Prober) healthExternalEndpoint(ctx context.Context) Finding {
	kind, note, svc := externalServiceLook(ctx, p.KubeClient, p.ManagerNamespace)
	switch {
	case note == nil:
		return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf(
			"the %s service is %s, address %s", externalServiceName, svc.Spec.Type, externalServiceAddr(svc))}}
	case kind == externalServiceNotFound || kind == externalServiceNoNodePort:
		return Finding{Verdict: VerdictNo, Evidence: []string{note.Text}}
	default:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{note.Text}}
	}
}

// externalServiceAddr describes svc's externally reachable address for
// evidence text: a LoadBalancer's ingress address, or the allocated node
// port for a NodePort Service.
func externalServiceAddr(svc *core.Service) string {
	switch svc.Spec.Type {
	case core.ServiceTypeLoadBalancer:
		return firstLoadBalancerIngressAddr(svc)
	case core.ServiceTypeNodePort:
		if np, ok := firstAllocatedNodePort(svc); ok {
			return fmt.Sprintf("node port %d", np)
		}
	}
	return "unknown"
}

// healthWebhook checks that the agent-injector's MutatingWebhookConfiguration
// exists, and when it does, that its CA bundle certificate is not expired or
// about to expire.
func (p *Prober) healthWebhook(ctx context.Context) (Finding, *Finding) {
	name := webhookConfigurationPrefix + p.ManagerNamespace
	mwc, err := p.KubeClient.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, name, meta.GetOptions{})
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

// x509ClientAuth checks, under enforcing mode, whether this client's own
// credentials remain usable: a bearer token always is, a client certificate
// needs the manager's x509 listener enabled, and a kubeconfig producing
// neither is rejected outright.
func (r *ReleaseFacts) x509ClientAuth(auth ClientAuthFacts) Finding {
	p := r.Values.Security.Authentication.X509.Enabled
	present := p != nil
	x509Enabled := present && *p
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

// versionSkew relates the installed release's version to the client's: the
// two are meant to be kept in lockstep, and an upgrade always moves the
// older side forward.
func (r *ReleaseFacts) versionSkew() Finding {
	facts := &ClusterFacts{Release: *r}
	switch facts.releaseAge(version.Structured) {
	case releaseSame:
		return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("client and traffic-manager are both %s", r.Version)}}
	case releaseOlder:
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"traffic-manager %s is older than this client (%s); run 'telepresence setup --apply' or 'telepresence helm upgrade' to upgrade it",
			r.Version, version.Version)}}
	case releaseNewer:
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the installed traffic-manager %s is newer than this client (%s); upgrade the client instead of downgrading the traffic-manager",
			r.Version, version.Version)}}
	default:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the installed traffic-manager version %q could not be parsed", r.Version)}}
	}
}
