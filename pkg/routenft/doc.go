//go:build linux

// Package routenft programs the route-controller's blackholes natively, over
// netlink, using github.com/google/nftables: a dedicated `telepresence`
// filter table per address family (ip/ip6), each with a single forward-hook
// chain. One rule drops traffic to a named interval set of service CIDRs
// (`ip daddr @service_cidrs drop`, and the ip6 equivalent). Another drops
// traffic to the node's own pod CIDRs when it leaves through the interface
// of the node's default route (`oifname "eth0" ip daddr @pod_cidrs drop`):
// a pod that exists is reached through its own interface route, so such a
// packet is addressed to a pod that is gone.
//
// Unlike pkg/agentnft (which programs a pod's own network namespace),
// routenft programs the route-controller's own namespace directly -- the
// node's host network namespace, since the DaemonSet runs with
// hostNetwork: true -- so its rules affect the node's FORWARD hook, not any
// per-pod chain. See docs/reference/route-controller.md for why a FORWARD
// drop is used instead of a kernel blackhole route.
//
// Because the ruleset is just named CIDR sets, reconciling which CIDRs are
// blackholed is a set-element add/remove rather than a per-rule add/remove:
// Build constructs the desired sets from the currently discovered CIDRs,
// and nftutil.Apply's idempotent full-replace (the same pattern
// pkg/agentnft uses) makes each (re)start converge to exactly those sets,
// regardless of what a previous run left behind.
//
// As with pkg/agentnft, ruleset construction (Build) is a pure function of
// Config and is unit-tested without root; only nftutil.Apply and
// nftutil.Teardown touch netlink.
package routenft
