# Make pods at a routed subnet's network address reachable (#4161)

## Problem

When a subnet is routed through the TUN device on Linux, its network address is
assigned as the device's interface address (`AddrAdd 10.244.3.0/24`). The kernel
installs `local 10.244.3.0 dev tel0`, so unicast traffic to that address is
delivered to the local host instead of entering the TUN device.

The subnets are derived by the traffic-manager from observed pod IPs, so their
boundary addresses are ordinary, allocatable pod IPs. A pod that receives the
network address is unreachable from the workstation. When that pod is the
traffic-manager, the failure cascades: the client uses the manager's pod IP as
the VIF DNS address, the connect-time DNS sanity check black-holes, and DNS
falls back to a legacy mode in which cluster names don't resolve.

This is the sibling of the broadcast-address bug fixed in #4160. That fix
removed a derived route entry; the network-address case is structural — the
VIF must own *some* local address, and it currently owns the subnet's network
address.

## Approach: peer-scoped address assignment

Assign cluster subnets as **peer addresses**: a single anchor address as the
local side, with the subnet as the peer prefix
(`ip addr add 246.246.0.0 peer 10.244.3.0/24 dev tel0`, i.e.
`netlink.Addr{IPNet: anchor/32, Peer: subnet}`).

Verified on a dummy interface, this:

- claims only the anchor in the local routing table — the subnet's network
  address routes like any other address,
- still creates the kernel-derived (`proto kernel`) connected route for the
  subnet, preserving the insert-before-conflicting-routes property that makes
  `allowConflictingSubnets` win (the reason a plain-route approach was
  rejected in #4160),
- still derives a broadcast entry for the peer prefix, which the existing
  #4160 removal deletes.

### The anchor address

The network address of the virtual subnet that is allocated for VIPs — always
taken from the runtime configuration (`client.GetConfig(ctx).Routing().
VirtualSubnet`; the config is already available in the device's context, since
`openTun` reads it today). The value must never be assumed constant: the
default is platform-specific (`246.246.0.0/16` on Linux/macOS, but
`211.55.48.0/20` on Windows where that range is not usable), and the user can
configure any subnet.

The VIP generator pre-increments before allocating, so the subnet's network
address is never handed out as a virtual IP, and the subnet is fully owned by
Telepresence — it can never be a pod IP. The address becomes the local side of
every routed cluster subnet of the same family, and the source for
host-originated traffic into them.

### When classic assignment is kept

Peer-scoping applies only when the prefix's address *is* its network address
(`pfx.Addr() == pfx.Masked().Addr()`) — the hazardous case. Classic
`AddrAdd` remains for:

- prefixes carrying a deliberate host address, such as the virtual DNS subnet
  (`192.168.0.1/30`, where `.1` is the device's side and `.2` the resolver),
- the virtual subnet itself (it contains the anchor; claiming its network
  address is the point),
- prefixes of a family with no family-matched virtual subnet (e.g. IPv6
  cluster subnets with the default IPv4 virtual subnet) — today's behavior is
  kept for those, no worse than before.

`removeSubnet` mirrors the same logic so `AddrDel` matches what was added.

### Out of scope

- macOS/Windows use different plumbing (darwin already assigns a
  point-to-point pair). Whether they have an analogous problem is a separate
  investigation.
- Tightening the recursion-check exemption in `stream_creator.go`
  (`ip != sn.Masked().Addr()`), which existed because the VIF's own address
  was the network address. It becomes obsolete for peer-scoped subnets but is
  harmless; removing it can follow once all platforms stop claiming network
  addresses.

## Affected files (anticipated)

- `pkg/vif/device_linux.go` — peer-scoped `addSubnet`/`removeSubnet`.
- `CHANGELOG.yml`, docs regen.

## Testing

- **Kernel-level**: dummy-interface verification of the address/route shape
  (done during design; both `.0` and `.255` route, conflict-win route remains
  `proto kernel`).
- **Integration (kind-dev, minikube untouched)**: full suite must stay green —
  notably `CIDRConflict/Test_AllowConflictResolution` (conflict-win), the DNS
  suites, and `ProxyVia` (virtual subnet interplay).
- **Live shape check on kind-dev**: after connect, `ip route show table local`
  must contain no `local <subnet network address>` for cluster subnets, and a
  dial to a pod subnet's network address must route into the TUN rather than
  fail with `network is unreachable`.

## Rollout

Phase 1: implementation, dummy-level verification.
Phase 2: kind-dev integration runs + live shape check.
Phase 3: changelog, drop this plan.
