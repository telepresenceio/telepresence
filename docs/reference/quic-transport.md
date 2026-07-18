---
title: QUIC tunnel transport
description: How the opt-in QUIC endpoint carries tunneled traffic, how trust is bootstrapped from the Kubernetes-authenticated connection, and how clients upgrade and fall back.
---

Telepresence tunnels traffic between the workstation and the cluster as
streams of messages. There are several tunnels: one to the traffic-manager,
and one to the traffic-agent of each attached workload. By default each
tunnel is a gRPC connection port-forwarded through the Kubernetes API
server — universally reachable, but subject to head-of-line blocking (a
tunnel's streams share one TCP connection, so one lost packet stalls all of
them), to the API server's throughput limits, and to connection loss when
the workstation changes networks.

The traffic-manager can expose an alternative **QUIC endpoint** for the same
streams. QUIC gives each tunneled connection an independently retransmitted
stream, and its connections survive the client's address changing. The
port-forwarded transport always remains available; QUIC is an opportunistic
upgrade, never a requirement. Enabling it is described in the
[Enable the QUIC tunnel transport](../howtos/quic-transport.md) howto; how the
transport is built -- the packet forwarder, its routing, and the reasoning
behind the design -- in the
[architecture reference](quic-transport-architecture.md).

## When it helps (and when it doesn't)

QUIC is an opt-in optimization, not a universal win. It helps most when the
network is lossy or the round trip is long; it helps least on a quiet link or a
node whose UDP buffers are capped. Which transport comes out ahead, by situation:

| Situation | gRPC | QUIC | Why |
|---|---|---|---|
| Small requests under packet loss |  | faster (~1.8x p95) | One lost packet stalls all gRPC flows on the shared connection; QUIC recovers each on its own stream. |
| A single bulk transfer, well-buffered node |  | faster (~8x) | gRPC's one flow is window-capped by the port-forward at RTT; QUIC's per-flow streams are not. |
| Pause-heavy traffic at WAN latency |  | faster (~1 RTT/req) | Kernel TCP shrinks its window after every idle gap; QUIC keeps it. |
| Workstation changes network (roam, NAT rebind) |  | survives | The connection survives the address change instead of reconnecting. |
| Traffic-manager restart, active intercept |  | keeps flowing | Agent traffic rides the forwarder, not the manager. |
| Bulk transfer on capped-UDP-buffer nodes (GKE COS / Autopilot) | faster |  | QUIC's UDP buffers pin below the bandwidth-delay product; kernel TCP autotunes past it. Tunable on GKE Standard / on-prem. |
| Many concurrent bulk transfers | faster |  | Independent flows aggregate past QUIC's buffer-capped ceiling. |
| Quiet, unloaded network | even | even | Nothing to fix: no loss, no queueing. |
| Tunneled UDP carried as datagrams | no worse | no better | RFC 9221 datagram carriage measured no better and often worse; off by default. |

**Beyond speed: the API server stops carrying the data plane.** On the
port-forwarded transport, every tunneled byte that a traffic-agent doesn't handle
is relayed by the traffic-manager through the Kubernetes API server's
port-forward. QUIC moves that data plane onto the dedicated forwarder, so the API
server no longer carries telepresence's tunnel traffic at all. In a large cluster
with many connected developers, that removes the API server as a shared chokepoint
and rate-limited dependency for the data path -- potentially the biggest win of
all, but a cluster-scalability property rather than a per-request one, so it is
not in the table above.

These are directional results from the `perf/network` experiments (kind), not
guarantees -- they shift with RTT, loss, concurrency, and especially node UDP
buffer limits (see "Throughput and node tuning" below). How each was measured is
in `perf/README.md`.

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
while idle. If it is lost mid-session, the client logs one warning and moves
the affected and all subsequent streams back to the port-forwarded transport.
It then re-probes the QUIC endpoint in the background on an interval and
returns new streams to QUIC once a probe succeeds, without a
`telepresence connect` -- see the recovery behavior described under
"Observability" below.

Direct client-to-agent connections upgrade independently. When
`cluster.agentPortForward` is enabled (the default) the client dials each
agent that advertises a QUIC listener through the forwarder, using the
agent's own SNI, and falls back to that agent's Kubernetes port-forward if
the QUIC dial fails; `telepresence status` reports each agent's transport
separately from the traffic-manager's. When agent port-forwarding is disabled
(`cluster.agentPortForward=false`) the client makes no direct agent
connections at all: agent-bound traffic relays through the traffic-manager
and rides whatever transport the manager tunnel is using.

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
| `quic (host:port)` | The QUIC endpoint at that address carries the manager tunnel |
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

