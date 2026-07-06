//go:build linux

package agentnft

import (
	"net/netip"

	"github.com/google/nftables"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// Intercept describes a single container-port -> agent-port redirect for a
// given protocol. For numeric target ports, ProxyPort is also set: the agent's
// forwarder writes to it (instead of to ContainerPort directly) when the app
// container is otherwise unreachable, relying on the DNAT rule built from this
// field to reach ContainerPort without looping back through the redirect. A
// zero ProxyPort skips that DNAT rule (symbolic target ports don't need it).
type Intercept struct {
	Protocol      types.Proto
	ContainerPort uint16
	AgentPort     uint16
	ProxyPort     uint16
}

// OwnerMatch identifies the traffic-agent's own sockets. It mirrors
// agentinit's trafficAgentOwner: match on the agent's primary group when
// AGENT_GID is set (UseGID true), otherwise fall back to its UID. skgid/skuid
// are matched host-endian (see matchOwner). Mark selects a different
// discriminator entirely -- see its own doc comment.
type OwnerMatch struct {
	// UseGID selects `meta skgid` (true) or `meta skuid` (false). Ignored
	// when Mark is non-zero.
	UseGID bool
	ID     uint32

	// Mark, when non-zero, identifies the agent's own packets by their
	// firewall mark (`meta mark`) instead of by socket owner. This is required
	// for targets whose network namespace is owned by a non-init user
	// namespace, where a socket-owner match cannot be installed.
	Mark uint32
}

// Config describes the ruleset to build for a single pod's network
// namespace.
type Config struct {
	// PodIP is the pod's own address. Its family selects the nftables table
	// family (ip vs ip6) for the whole ruleset, and MeshDialSubnets of the
	// other family are ignored (mirroring agentinit's familySubnets).
	PodIP netip.Addr

	// Loopback is the name of the loopback interface (typically "lo").
	Loopback string

	// Owner identifies the traffic-agent's own sockets so its egress can be
	// told apart from the application's (and a mesh proxy's).
	Owner OwnerMatch

	// Intercepts are the container-port -> agent-port redirects to install,
	// across all protocols.
	Intercepts []Intercept

	// MeshDialSubnets are destinations that must always be routed through a
	// service mesh, even for the agent's own (otherwise mesh-bypassing)
	// traffic -- typically a mesh's virtual address range for external
	// services, which only the mesh proxy knows how to route. Entries whose
	// address family differs from PodIP's are ignored.
	MeshDialSubnets []netip.Prefix
}

const (
	// TableName is the name of the dedicated nftables table this package
	// manages. Everything the traffic-agent programs lives in this one table,
	// so teardown is a single DelTable.
	TableName = "telepresence"

	chainPrerouting = "prerouting"
	chainOutput     = "output"

	// mapRedirectPorts is the name of the container-port -> agent-port map,
	// keyed by the concatenation of the transport protocol and the container
	// port so a port may be intercepted on both TCP and UDP with distinct
	// agent ports.
	mapRedirectPorts = "redirect_ports"

	setMeshDialSubnets = "mesh_dial_subnets"

	dnsPort = 53

	// ifNameSize is IFNAMSIZ: nft pads interface names to this length when
	// comparing them as raw register data.
	ifNameSize = 16

	// priorityDeltaFromMeshDstnat is the distance from
	// nftables.ChainPriorityNATDest (-100, the conventional priority service
	// meshes use for their own dstnat nat chains, e.g. Istio's) at which our
	// base chains are registered. Base chains at the same hook run in
	// ascending priority order, so the sign of this delta -- added for
	// prerouting, subtracted for output -- is what orders our chains relative
	// to the mesh's.
	priorityDeltaFromMeshDstnat = 10
)

// DefaultPreroutingPriority places the prerouting chain just after the mesh's
// dstnat (so a mesh's inbound redirect runs first, and we never bypass it).
//
//nolint:gochecknoglobals // constant
var DefaultPreroutingPriority = int32(*nftables.ChainPriorityNATDest) + priorityDeltaFromMeshDstnat

// DefaultOutputPriority places the output chain just before the mesh's dstnat
// (so the agent's own nat decisions -- app-port redirect and identity DNAT --
// set the conntrack mapping first and thereby preempt the mesh's outbound
// redirect).
//
//nolint:gochecknoglobals // constant
var DefaultOutputPriority = int32(*nftables.ChainPriorityNATDest) - priorityDeltaFromMeshDstnat
