---
title: External control endpoint
description: The traffic-manager's optional TLS gRPC listener for clients that must not contact the Kubernetes API server.
---

The traffic-manager can publish an optional TLS gRPC listener for clients,
configured with the Helm value `externalEndpoint`. A client configured with
`cluster.managerAddress` dials it directly instead of establishing a
port-forward through the Kubernetes API server, which removes the last
mechanical Kubernetes permission a connection needs: such a client makes no
Kubernetes API requests at all — from either of its daemons.

```yaml
# client config (config.yml or the kubeconfig's telepresence.io extension)
cluster:
  managerAddress: tls://tm.example.com:443
  managerServerCA: /path/to/ca.pem   # PEM, base64 PEM, or file path; omit for public trust
```

## Requirements

- **`security.authentication.mode: enforcing` is mandatory.** The chart
  refuses to render, and the manager refuses to start, with an external port
  and any other mode. In permissive mode an unauthenticated caller would be
  admitted; that is tolerable over a port-forward only because reaching the
  manager then requires `pods/portforward` and the API server has vouched for
  the caller. An external endpoint has no such precondition.
- **Server trust must survive manager restarts.** The listener terminates TLS
  with a persisted certificate: either an existing `kubernetes.io/tls` Secret
  (`externalEndpoint.tls.secretName`) or a cert-manager Certificate
  (`externalEndpoint.tls.certManager`). The in-memory QUIC CA is ephemeral by
  design and is not used here.
- The client authenticates with its kubeconfig bearer token, exactly as over
  a port-forward. Reading the kubeconfig and running its exec plugin are
  local operations, not API-server requests.

## The data plane rides QUIC

The external listener carries the control plane. Outbound cluster traffic
can fall back to tunnel streams on the TLS gRPC connection itself, but
agent-bound streams — the delivery path for intercepted traffic and volume
mounts — need a client-to-agent channel, which in external mode is the QUIC
tunnel (`quicTunnel.enabled`): there is no Kubernetes port-forward to fall
back to. Publish the QUIC endpoint alongside the external endpoint.
Without it, an external-only client can connect, browse, and gather logs,
but creating an intercept or ingest fails early with an error naming the
missing channel — the client never creates an attachment whose traffic
has no way to reach the local workstation.

## What the client loses without cluster access

In external-only mode, features that inherently require client-side
Kubernetes access are disabled with explicit errors rather than degraded
silently: the ConfigMap-backed admin commands for revoking intercepts, and
the legacy direct log-gathering path (the manager serves the logs instead).
Symbolic service ports are resolved by the manager on the client's behalf,
so they work the same as over a port-forward; only against a
traffic-manager too old to serve that resolution does the client report an
error asking for a numeric port. Namespace discovery always comes from the
manager.

## A restricted surface

The external listener does not serve the manager's internal gRPC surface.
It exposes only the client-facing functionality, restricted to the request
forms an external caller is permitted to use. Everything that only
in-cluster peers need — the traffic-agent's calls and the quic-forwarder
feed — is absent and remains reachable only on the in-cluster listener.

Pre-session, the deliberately public surface is the version handshake and
health checking — nothing else. Every other call requires an authenticated
principal, and every call that names a session verifies that the session
belongs to the caller's identity; possession of a session ID is never
sufficient. Request forms that only in-cluster peers use — an intercept
watch naming an agent session, for example — are rejected.

## Admission controls

The external listener is a `TokenReview` amplification surface: each
previously unseen invalid token can cost the manager up to two reviews. The
listener therefore bounds the path before any review runs: a cap on
concurrent authentication attempts, an authentication rate limiter, a
maximum credential length, per-connection stream and message-size limits,
TLS-handshake and unauthenticated-idle deadlines. The
`telepresence_external_auth_*` counters make this path observable; see
[External Endpoint Authentication Metrics](../howtos/monitoring.md#external-endpoint-authentication-metrics).
Network-level restrictions (LoadBalancer source ranges, NetworkPolicy)
remain recommended defense in depth.

## Disabling the port-forward path entirely

Publishing an endpoint stops the chart from granting the port-forward
bootstrap — except when `security.authorization.requiredGrant: portforward`,
where possession of `pods/portforward` is itself the connect policy, so the
named grant, and with it the path, remains. With any other required grant,
what remains is RBAC granted elsewhere: set
`security.authorization.requiredGrant: telepresence` so an identity holding
`pods/portforward` for unrelated reasons still cannot turn the tunnel into
a session. Wildcard identities such as `cluster-admin` pass every review
and cannot be constrained this way.
