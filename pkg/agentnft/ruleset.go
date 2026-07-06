//go:build linux

package agentnft

import (
	"errors"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"

	"github.com/telepresenceio/telepresence/v2/pkg/nftutil"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// Ruleset is the fully constructed, netlink-agnostic representation of the
// agent's nftables ruleset. Build produces one from a Config;
// nftutil.Apply programs it into the kernel over netlink. Keeping
// construction separate from application is what makes the ruleset
// unit-testable without root or a network namespace: every field here is a
// plain Go value from the google/nftables package, never a live netlink
// handle.
type Ruleset struct {
	nftutil.Ruleset

	Prerouting *nftables.Chain
	Output     *nftables.Chain

	// RedirectMap is the container-port -> agent-port map keyed by
	// (inet_proto . inet_service); nil when there are no intercepts.
	RedirectMap *nftutil.SetData

	// MeshSet is the interval set of mesh-dial subnets, or nil when none of
	// the pod's address family are configured.
	MeshSet *nftutil.SetData
}

// Build constructs the ruleset described by cfg. It does not touch netlink.
func Build(cfg Config) (*Ruleset, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}

	family := nftables.TableFamilyIPv4
	localhost := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if cfg.PodIP.Is6() {
		family = nftables.TableFamilyIPv6
		localhost = netip.IPv6Loopback()
	}
	table := &nftables.Table{Name: TableName, Family: family}

	preroutingPrio := nftables.ChainPriority(DefaultPreroutingPriority)
	outputPrio := nftables.ChainPriority(DefaultOutputPriority)

	rs := &Ruleset{
		Ruleset: nftutil.Ruleset{Table: table},
		Prerouting: &nftables.Chain{
			Name:     chainPrerouting,
			Table:    table,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookPrerouting,
			Priority: &preroutingPrio,
		},
		Output: &nftables.Chain{
			Name:     chainOutput,
			Table:    table,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookOutput,
			Priority: &outputPrio,
		},
	}
	rs.Chains = append(rs.Chains, rs.Prerouting, rs.Output)

	// Redirect map, covering every intercepted protocol via a concatenated
	// (inet_proto . inet_service) key.
	if elems := redirectMapElements(cfg.Intercepts); len(elems) > 0 {
		rs.RedirectMap = &nftutil.SetData{
			Set: &nftables.Set{
				Table:         table,
				Name:          mapRedirectPorts,
				KeyType:       nftables.MustConcatSetType(nftables.TypeInetProto, nftables.TypeInetService),
				DataType:      nftables.TypeInetService,
				IsMap:         true,
				Concatenation: true,
			},
			Elements: elems,
		}
		rs.Sets = append(rs.Sets, rs.RedirectMap)
	}

	// Mesh-dial-subnet set (of the pod's address family only).
	if meshSubnets := familySubnets(cfg.MeshDialSubnets, cfg.PodIP); len(meshSubnets) > 0 {
		var err error
		rs.MeshSet, err = nftutil.IntervalSet(table, setMeshDialSubnets, meshSubnets)
		if err != nil {
			return nil, err
		}
		rs.Sets = append(rs.Sets, rs.MeshSet)
	}

	addrOff, addrLen := nftutil.AddrLayout(family)

	// --- prerouting chain ---------------------------------------------------
	// Inbound app-port traffic is redirected to the agent port, with no owner
	// or destination gating. The chain runs AFTER a mesh's dstnat (via
	// base-chain priority), so a mesh's inbound redirect wins and we never
	// bypass it.
	if rs.RedirectMap != nil {
		rs.AddRule(rs.Prerouting, redirectViaProtoPortMap(rs.RedirectMap.Set))
	}

	// --- output chain -------------------------------------------------------
	// The output chain runs BEFORE a mesh's dstnat, so our nat decisions set
	// the conntrack mapping first and preempt the mesh's outbound redirect.
	//
	// Precedence (top to bottom):
	//   1. the three app-port redirect gates,
	//   2. the proxy-port DNAT (per numeric intercept),
	//   3. the identity-DNAT mesh bypass.
	// The redirect gates and proxy DNAT must precede the identity DNAT so that
	// the agent's traffic to its own app/proxy ports is redirected/rewritten
	// rather than pinned direct.

	if rs.RedirectMap != nil {
		m := rs.RedirectMap.Set

		// Gate 1: non-agent traffic leaving via loopback (a mesh proxy dialing
		// the app on loopback).
		gate := matchOif(cfg.Loopback)
		gate = append(gate, matchOwner(cfg.Owner, true)...)
		gate = append(gate, redirectViaProtoPortMap(m)...)
		rs.AddRule(rs.Output, gate)

		// Gate 2: any traffic addressed to the pod IP -- a mesh proxy dialing
		// the app on the pod IP, or the agent addressing its own pod IP (the
		// pod-IP proxy path, and reaching an intercepted pod by IP). No owner
		// match: application, mesh, and agent traffic to the pod IP's app
		// ports must all reach the agent listener. Socket-less
		// kernel-generated packets to the pod IP also match, which is
		// harmless: the redirect target is the agent's own listener, and nat
		// output only sees packets creating a new conntrack entry.
		gate = matchDaddr(addrOff, addrLen, cfg.PodIP, false)
		gate = append(gate, redirectViaProtoPortMap(m)...)
		rs.AddRule(rs.Output, gate)

		// Gate 3: the agent dialing its own app port over loopback, but not
		// localhost (so intercept-by-IP and headless-service interception work).
		gate = matchOif(cfg.Loopback)
		gate = append(gate, matchDaddr(addrOff, addrLen, localhost, true)...)
		gate = append(gate, matchOwner(cfg.Owner, false)...)
		gate = append(gate, redirectViaProtoPortMap(m)...)
		rs.AddRule(rs.Output, gate)
	}

	// 2. Proxy-port DNAT for numeric-target intercepts: the forwarder writes to
	// pod IP : proxy port, which this rewrites to the real container port
	// (breaking the loop the redirect would otherwise create).
	for _, ic := range cfg.Intercepts {
		if ic.ProxyPort == 0 {
			continue
		}
		rule := matchL4Proto(ic.Protocol)
		rule = append(rule, matchDaddr(addrOff, addrLen, cfg.PodIP, false)...)
		rule = append(rule, matchDport(ic.ProxyPort, false)...)
		rule = append(rule, dnatTo(family, cfg.PodIP, ic.ContainerPort)...)
		rs.AddRule(rs.Output, rule)
	}

	// 3. Identity-DNAT mesh bypass for the agent's own general egress. DNS
	// (port 53) and mesh_dial_subnets destinations are excluded, so they
	// carry no rule and fall through to the mesh; everything else the agent
	// sends gets an identity DNAT and goes direct. There is no l4proto match:
	// the transport-header dport load only matches protocols that carry a
	// port-like field at transport offset 2 (tcp/udp/sctp...), and an
	// identity DNAT rewrites the destination to itself -- a no-op for any
	// protocol a mesh does not redirect -- so restricting by protocol adds
	// nothing.
	rule := matchOwner(cfg.Owner, false)
	rule = append(rule, matchDport(dnsPort, true)...)
	if rs.MeshSet != nil {
		rule = append(rule, matchDaddrNotInSet(addrOff, addrLen, rs.MeshSet.Set)...)
	}
	rule = append(rule, identityDNAT(family, addrOff, addrLen)...)
	rs.AddRule(rs.Output, rule)

	return rs, nil
}

