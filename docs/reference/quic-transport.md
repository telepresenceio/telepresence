---
title: QUIC tunnel transport
description: How the opt-in QUIC endpoint carries tunneled traffic, how trust is bootstrapped from the Kubernetes-authenticated connection, and how clients upgrade and fall back.
---

Telepresence tunnels traffic between the workstation and the cluster as
streams of messages. By default those streams are gRPC streams, multiplexed
onto a single connection that is port-forwarded through the Kubernetes API
server — universally reachable, but subject to head-of-line blocking (all
streams share one TCP connection, so one lost packet stalls them all), to the
API server's throughput limits, and to connection loss when the workstation
changes networks.

The traffic-manager can expose an alternative **QUIC endpoint** for the same
streams. QUIC gives each tunneled connection an independently retransmitted
stream, and its connections survive the client's address changing. The
port-forwarded transport always remains available; QUIC is an opportunistic
upgrade, never a requirement. Enabling it is described in the
[Enable the QUIC tunnel transport](../howtos/quic-transport.md) howto.

## How the upgrade happens

1. The client connects exactly as it always has, over the port-forwarded
   connection, and establishes its session.
2. It then asks the traffic-manager for the QUIC endpoint descriptor. A
   manager without a listener (or one predating the feature), or one with a
   listener but no candidate address to advertise (see "Endpoint discovery"
   below), answers that no endpoint exists, and the session simply stays on
   the port-forwarded transport.
3. If an endpoint is advertised, it carries an ordered list of candidate
   addresses. The client dials every candidate concurrently, staggered by
   250ms in the manager's preferred order so the preferred candidate
   normally wins outright, within an overall three-second budget. The first
   candidate whose handshake completes upgrades all traffic-manager-bound
   tunnel streams to QUIC for the rest of the session; every other candidate
   is closed. If every candidate fails, that is logged at debug level and
   the session continues on the port-forwarded transport. Session startup is
   never delayed or failed by this step. Direct client-to-agent connections
   that also use QUIC (see below) dial the same address this step found
   reachable, rather than re-probing the candidate list themselves.

An established QUIC connection is kept alive with pings every 15 seconds
while idle. If it is lost mid-session, the client logs one warning, moves the
affected and all subsequent streams back to the port-forwarded transport, and
does not retry QUIC until the next `telepresence connect`.

Streams to a traffic-agent that use the client's direct agent port-forward
are not affected by the upgrade; they keep their own port-forwarded
connections. When agent port-forwarding is disabled
(`cluster.agentPortForward=false`), agent-bound traffic relays through the
traffic-manager and therefore does benefit from QUIC.

## Endpoint discovery

`quicTunnel.enabled=true` alone is enough to get the QUIC transport working
on any cluster whose `quicTunnel.service.type` the traffic-manager can
observe: it watches the Service in front of the QUIC forwarder and derives
the candidate address list from it, rather than requiring an admin to look
up and configure an address by hand.

| Service type | Candidates | Requires |
|---|---|---|
| `LoadBalancer` (default) | One per `status.loadBalancer.ingress[]` entry (IP or hostname), at the Service's port | Nothing beyond the default `services` RBAC every install has |
| `NodePort` | One per cluster Node, at the Service's assigned `nodePort`; a Node's external IP is preferred, its internal IP is the fallback | Read access to Nodes (`list`, `watch`) |
| `ClusterIP`, or `quicTunnel.service.create=false` | None | — |

