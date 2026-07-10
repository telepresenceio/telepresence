---
title: Routing Loop Prevention on Local Clusters
description: How the route-controller DaemonSet prevents routing loops on local clusters by blackholing the service CIDR in an nftables FORWARD chain.
---

# Routing Loop Prevention on Local Clusters

## Overview

On local Kubernetes clusters (Kind, minikube, k3d, Docker Desktop), deleted services can cause
routing loops when Telepresence is connected:

1. A workstation process reaches a stale service ClusterIP through the TUN device.
2. The TUN device forwards the connection to the traffic-agent.
3. The traffic-agent dials the same stale ClusterIP.
4. No kube-proxy rule exists for it, so the packet follows the node's default route and leaves the cluster.
5. The packet returns to the workstation's TUN device and the loop repeats.

The root cause is that local-cluster nodes have no blackhole or reject route for the service CIDR.
Deleted (or never-assigned) service IPs fall through to the node's default route and escape to the
external network.

The **route-controller** is a DaemonSet that prevents these loops. It runs on every node
with host networking and `NET_ADMIN` privileges. At startup it discovers the cluster's service
CIDRs and programs a dedicated `telepresence` nftables table (one per address family in use) with
a `forward`-hook chain whose single rule drops any forwarded packet destined for an address in a
named set of the service CIDRs. The rule is applied natively over netlink
(`github.com/google/nftables`); no `iptables` or `nft` binary is involved. Any forwarded packet
(i.e. pod traffic flowing through the host's network stack) destined for an IP that has no active
kube-proxy DNAT rule is dropped rather than escaping via the default route.

### Why an nftables FORWARD drop and not a kernel blackhole route?

A kernel `RTN_BLACKHOLE` route for the service subnet would cause `connect()` and `sendmsg()` to
fail at the socket level before any netfilter hook can fire. This would break locally-generated
traffic such as the kube-apiserver calling the mutating webhook, or the route-controller itself
reaching the Kubernetes API server.

The `forward` hook is only evaluated for traffic that has already been accepted into the host's
forwarding path. kube-proxy's `prerouting` DNAT rewrites the destination to a pod IP *before* the
`forward` hook is reached, so rules for active services are completely unaffected.

### Ruleset

For each address family with at least one discovered service CIDR, the route-controller programs:

- A table named `telepresence` (`ip telepresence` for IPv4, `ip6 telepresence` for IPv6).
- A `forward` chain in that table: type `filter`, hook `forward`, at the standard filter priority.
- A named interval set, `service_cidrs`, holding the discovered CIDRs for that family.
- A single rule in the `forward` chain: `ip daddr @service_cidrs drop` (or the `ip6` equivalent).

Because the CIDRs live in a set rather than as one rule per CIDR, adding or removing a service CIDR
is a set-element change, not a rule change. On each start (or restart) the route-controller applies
the whole table as a single atomic, idempotent replacement, so it always converges to exactly the
currently discovered CIDRs regardless of what a previous run programmed.

## Enabling the route controller

The route-controller is automatically enabled when the traffic-manager Helm chart is installed with
`image.registry` set to `"local"` or a `"localhost:*"` address (the convention for local clusters).
It can also be force-enabled or force-disabled explicitly:

```bash
# Force-enable (e.g. on a remote cluster that exhibits the same problem)
helm upgrade --install traffic-manager charts/telepresence-oss \
  --set routeController.enabled=true

# Force-disable (e.g. to opt out on a local cluster)
helm upgrade --install traffic-manager charts/telepresence-oss \
  --set routeController.enabled=false
```

### Helm values

| Value | Default | Description |
|---|---|---|
| `routeController.enabled` | `null` | `null` = auto-detect, `true` = always enable, `false` = always disable |
| `routeController.image.registry` | `""` | Image registry (inherits `image.registry` when empty) |
| `routeController.image.name` | `route-controller` | Image name |
| `routeController.image.pullPolicy` | `""` | Image pull policy (inherits `image.pullPolicy` when empty) |
| `routeController.logLevel` | `""` | Log level (inherits `logLevel` when empty) |
| `routeController.serviceCIDRs` | `[]` | Explicit service CIDRs (see [Service CIDR discovery](#service-cidr-discovery)) |

## Service CIDR discovery

The route-controller needs to know the cluster's service CIDRs to populate the `service_cidrs` set.
It discovers them in priority order:

1. **`SERVICE_CIDRS` environment variable** — set via `routeController.serviceCIDRs` in Helm values
   (comma-separated list, e.g. `10.96.0.0/12`). This takes priority over all other methods.
2. **Kubernetes ServiceCIDR API** (`networking.k8s.io/v1`, available in Kubernetes 1.33+) — the
   controller lists `ServiceCIDR` objects from the cluster automatically.
3. **Fallback** — if neither source is available the controller logs a warning and no nftables
   rules are installed. Set `routeController.serviceCIDRs` explicitly in that case.

For clusters older than Kubernetes 1.33, set the service CIDR explicitly:

```bash
helm upgrade --install traffic-manager charts/telepresence-oss \
  --set routeController.enabled=true \
  --set 'routeController.serviceCIDRs={10.96.0.0/12}'
```

To find the service CIDR of an existing cluster you can inspect the kube-apiserver flags:

```bash
kubectl -n kube-system get pod kube-apiserver-$(kubectl get node -o jsonpath='{.items[0].metadata.name}') \
  -o jsonpath='{.spec.containers[0].command}' | tr ' ' '\n' | grep service-cluster-ip-range
```

## Verifying the route controller

After enabling, confirm the DaemonSet is running and the rules are in place:

```bash
# Check DaemonSet status
kubectl -n ambassador get daemonset route-controller

# View logs from one node
kubectl -n ambassador logs -l app=route-controller --tail=50

# On a Kind node: inspect the telepresence table's forward chain and service_cidrs set
docker exec kind-control-plane nft list table ip telepresence
```

## RBAC

The route-controller uses a dedicated `ServiceAccount` with a `ClusterRole` that grants:

- `get`, `list` on `networking.k8s.io/servicecidrs` (to auto-discover CIDRs on Kubernetes 1.33+)

Both resources are created automatically when the route-controller is enabled.