// protoPortKey returns the 8-byte concatenation key for the redirect_ports
// map: the protocol number, then the port big-endian, each padded to 4 bytes
// -- matching how the kernel lays out a (inet_proto . inet_service)
// concatenated set key.
func protoPortKey(p types.Proto, port uint16) []byte {
	key := make([]byte, 8)
	key[0] = byte(p)
	copy(key[4:6], binaryutil.BigEndian.PutUint16(port))
	return key
}

// protoPort is a (protocol, port) pair, used to dedupe redirect-map elements
// by their key.
type protoPort struct {
	proto types.Proto
	port  uint16
}

// redirectMapElements returns the deduplicated (protocol, container port) ->
// agent port map elements for every intercept. A duplicate (protocol,
// container port) pair (e.g. the same port intercepted in two containers)
// keeps the first agent port: a set can hold each key only once, so later
// duplicates are simply dropped before the elements reach the kernel.
func redirectMapElements(intercepts []Intercept) []nftables.SetElement {
	seen := map[protoPort]struct{}{}
	var elems []nftables.SetElement
	for _, ic := range intercepts {
		k := protoPort{ic.Protocol, ic.ContainerPort}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		elems = append(elems, nftables.SetElement{
			Key: protoPortKey(ic.Protocol, ic.ContainerPort),
			Val: binaryutil.BigEndian.PutUint16(ic.AgentPort),
		})
	}
	return elems
}

// familySubnets returns the subnets whose address family matches podIP's,
// mirroring agentinit's familySubnets.
func familySubnets(subnets []netip.Prefix, podIP netip.Addr) []netip.Prefix {
	var out []netip.Prefix
	for _, sn := range subnets {
		if sn.Addr().Is4() == podIP.Is4() {
			out = append(out, sn)
		}
	}
	return out
}

func validate(cfg Config) error {
	if !cfg.PodIP.IsValid() {
		return errors.New("agentnft: PodIP is required")
	}
	if cfg.Loopback == "" {
		return errors.New("agentnft: Loopback is required")
	}
	return nil
}
