package setup

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// quicServiceName is the chart's fixed name for the Service fronting the QUIC
// forwarder (the traffic-manager.quicServiceName template helper).
const quicServiceName = "traffic-manager-quic"

// injectorServiceName is the chart's default agentInjector.name.
const injectorServiceName = "agent-injector"

const (
	verifyTimeout  = 30 * time.Second
	quicLBInterval = 2 * time.Second
)

// VerifyInstall performs the best-effort post-apply checks that Atomic/Wait
// does not cover: the QUIC endpoint and the agent-injector Service. Findings
// are notes, never errors, and the whole verification is capped in time.
func VerifyInstall(ctx context.Context, ki kubernetes.Interface, managerNamespace string, values map[string]any) []Note {
	return verifyInstall(ctx, ki, managerNamespace, values, quicGoDial)
}

// verifyInstall is VerifyInstall with the QUIC reachability dialer factored
// out as a parameter so tests can exercise the classification logic without
// opening real sockets.
func verifyInstall(ctx context.Context, ki kubernetes.Interface, managerNamespace string, values map[string]any, dial quicDialer) []Note {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	var notes []Note
	if enabled, present := boolAt(values, "quicTunnel", "enabled"); present && enabled {
		notes = append(notes, noteFromFinding(verifyQuic(ctx, ki, managerNamespace, dial)))
	}
	if enabled, present := boolAt(values, "agentInjector", "enabled"); present && enabled {
		notes = append(notes, noteFromFinding(injectorEndpointsFinding(ctx, ki, managerNamespace)))
	}
	return notes
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
func quicServiceFinding(ctx context.Context, ki kubernetes.Interface, namespace string) (Finding, bool, *corev1.Service) {
	svc, err := ki.CoreV1().Services(namespace).Get(ctx, quicServiceName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
			"the QUIC service %s was not found in namespace %s: %v", quicServiceName, namespace, err)}}, false, nil
	case err != nil:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the QUIC service %s could not be read: %v", quicServiceName, err)}}, false, nil
	}
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			addr := ing.IP
			if addr == "" {
				addr = ing.Hostname
			}
			if addr != "" {
				return Finding{Verdict: VerdictYes, Evidence: []string{"QUIC endpoint available at " + addr}}, false, svc
			}
		}
		return Finding{Verdict: VerdictNo, Evidence: []string{
			"no QUIC endpoint yet — clients will fall back to gRPC (the LoadBalancer may still be provisioning)",
		}}, true, nil
	case corev1.ServiceTypeNodePort:
		for _, p := range svc.Spec.Ports {
			if p.NodePort != 0 {
				return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("QUIC endpoint allocated node port %d", p.NodePort)}}, false, svc
			}
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
func injectorEndpointsFinding(ctx context.Context, ki kubernetes.Interface, namespace string) Finding {
	slices, err := ki.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + injectorServiceName,
	})
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{fmt.Sprintf(
			"the %s service endpoints could not be read (%v); the webhook's failurePolicy Ignore means a broken injector degrades silently",
			injectorServiceName, err)}}
	}
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			if r := ep.Conditions.Ready; (r == nil || *r) && len(ep.Addresses) > 0 {
				return Finding{Verdict: VerdictYes, Evidence: []string{fmt.Sprintf("the %s service has ready endpoints", injectorServiceName)}}
			}
		}
	}
	return Finding{Verdict: VerdictNo, Evidence: []string{fmt.Sprintf(
		"the %s service has no ready endpoints yet; the webhook's failurePolicy Ignore means a broken injector degrades silently",
		injectorServiceName)}}
}
