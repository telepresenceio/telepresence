package setup

import (
	"context"
	"fmt"
	"net/netip"

	corev1 "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

// Subnet sources named in routing conflict evidence.
const (
	sourcePodCIDR     = "pod CIDR"
	sourceServiceCIDR = "service CIDR (estimated)"
)

// RoutingConflict is one overlap between a cluster subnet and a route already
// present on the workstation.
type RoutingConflict struct {
	ClusterSubnet string `json:"clusterSubnet"`
	Source        string `json:"source"` // "pod CIDR" or "service CIDR (estimated)"
	LocalRoute    string `json:"localRoute"`
	Interface     string `json:"interfaceName,omitempty"`
}

// RoutingFacts is the subnet-conflict probe's outcome: a summary finding
// (yes = no conflicts) and the itemized overlaps.
type RoutingFacts struct {
	Summary   Finding           `json:"summary"`
	Conflicts []RoutingConflict `json:"conflicts,omitempty"`
}

// clusterSubnet is a subnet the cluster claims, with the source it was
// derived from.
type clusterSubnet struct {
	prefix netip.Prefix
	source string
}

// probeRouting is P8: it derives the cluster's subnets from the node pod
// CIDRs and the observed Service ClusterIPs, and intersects them with the
// workstation's routing table.
func (p *Prober) probeRouting(ctx context.Context, nodes []corev1.Node) RoutingFacts {
	subnets := podCIDRSubnets(nodes)
	services, _ := p.listServices(ctx)
	subnets = append(subnets, estimatedServiceSubnets(services)...)
	if len(subnets) == 0 {
		return RoutingFacts{Summary: Finding{
			Verdict:  VerdictUnknown,
			Evidence: []string{"no cluster subnets could be determined (node pod CIDRs and service ClusterIPs were unavailable)"},
		}}
	}

	routes, err := p.localRoutes(ctx)
	if err != nil {
		return RoutingFacts{Summary: Finding{
			Verdict:  VerdictUnknown,
			Evidence: []string{fmt.Sprintf("the workstation's routing table could not be read: %v", err)},
		}}
	}

	var conflicts []RoutingConflict
	var evidence []string
	for _, sn := range subnets {
		for _, rt := range routes {
			if !relevantRoute(rt) || !prefixesOverlap(sn.prefix, rt.RoutedNet) {
				continue
			}
			conflicts = append(conflicts, RoutingConflict{
				ClusterSubnet: sn.prefix.String(),
				Source:        sn.source,
				LocalRoute:    rt.RoutedNet.String(),
				Interface:     rt.InterfaceName,
			})
			evidence = append(evidence, fmt.Sprintf("%s (%s) overlaps local route %s dev %s",
				sn.prefix, sn.source, rt.RoutedNet, rt.InterfaceName))
		}
	}
	if len(conflicts) == 0 {
		return RoutingFacts{Summary: Finding{Verdict: VerdictYes}}
	}
	return RoutingFacts{
		Summary:   Finding{Verdict: VerdictNo, Evidence: evidence},
		Conflicts: conflicts,
	}
}

func (p *Prober) localRoutes(ctx context.Context) ([]*routing.Route, error) {
	if p.RouteSource != nil {
		return p.RouteSource(ctx)
	}
	return routing.GetRoutingTable(ctx)
}

// relevantRoute filters out the routes that overlap everything or nothing by
// nature: default routes, loopback, and link-local destinations.
func relevantRoute(rt *routing.Route) bool {
	rn := rt.RoutedNet
	if rt.Default || !rn.IsValid() || rn.Bits() == 0 {
		return false
	}
	addr := rn.Addr()
	return !(addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast())
}

// prefixesOverlap reports whether the two prefixes intersect; for CIDRs this
// holds exactly when one contains the other's base address.
func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// podCIDRSubnets collects the distinct pod CIDRs the nodes declare.
func podCIDRSubnets(nodes []corev1.Node) []clusterSubnet {
	var subnets []clusterSubnet
	seen := map[netip.Prefix]bool{}
	for i := range nodes {
		n := &nodes[i]
		cidrs := n.Spec.PodCIDRs
		if len(cidrs) == 0 && n.Spec.PodCIDR != "" {
			cidrs = []string{n.Spec.PodCIDR}
		}
		for _, c := range cidrs {
			prefix, err := netip.ParsePrefix(c)
			if err != nil || seen[prefix] {
				continue
			}
			seen[prefix] = true
			subnets = append(subnets, clusterSubnet{prefix: prefix, source: sourcePodCIDR})
		}
	}
	return subnets
}

// estimatedServiceSubnets derives a best-effort service CIDR from the
// observed ClusterIPs: the distinct /16 (IPv4) or /112 (IPv6) containers of
// the addresses. Headless services carry no ClusterIP and are skipped.
func estimatedServiceSubnets(services []corev1.Service) []clusterSubnet {
	var subnets []clusterSubnet
	seen := map[netip.Prefix]bool{}
	for i := range services {
		ip := services[i].Spec.ClusterIP
		if ip == "" || ip == "None" {
			continue
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		bits := 16
		if addr.Is6() {
			bits = 112
		}
		prefix, err := addr.Prefix(bits)
		if err != nil || seen[prefix] {
			continue
		}
		seen[prefix] = true
		subnets = append(subnets, clusterSubnet{prefix: prefix, source: sourceServiceCIDR})
	}
	return subnets
}

// ConflictingSubnets returns the distinct cluster subnets involved in the
// conflicts, in first-seen order.
func (r *RoutingFacts) ConflictingSubnets() []string {
	var subnets []string
	seen := map[string]bool{}
	for _, c := range r.Conflicts {
		if !seen[c.ClusterSubnet] {
			seen[c.ClusterSubnet] = true
			subnets = append(subnets, c.ClusterSubnet)
		}
	}
	return subnets
}
