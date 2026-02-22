---
title: Route Controller
description: Prevent routing loops on local clusters by installing iptables FORWARD DROP rules for the service CIDR.
---

# Route Controller

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
CIDRs and inserts a `DROP` rule into the iptables `FORWARD` chain for each CIDR. Any forwarded
packet (i.e. pod traffic flowing through the host's network stack) destined for an IP that has no
active kube-proxy DNAT rule is dropped rather than escaping via the default route.

### Why iptables FORWARD and not a kernel blackhole route?

A kernel `RTN_BLACKHOLE` route for the service subnet would cause `connect()` and `sendmsg()` to
fail at the socket level before any iptables hook can fire. This would break locally-generated
traffic such as the kube-apiserver calling the mutating webhook, or the route-controller itself
reaching the Kubernetes API server.

An iptables `FORWARD` rule is only evaluated for traffic that has already been accepted into the
host's forwarding path. kube-proxy's PREROUTING DNAT rewrites the destination to a pod IP *before*
the FORWARD chain is reached, so rules for active services are completely unaffected.

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

The route-controller needs to know the cluster's service CIDRs to install the iptables rules.
It discovers them in priority order:

1. **`SERVICE_CIDRS` environment variable** — set via `routeController.serviceCIDRs` in Helm values
   (comma-separated list, e.g. `10.96.0.0/12`). This takes priority over all other methods.
2. **Kubernetes ServiceCIDR API** (`networking.k8s.io/v1`, available in Kubernetes 1.33+) — the
   controller lists `ServiceCIDR` objects from the cluster automatically.
3. **Fallback** — if neither source is available the controller logs a warning and no iptables
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

# On a Kind node: inspect iptables FORWARD rules
docker exec kind-control-plane iptables -L FORWARD -n | grep DROP
```

## RBAC

The route-controller uses a dedicated `ServiceAccount` with a `ClusterRole` that grants:

- `get`, `list` on `networking.k8s.io/servicecidrs` (to auto-discover CIDRs on Kubernetes 1.33+)

Both resources are created automatically when the route-controller is enabled.