A `LoadBalancer` Service with no ingress assigned yet (the cloud provider
hasn't provisioned one), or a Node without any usable address, simply
contributes no candidate; clients pick up a candidate on a later connect
once one exists. The candidate list is capped at 8 entries and ordered
deterministically (ingress order for `LoadBalancer`, Nodes sorted by name
for `NodePort`), so which addresses are advertised is stable across
reconnects.

**Namespace-scoped installs and NodePort.** A traffic-manager restricted to
a namespace-scoped Role (`traffic-manager.namespaced`) has no RBAC to list
or watch Nodes — Node objects are cluster-scoped, and granting a
namespace-scoped install access to them would widen its privileges beyond
its own namespace. Discovery detects this once at startup, logs it at info,
and simply advertises no NodePort candidates rather than erroring. Reaching
the endpoint at all in this shape requires the explicit override described
in the [howto](../howtos/quic-transport.md#when-discovery-cannot-see-your-topology).

`quicTunnel.externalHost` bypasses all of the above: when it's set,
discovery isn't even started, and the descriptor always advertises exactly
that one address. This is the escape hatch for topologies the
traffic-manager cannot observe by watching its own Service and Nodes — a NAT
or proxy in front of the load balancer, port remapping, or a DNS name that
only resolves on the developer's VPN.

## Trust model

The QUIC endpoint uses mutual TLS, bootstrapped entirely from the
Kubernetes-authenticated connection so that a kubeconfig remains the only
client credential:

- At startup, a traffic-manager with the listener enabled generates an
  **ephemeral certificate authority**, held only in memory. A manager restart
  generates a new one, implicitly revoking everything the old one signed.
- The endpoint descriptor handed to a client over the port-forwarded, RBAC-
  authenticated connection contains the CA bundle and a short-lived client
  certificate whose CommonName is the client's session ID.
- The client verifies the server against exactly that CA — never the system
  trust store — and presents the session certificate. The listener rejects
  connections without a valid certificate, and rejects any stream whose
  declared session doesn't match the certificate it arrived on.

A peer that can reach the UDP port but has no Kubernetes access is left with
a TLS handshake it cannot complete.

## Helm values

| Value | Default | Description |
|-------|---------|-------------|
| `quicTunnel.enabled` | `false` | Run the QUIC listener and create its Service |
| `quicTunnel.port` | `7778` | UDP port the listener binds to |
| `quicTunnel.externalHost` | `""` | Host or IP advertised to clients, overriding discovery entirely. Empty means discover the address instead (see "Endpoint discovery" above) |
| `quicTunnel.externalPort` | `0` | Port advertised to clients when `externalHost` is set; `0` means `quicTunnel.port`. Not used by discovery, which always uses the Service's own port |
| `quicTunnel.service.create` | `true` | Create a Service for the endpoint. Discovery has nothing to watch when this is `false` |
| `quicTunnel.service.type` | `LoadBalancer` | Type of that Service; determines which discovery rule applies |
| `quicTunnel.service.nodePort` | `0` | Fixed node port when the type is `NodePort`; `0` auto-assigns |
| `quicTunnel.service.annotations` | `{}` | Annotations for the Service |

The chart passes these to the traffic-manager as the environment variables
`TUNNEL_QUIC_PORT`, `TUNNEL_QUIC_EXTERNAL_HOST`, `TUNNEL_QUIC_EXTERNAL_PORT`,
and — whenever there is discovery for the traffic-manager to do, i.e.
`externalHost` is unset and the Service is created —
`TUNNEL_QUIC_SERVICE_NAME`, naming the Service discovery watches.

## Observability

`telepresence status` reports the transport that currently serves
traffic-manager-bound tunnel streams:

| Reported value | Meaning |
|----------------|---------|
| `grpc` | The port-forwarded transport; no QUIC upgrade happened |
| `quic (host:port)` | The QUIC endpoint at that address carries the tunnel |
| `grpc (fallback)` | QUIC was active but was lost; the session downgraded |

The root daemon logs `QUIC tunnel transport active (host:port)` on a
successful upgrade, and the traffic-manager logs
`QUIC tunnel listener started` when the listener is enabled.

A session that falls back to `grpc (fallback)` is not stuck there: the root
daemon retries the QUIC dial in the background, re-fetching the endpoint
descriptor (a fresh CA and client certificate) on every attempt, so it
recovers on its own once the endpoint is reachable again — for example after
a traffic-manager restart, without a `telepresence quit`/`connect` cycle.
`telepresence status` returns to `quic (host:port)` once a retry succeeds.

## Throughput and node tuning

QUIC runs in userspace over UDP, so its bulk throughput depends on the UDP
socket buffers the cluster nodes allow. Both the traffic-manager and the
forwarder ask the kernel for large buffers at startup, but the grant is
silently capped by the node's `net.core.rmem_max` / `net.core.wmem_max`
sysctls. On nodes with small caps (for example Container-Optimized OS
defaults to 208 KiB), a paced burst from the sender can overflow a receive
buffer; every such overflow is silent packet loss that shrinks the sender's
congestion window, which caps sustained throughput well below what the link
supports.

Watch for these log lines:

- the traffic-manager (from quic-go): `failed to sufficiently increase
  receive buffer size (wanted: 7168 kiB, got: ...)`
- the forwarder, once at startup: `front socket buffers: rcv=... snd=...
  (asked for ...), gro=..., gso=...` — the granted socket buffers plus
  whether the kernel accepted UDP_GRO/UDP_SEGMENT (generic receive/segmentation
  offload), which roughly halve the forwarder's per-datagram relay cost at
  high throughput when the kernel supports them
- the forwarder, periodically at debug level: `quic-forwarder counters:
  forwarded=... rcvbuf=... sndbuf=... gro=... gso=...` — the same buffer and
  offload state carried alongside the running forwarded/dropped counters, so
  a long-lived log answers "why is throughput capped" without having to
  scroll back to the startup line

If they report far less than what was asked for, raise the node sysctls —
for example `net.core.rmem_max=16777216` and `net.core.wmem_max=16777216`
via your node configuration mechanism (on GKE Standard,
`linuxNodeConfig.sysctls`; not configurable on GKE Autopilot). The
port-forwarded gRPC transport is unaffected: it rides kernel TCP, whose
buffers autotune independently of these caps.

## Version compatibility

The endpoint descriptor is a purely additive API. Old clients never ask for
it and behave as before against a new traffic-manager; new clients treat an
old traffic-manager as one without an endpoint. The candidate list is
likewise additive: the descriptor's `host`/`port` fields always duplicate
the first candidate, so a client built before candidate discovery existed
still gets exactly one address to dial from a traffic-manager that now has
several. No coordinated upgrade is required in either direction.

## Current limitations

- Tunneled UDP is carried over reliable QUIC streams, like it is over the
  port-forwarded transport; QUIC's unreliable datagrams are not used yet.
- Direct client-to-agent port-forwards do not use QUIC.