## Connection migration

A QUIC connection is not tied to the client's network 4-tuple the way the
port-forwarded TCP transport is. Once a connection is established, the
forwarder routes its packets by the connection ID the traffic-manager (or
agent) assigned during the handshake, never by the client's source address;
RFC 9221 datagrams ride the same connection-ID-routed packets as everything
else, with no special case. A change in the client's source address — a NAT
re-mapping, or roaming to a network whose local address the OS keeps usable
on the same socket — therefore does not interrupt an established connection:
the forwarder sees the next packet from an unrecognized source, decodes its
connection ID, and forwards it to the same backend exactly as it would for a
brand-new connection, while quic-go's own path validation
(PATH_CHALLENGE/PATH_RESPONSE) confirms the new path before trusting it.
This is proven end to end, without a cluster, by
`cmd/traffic/cmd/quicforwarder`'s NAT-rebind tests: an 8 MiB transfer
survives a mid-transfer rebind, a connection survives two rebinds in a row,
and a rebind followed by an idle period past the keep-alive interval still
leaves the connection alive on the new path.

Two things do not survive a rebind:

- **A source change before the handshake completes.** Until the manager has
  assigned a connection ID, the forwarder can only route by SNI, keyed on
  the client's source address. A rebind that straddles the handshake's own
  Initial-packet retry leaves two independently-negotiated connection
  attempts alive for what the client considers a single dial, which it
  cannot reconcile; the dial fails and the client is left to retry, the
  same as against any other unreachable endpoint.
- **A change to the client's own local address**, as opposed to a NAT
  re-mapping that leaves the client's local socket untouched, is not
  actively migrated today. quic-go supports adding a new local path to an
  existing connection, but only if the application detects the network
  change and requests it; the root daemon does not monitor the
  workstation's network or interface state, so nothing currently makes
  that call. A genuine interface roam degrades the same way any other QUIC
  path failure does: the connection idles out, the session falls back to
  the port-forwarded transport, and the background re-probe described
  above re-establishes a fresh QUIC connection once the new path is
  reachable — but in-flight streams on the old connection are not carried
  over to it.

The port-forwarded fallback transport has no migration property of its own:
it is plain TCP through the Kubernetes API server, and a network change
that breaks the underlying connection requires a reconnect, the same as any
other TCP-based tool.

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

- **Datagram carriage for tunneled UDP is off by default and unproven.** A UDP
  payload between the client and the traffic-manager rides an unreliable
  QUIC datagram (RFC 9221) when it fits the connection's datagram budget,
  and falls back to the flow's reliable stream per message when it does not
  (no negotiation or latching, so an occasional oversized payload never
  disables the fast path for the rest of the flow). This was intended to
  remove the head-of-line blocking that reliable-stream carriage imposes on
  tunneled UDP, but a measurement of an inner QUIC (HTTP/3) request/response
  workload found datagram carriage **no better than stream carriage and
  often worse** (see `perf/README.md`, "Datagram carriage") — it has not been
  shown to help a real workload, and there is evidence of a latency penalty.
  It is off by default; set the manager environment variable
  `TELEPRESENCE_QUIC_ENABLE_DATAGRAMS=true` to opt in. Both
  ends must have negotiated datagram support for it to happen at all;
  against an older peer, or over the gRPC fallback transport, every payload
  keeps arriving on the stream exactly as before. Direct client-to-agent UDP
  flows are unaffected: the agent's QUIC listener
  (`cmd/traffic/cmd/agent/quicserver`) serves the agent's gRPC server over
  QUIC streams, so a tunnel message there lives *inside* a gRPC frame rather
  than in pkg/tunnel's own framing, and datagram carriage for that path is
  unimplemented.

- **The QUIC endpoint assumes a single traffic-manager replica.** The
  manager's QUIC certificate authority and session state are process-local,
  and the manager's SNI is not pod-specific, so with more than one
  traffic-manager pod a client can receive a certificate minted by one manager
  while the forwarder routes the fixed manager SNI to another, breaking the
  handshake. The traffic-manager runs as a single replica; more than one is
  unsupported for the QUIC path. The port-forwarded transport is unaffected and
  works with any number of replicas.
