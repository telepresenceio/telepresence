package setup

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
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
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	var notes []Note
	if enabled, present := boolAt(values, "quicTunnel", "enabled"); present && enabled {
		notes = append(notes, verifyQuic(ctx, ki, managerNamespace))
	}
	if enabled, present := boolAt(values, "agentInjector", "enabled"); present && enabled {
		notes = append(notes, verifyInjectorService(ctx, ki, managerNamespace))
	}
	return notes
}

// verifyQuic checks that the QUIC Service has something clients can reach: an
// assigned LoadBalancer ingress (polling, since provisioning takes a while)
// or an allocated NodePort.
func verifyQuic(ctx context.Context, ki kubernetes.Interface, namespace string) Note {
	svc, err := ki.CoreV1().Services(namespace).Get(ctx, quicServiceName, metav1.GetOptions{})
	if err != nil {
		return Note{Level: NoteWarning, Text: fmt.Sprintf("the QUIC service %s was not found in namespace %s: %v", quicServiceName, namespace, err)}
	}
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		return verifyQuicLoadBalancer(ctx, ki, namespace, svc)
	case corev1.ServiceTypeNodePort:
		for _, p := range svc.Spec.Ports {
			if p.NodePort != 0 {
				return Note{Level: NoteInfo, Text: fmt.Sprintf("QUIC endpoint allocated node port %d", p.NodePort)}
			}
		}
		return Note{Level: NoteWarning, Text: "the QUIC service has no allocated node port yet — clients will fall back to gRPC"}
	default:
		return Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the QUIC service has type %s; endpoint discovery has nothing externally reachable to advertise", svc.Spec.Type)}
	}
}

func verifyQuicLoadBalancer(ctx context.Context, ki kubernetes.Interface, namespace string, svc *corev1.Service) Note {
	ticker := time.NewTicker(quicLBInterval)
	defer ticker.Stop()
	for {
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			addr := ing.IP
			if addr == "" {
				addr = ing.Hostname
			}
			if addr != "" {
				return Note{Level: NoteInfo, Text: fmt.Sprintf("QUIC endpoint available at %s", addr)}
			}
		}
		select {
		case <-ctx.Done():
			return Note{Level: NoteWarning, Text: "no QUIC endpoint yet — clients will fall back to gRPC (the LoadBalancer may still be provisioning)"}
		case <-ticker.C:
		}
		var err error
		if svc, err = ki.CoreV1().Services(namespace).Get(ctx, quicServiceName, metav1.GetOptions{}); err != nil {
			return Note{Level: NoteWarning, Text: fmt.Sprintf("the QUIC service %s could not be re-read: %v", quicServiceName, err)}
		}
	}
}

// verifyInjectorService checks that the agent-injector Service has ready
// endpoints. An injector canary (an annotated dry-run pod) is deliberately
// not attempted: the injector skips any pod without a supported workload
// owner, so a standalone canary always comes back unmutated regardless of
// webhook health.
func verifyInjectorService(ctx context.Context, ki kubernetes.Interface, namespace string) Note {
	slices, err := ki.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + injectorServiceName,
	})
	if err != nil {
		return Note{Level: NoteWarning, Text: fmt.Sprintf(
			"the %s service endpoints could not be read (%v); the webhook's failurePolicy Ignore means a broken injector degrades silently", injectorServiceName, err)}
	}
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			if r := ep.Conditions.Ready; (r == nil || *r) && len(ep.Addresses) > 0 {
				return Note{Level: NoteInfo, Text: fmt.Sprintf("the %s service has ready endpoints", injectorServiceName)}
			}
		}
	}
	return Note{Level: NoteWarning, Text: fmt.Sprintf(
		"the %s service has no ready endpoints yet; the webhook's failurePolicy Ignore means a broken injector degrades silently", injectorServiceName)}
}
