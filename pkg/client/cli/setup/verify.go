package setup

import (
	"context"
	"fmt"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

// quicServiceName is the chart's fixed name for the Service fronting the QUIC
// forwarder (the traffic-manager.quicServiceName template helper).
const quicServiceName = "traffic-manager-quic"

// injectorServiceName is the chart's default agentInjector.name, used to look
// up the Service when the values leave it unset.
const injectorServiceName = helm.DefaultInjectorName

const (
	verifyTimeout  = 30 * time.Second
	quicLBInterval = 2 * time.Second
)

// VerifyInstall performs the best-effort post-apply checks that Atomic/Wait
// does not cover: the QUIC endpoint, the external TLS control endpoint, and
// the agent-injector Service. Findings are notes, never errors, and the
// whole verification is capped in time.
func VerifyInstall(ctx context.Context, ki kubernetes.Interface, managerNamespace string, values *helm.Values, auth ClientAuthFacts) []Note {
	return verifyInstall(ctx, ki, managerNamespace, values, auth, quicGoDial, externalGRPCProbe)
}

// verifyInstall is VerifyInstall with the QUIC reachability dialer and the
// external-endpoint prober factored out as parameters so tests can exercise
// the classification logic without opening real sockets.
func verifyInstall(
	ctx context.Context, ki kubernetes.Interface, managerNamespace string, values *helm.Values, auth ClientAuthFacts,
	dial quicDialer, extProbe externalProber,
) []Note {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	var notes []Note
	if deref(values.QuicTunnel.Enabled) {
		notes = append(notes, noteFromFinding(verifyQuic(ctx, ki, managerNamespace, dial)))
	}
	if deref(values.ExternalEndpoint.Enabled) {
		notes = append(notes, verifyExternalEndpoint(ctx, ki, managerNamespace, values, auth, extProbe)...)
	}
	// The chart enables the agent-injector by default, so the check runs
	// unless the values disable it explicitly.
	if p := values.AgentInjector.Enabled; p == nil || *p {
		notes = append(notes, noteFromFinding(injectorEndpointsFinding(ctx, ki, managerNamespace, values.InjectorName())))
	}
	if values.AuthEnforced() {
		notes = append(notes, authEnforcedNote(values, auth))
	}
	return notes
}

// authEnforcedNote reports how this client will authenticate against a
// manager that enforces authentication: its bearer token when it has one,
// the manager's x509 listener when the values leave it enabled, or a
// warning when the client has no credential the manager will accept.
func authEnforcedNote(values *helm.Values, auth ClientAuthFacts) Note {
	switch {
	case auth.Bearer:
		return Note{Level: NoteInfo, Text: "authentication is enforced; this client authenticates with its kubeconfig's bearer token"}
	case auth.X509 && x509AuthEnabled(values):
		return Note{Level: NoteInfo, Text: "authentication is enforced; this client authenticates with its kubeconfig's client certificate via the manager's x509 listener"}
	case auth.X509:
		return Note{Level: NoteWarning, Text: "authentication is enforced, but this client's kubeconfig only produces " +
			"a client certificate and x509 client authentication is disabled; it will be rejected"}
	default:
		return Note{Level: NoteWarning, Text: "authentication is enforced, but this client's kubeconfig produces neither a bearer token nor a client certificate; it will be rejected"}
	}
}

// noteFromFinding renders a shared check's finding as a post-apply
// verification note.
func noteFromFinding(f Finding) Note {
	level := NoteInfo
	if f.Verdict != VerdictYes {
		level = NoteWarning
	}
	return Note{Level: level, Text: strings.Join(f.Evidence, "; ")}
}

// verifyQuic polls the single-look QUIC check while a LoadBalancer is still
// provisioning, bounded by ctx, then -- once the endpoint exists -- attempts
// one QUIC handshake to it, replacing the existence finding with the
// reachability verdict: existence alone does not mean this workstation can
// actually reach it over UDP.
func verifyQuic(ctx context.Context, ki kubernetes.Interface, namespace string, dial quicDialer) Finding {
	f, retryLB, svc := quicServiceFinding(ctx, ki, namespace)
	if retryLB {
		ticker := time.NewTicker(quicLBInterval)
		defer ticker.Stop()
		for retryLB {
			select {
			case <-ctx.Done():
				return f
			case <-ticker.C:
			}
			f, retryLB, svc = quicServiceFinding(ctx, ki, namespace)
		}
	}
	if f.Verdict != VerdictYes {
		return f
	}
	return verifyQuicReachability(ctx, ki, svc, dial)
}

// quicServiceFinding is a single look at the QUIC Service: does it offer
// something clients can reach? retryLB is true when a LoadBalancer exists
// whose ingress has not been assigned yet, so a caller with time to spare may
// look again. The Service itself is returned alongside a VerdictYes finding
// so a caller can resolve a reachability dial address from it without
// re-fetching.
func quicServiceFinding(ctx context.Context, ki kubernetes.Interface, namespace string) (Finding, bool, *core.Service) {
	svc, err := ki.CoreV1().Services(namespace).Get(ctx, quicServiceName, meta.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the QUIC service %s was not found in namespace %s: %v", quicServiceName, namespace, err)}}, false, nil
	case err != nil:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the QUIC service %s could not be read: %v", quicServiceName, err)}}, false, nil
	}
	switch svc.Spec.Type {
	case core.ServiceTypeLoadBalancer:
		if addr := firstLoadBalancerIngressAddr(svc); addr != "" {
			return Finding{Verdict: VerdictYes, Evidence: []string{"QUIC endpoint available at " + addr}}, false, svc
		}
		return Finding{Verdict: VerdictNo, Evidence: []string{
			"no QUIC endpoint yet — clients will fall back to gRPC (the LoadBalancer may still be provisioning)",
		}}, true, nil
	case core.ServiceTypeNodePort:
		if np, ok := firstAllocatedNodePort(svc); ok {
			return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("QUIC endpoint allocated node port %d", np)}}, false, svc
		}
		return Finding{Verdict: VerdictNo, Evidence: []string{
			"the QUIC service has no allocated node port yet — clients will fall back to gRPC",
		}}, false, nil
	default:
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the QUIC service has type %s; endpoint discovery has nothing externally reachable to advertise", svc.Spec.Type)}}, false, nil
	}
}

// injectorEndpointsFinding checks that the agent-injector Service has ready
// endpoints. An injector canary (an annotated dry-run pod) is deliberately
// not attempted: the injector skips any pod without a supported workload
// owner, so a standalone canary always comes back unmutated regardless of
// webhook health.
func injectorEndpointsFinding(ctx context.Context, ki kubernetes.Interface, namespace, name string) Finding {
	slices, err := ki.DiscoveryV1().EndpointSlices(namespace).List(ctx, meta.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + name,
	})
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the %s service endpoints could not be read (%v); the webhook's failurePolicy Ignore means a broken injector degrades silently",
			name, err)}}
	}
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			if r := ep.Conditions.Ready; (r == nil || *r) && len(ep.Addresses) > 0 {
				return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("the %s service has ready endpoints", name)}}
			}
		}
	}
	return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
		"the %s service has no ready endpoints yet; the webhook's failurePolicy Ignore means a broken injector degrades silently",
		name)}}
}
