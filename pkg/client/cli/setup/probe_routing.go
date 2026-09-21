package setup

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	core "k8s.io/api/core/v1"

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
	// ActiveSessionSubnets are the local routes that were excluded from
	// conflict detection because they belong to an already-connected
	// Telepresence session rather than to a foreign route.
	ActiveSessionSubnets []string `json:"activeSessionSubnets,omitempty"`
}

// clusterSubnet is a subnet the cluster claims, with the source it was
// derived from.
type clusterSubnet struct {
	prefix netip.Prefix
	source string
}

// probeRouting is P8: it derives the cluster's subnets from the node pod
// CIDRs and the observed Service ClusterIPs, and intersects them with the
// workstation's routing table. services is the single cluster-wide (or
// manager-namespace fallback) Service list GatherFacts fetches once.
func (p *Prober) probeRouting(ctx context.Context, nodes []core.Node, services []core.Service) RoutingFacts {
	subnets := podCIDRSubnets(nodes)
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
	activeSubnets, activeOK := p.activeRoutes(ctx)

	var conflicts []RoutingConflict
	var evidence []string
	var excluded []string
	excludedSeen := map[string]bool{}
	for _, sn := range subnets {
		for _, rt := range routes {
			if !relevantRoute(rt) || !prefixesOverlap(sn.prefix, rt.RoutedNet) {
				continue
			}
			if isActiveSessionRoute(rt, activeSubnets, activeOK) {
				key := rt.RoutedNet.String()
				if !excludedSeen[key] {
					excludedSeen[key] = true
					excluded = append(excluded, key)
				}
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
	if len(excluded) > 0 {
		evidence = append(evidence, exclusionEvidence(excluded))
	}
	if len(conflicts) == 0 {
		return RoutingFacts{Summary: Finding{Verdict: VerdictYes, Evidence: evidence}, ActiveSessionSubnets: excluded}
	}
	return RoutingFacts{
		Summary:              Finding{Verdict: VerdictNo, Evidence: evidence},
		Conflicts:            conflicts,
		ActiveSessionSubnets: excluded,
	}
}

func (p *Prober) localRoutes(ctx context.Context) ([]*routing.Route, error) {
	if p.RouteSource != nil {
		return p.RouteSource(ctx)
	}
	return routing.GetRoutingTable(ctx)
}

// activeRoutes reports the subnets an already-connected Telepresence session
// routes, using Prober.ActiveRoutes when set and defaultActiveRoutes
// otherwise.
func (p *Prober) activeRoutes(ctx context.Context) (subnets []netip.Prefix, ok bool) {
	if p.ActiveRoutes != nil {
		return p.ActiveRoutes(ctx)
	}
	return defaultActiveRoutes(ctx)
}

// isActiveSessionRoute reports whether rt is a route the local Telepresence
// tunnel device owns: either it runs on a "tel"-prefixed device (checked
// regardless of activeOK, since the device name alone identifies it), or its
// routed net is one an active session reports as its own.
func isActiveSessionRoute(rt *routing.Route, activeSubnets []netip.Prefix, activeOK bool) bool {
	if isTelepresenceDevice(rt.InterfaceName) {
		return true
	}
	if !activeOK {
		return false
	}
	for _, sn := range activeSubnets {
		if sn == rt.RoutedNet {
			return true
		}
	}
	return false
}

// isTelepresenceDevice reports whether name is an incarnation of the "tel"
// tunnel device Telepresence creates on Linux and Windows, ignoring its
// trailing instance number (e.g. "tel0", "tel1").
func isTelepresenceDevice(name string) bool {
	return strings.TrimRightFunc(name, func(r rune) bool { return r >= '0' && r <= '9' }) == "tel"
}

// exclusionEvidence describes the local routes that were excluded from
// conflict detection because they belong to an active Telepresence session.
func exclusionEvidence(excluded []string) string {
	return fmt.Sprintf("a Telepresence session is connected; its own routes to %s were not treated as conflicts",
		strings.Join(excluded, ", "))
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
func podCIDRSubnets(nodes []core.Node) []clusterSubnet {
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
func estimatedServiceSubnets(services []core.Service) []clusterSubnet {
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
