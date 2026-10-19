//go:build linux

package routenft

import (
	"net/netip"

	"github.com/google/nftables"
)

// Config describes the blackhole ruleset for a single address family. A
// mixed-family CIDR list (e.g. dual-stack service CIDRs) is built as two
// Configs, one per family: see cmd/routecontroller, which splits the
// discovered CIDRs before calling Build.
type Config struct {
	// Family selects the nftables table family (ip or ip6) for this ruleset.
	Family nftables.TableFamily

	// ServiceCIDRs are the service CIDRs to blackhole. Every entry must
	// belong to Family; Build returns an error otherwise.
	ServiceCIDRs []netip.Prefix

	// PodCIDRs are the pod CIDRs assigned to this node. Traffic to them is
	// dropped when it leaves through one of the Uplinks, since a pod that
	// exists is reached through its own interface route and never through
	// the node's default route.
	PodCIDRs []netip.Prefix

	// Uplinks are the names of the interfaces that carry the node's default
	// routes. Required when PodCIDRs is non-empty.
	Uplinks []string
}

const (
	// TableName is the name of the dedicated nftables table this package
	// manages. It is the same name pkg/agentnft uses, but the two are
	// unrelated tables in different network namespaces: routenft's table
	// lives in the route-controller's (host) namespace, agentnft's in a
	// pod's.
	TableName = "telepresence"

	chainForward = "forward"

	// setServiceCIDRs is the name of the interval set holding the blackholed
	// service CIDRs. The same name is used in both the ip and ip6 tables;
	// they never collide because each address family gets its own table.
	setServiceCIDRs = "service_cidrs"

	// setPodCIDRs is the name of the interval set holding the node's pod
	// CIDRs.
	setPodCIDRs = "pod_cidrs"
)
