//go:build linux

// Package agentnft programs the traffic-agent's netfilter rules natively,
// over netlink, using github.com/google/nftables. The agent init container in
// cmd/traffic/cmd/agentinit uses it to set up the pod's packet routing; see
// docs/reference/agent-packet-routing.md for the behavior it implements.
//
// The ruleset is a dedicated table per address family (ip/ip6 telepresence)
// with two nat base chains:
//
//   - prerouting, at a priority just AFTER a mesh's dstnat, redirects inbound
//     app-port traffic to the agent port via a single map keyed by the
//     concatenation of the transport protocol and the container port
//     (inet_proto . inet_service).
//   - output, at a priority just BEFORE a mesh's dstnat, redirects three
//     app-port cases through that same map, rewrites the proxy port to the
//     container port for numeric-target intercepts, and gives the agent's own
//     general egress an identity DNAT (dnat to ip daddr) so it goes direct and
//     preempts the mesh -- except DNS (port 53) and mesh_dial_subnets
//     destinations, which fall through to the mesh.
//
// Ruleset construction (Build) is a pure function of Config and is
// unit-tested without root or a network namespace; only nftutil.Apply (a
// single atomic, idempotent full-replace batch) and nftutil.Teardown touch
// netlink. The package is netns-agnostic: passing nftables.WithNetNSFd to
// nftutil.Apply targets a network namespace other than the caller's current
// one -- which is how a node-hosted agent programs a target pod's namespace
// from outside it.
//
// Telling the agent's own traffic apart from the application's (Config.Owner)
// is normally a socket-owner match (skgid/skuid). A network namespace owned
// by a non-init user namespace rejects that match, so OwnerMatch.Mark selects
// a firewall-mark match instead for such targets.
package agentnft
