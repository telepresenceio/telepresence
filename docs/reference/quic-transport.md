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
   manager without a listener (or one predating the feature) answers that no
   endpoint exists, and the session simply stays on the port-forwarded
   transport.
3. If an endpoint is advertised, the client dials it with a three-second
   budget. Success upgrades all traffic-manager-bound tunnel streams to QUIC
   for the rest of the session; failure is logged at debug level and the
   session continues on the port-forwarded transport. Session startup is
   never delayed or failed by this step.

An established QUIC connection is kept alive with pings every 15 seconds
while idle. If it is lost mid-session, the client logs one warning, moves the
affected and all subsequent streams back to the port-forwarded transport, and
does not retry QUIC until the next `telepresence connect`.

Streams to a traffic-agent that use the client's direct agent port-forward
are not affected by the upgrade; they keep their own port-forwarded
connections. When agent port-forwarding is disabled
(`cluster.agentPortForward=false`), agent-bound traffic relays through the
traffic-manager and therefore does benefit from QUIC.

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
| `quicTunnel.externalHost` | `""` | Host or IP advertised to clients. The endpoint is not advertised while this is empty |
| `quicTunnel.externalPort` | `0` | Port advertised to clients; `0` means `quicTunnel.port` |
| `quicTunnel.service.create` | `true` | Create a Service for the endpoint |
| `quicTunnel.service.type` | `LoadBalancer` | Type of that Service |
| `quicTunnel.service.nodePort` | `0` | Fixed node port when the type is `NodePort`; `0` auto-assigns |
| `quicTunnel.service.annotations` | `{}` | Annotations for the Service |

The chart passes these to the traffic-manager as the environment variables
`TUNNEL_QUIC_PORT`, `TUNNEL_QUIC_EXTERNAL_HOST`, and
`TUNNEL_QUIC_EXTERNAL_PORT`.

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

Watch for two log lines:

- the traffic-manager (from quic-go): `failed to sufficiently increase
  receive buffer size (wanted: 7168 kiB, got: ...)`
- the forwarder: `front socket buffers: rcv=... snd=... (asked for ...)`

If they report far less than what was asked for, raise the node sysctls —
for example `net.core.rmem_max=16777216` and `net.core.wmem_max=16777216`
via your node configuration mechanism (on GKE Standard,
`linuxNodeConfig.sysctls`; not configurable on GKE Autopilot). The
port-forwarded gRPC transport is unaffected: it rides kernel TCP, whose
buffers autotune independently of these caps.

## Version compatibility

The endpoint descriptor is a purely additive API. Old clients never ask for
it and behave as before against a new traffic-manager; new clients treat an
old traffic-manager as one without an endpoint. No coordinated upgrade is
required in either direction.

## Current limitations

- Tunneled UDP is carried over reliable QUIC streams, like it is over the
  port-forwarded transport; QUIC's unreliable datagrams are not used yet.
- A session that fell back to the port-forwarded transport does not re-probe
  the QUIC endpoint until the next connect.
- Direct client-to-agent port-forwards do not use QUIC.
