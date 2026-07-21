package setup

import (
	"context"
	"fmt"
	"net/url"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// providerSchemes maps a node's spec.providerID scheme to the cloud it names.
var providerSchemes = map[string]string{ //nolint:gochecknoglobals // constant lookup table
	"gce":   "gke",
	"aws":   "eks",
	"azure": "aks",
	"kind":  "kind",
	"k3s":   "k3s",
}

// classifyProvider derives the cluster's provider from the first node whose
// providerID carries a recognized scheme.
func classifyProvider(nodes []corev1.Node) string {
	for _, n := range nodes {
		if n.Spec.ProviderID == "" {
			continue
		}
		u, err := url.Parse(n.Spec.ProviderID)
		if err != nil || u.Scheme == "" {
			continue
		}
		if provider, ok := providerSchemes[u.Scheme]; ok {
			return provider
		}
	}
	return "unknown"
}

func isCloudProvider(provider string) bool {
	switch provider {
	case "gke", "eks", "aks":
		return true
	default:
		return false
	}
}

// probeQuic is P2: it classifies the cluster's provider and decides which
// quicTunnel.service.type is likely to work.
func (p *Prober) probeQuic(ctx context.Context, nodes []corev1.Node, nodesErr error, provider string) QuicFacts {
	facts := QuicFacts{Provider: provider}

	services, listEvidence := p.listServices(ctx)
	lbService := ""
	for _, svc := range services {
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer && len(svc.Status.LoadBalancer.Ingress) > 0 {
			lbService = svc.Namespace + "/" + svc.Name
			break
		}
	}
	switch {
	case lbService != "":
		facts.LoadBalancer = Finding{
			Verdict:  VerdictYes,
			Evidence: append([]string{fmt.Sprintf("existing LoadBalancer service %s has an assigned ingress", lbService)}, listEvidence...),
		}
	case isCloudProvider(provider):
		facts.LoadBalancer = Finding{
			Verdict:  VerdictProbable,
			Evidence: append([]string{fmt.Sprintf("no LoadBalancer service observed yet, but provider %s typically supports them", provider)}, listEvidence...),
		}
	default:
		facts.LoadBalancer = Finding{
			Verdict:  VerdictNo,
			Evidence: append([]string{fmt.Sprintf("no LoadBalancer capability observed (provider: %s)", provider)}, listEvidence...),
		}
	}

	switch {
	case nodesErr != nil:
		facts.NodePort = Finding{Verdict: VerdictUnknown, Evidence: []string{nodesErr.Error()}}
	case nodeAddressCount(nodes) > 0:
		facts.NodePort = Finding{
			Verdict: VerdictProbable,
			Evidence: []string{
				fmt.Sprintf("%d of %d nodes have a usable address", nodeAddressCount(nodes), len(nodes)),
				"NodePort discovery requires the traffic-manager to list nodes (cluster-wide install)",
			},
		}
	default:
		facts.NodePort = Finding{Verdict: VerdictNo, Evidence: []string{"no nodes with a usable address"}}
	}
	return facts
}

// listServices lists Services cluster-wide, falling back to the manager
// namespace alone when the cluster-wide list fails.
func (p *Prober) listServices(ctx context.Context) ([]corev1.Service, []string) {
	list, err := p.KubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{Limit: 500})
	if err == nil {
		return list.Items, nil
	}
	evidence := []string{fmt.Sprintf("cannot list services cluster-wide: %v", err)}
	list, err = p.KubeClient.CoreV1().Services(p.ManagerNamespace).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, append(evidence, err.Error())
	}
	return list.Items, evidence
}

func nodeAddressCount(nodes []corev1.Node) int {
	count := 0
	for _, node := range nodes {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP || addr.Type == corev1.NodeExternalIP {
				count++
				break
			}
		}
	}
	return count
}
