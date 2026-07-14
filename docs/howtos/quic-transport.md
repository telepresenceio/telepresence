---
title: Enable the QUIC tunnel transport
description: Expose the traffic-manager's opt-in QUIC endpoint and let clients upgrade the tunnel from the port-forwarded connection, removing head-of-line blocking and surviving network changes.
hide_table_of_contents: true
---

# Enable the QUIC tunnel transport

By default, everything Telepresence tunnels between your workstation and the
cluster shares a single port-forwarded connection through the Kubernetes API
server. That works everywhere `kubectl` works, but one lost packet stalls
every tunneled connection at once, and the API server was never designed to
be a data plane.

The traffic-manager can additionally expose a **QUIC endpoint**. When it is
reachable, clients upgrade to it automatically: each tunneled connection gets
its own independently retransmitted QUIC stream, the tunnel survives your
workstation changing networks, and the API server is taken out of the data
path. The port-forwarded transport remains the default and the fallback, so
enabling QUIC never breaks a client that cannot reach the endpoint. See the
[QUIC Tunnel Transport](../reference/quic-transport.md) reference for how the
upgrade, discovery, and its security model work.

## Prerequisites

- A traffic-manager and clients of version 2.31 or later.
- A way to route UDP from your workstations to the cluster: a `LoadBalancer`
  Service that supports UDP, or node ports reachable from the developer
  network.

## Enable the endpoint

Enable the listener and its Service when installing or upgrading the
traffic-manager:

```console
$ telepresence helm upgrade --set quicTunnel.enabled=true
```

This makes the traffic-manager listen on UDP port 7778 and creates a
`traffic-manager-quic` Service of type `LoadBalancer` in front of it. Once
the Service has an external address, the traffic-manager discovers it
itself and starts advertising it to clients — no further configuration.

### Using a NodePort instead

On clusters where a UDP `LoadBalancer` isn't available but the nodes are
reachable from the developer network, switch the Service to `NodePort`:

```console
$ telepresence helm upgrade \
  --set quicTunnel.enabled=true \
  --set quicTunnel.service.type=NodePort
```

The traffic-manager discovers the assigned node port and the cluster's node
addresses (preferring a node's external IP, falling back to its internal IP)
and advertises them the same way. This needs the traffic-manager's
ServiceAccount to have read access to Nodes, which a cluster-scoped install
has by default; a namespace-scoped install does not, and needs the explicit
override below instead.

## When discovery cannot see your topology

Set `quicTunnel.externalHost` (and, if the Service remaps the port,
`quicTunnel.externalPort`) to bypass discovery entirely and advertise an
address you choose. This is the right tool when:

- A NAT or proxy sits in front of the `LoadBalancer`, so the address the
  traffic-manager observes isn't the address clients must dial.
- The Service's external port isn't the port clients should use.
- The reachable address is a DNS name that only resolves on the developer
  VPN, which the traffic-manager has no way to discover.
- The install is namespace-scoped and the Service is `NodePort` (see above).

```console
$ telepresence helm upgrade \
  --set quicTunnel.enabled=true \
  --set quicTunnel.service.type=NodePort \
  --set quicTunnel.service.nodePort=30777 \
  --set quicTunnel.externalPort=30777 \
  --set quicTunnel.externalHost=<address of a node>
```

An explicit `externalHost` always wins over discovery, and discovery never
even starts once it's set.

## Verify

Reconnect, then check which transport serves the tunnel:

```console
$ telepresence quit
$ telepresence connect
$ telepresence status
...
Root Daemon    : Running
  ...
  Tunnel transport: quic (203.0.113.7:7778)
```

`quic (host:port)` means the upgrade succeeded. `grpc` means the client
stayed on the port-forwarded transport — expected when the endpoint isn't
advertised (nothing discovered yet, or discovered and reachable) or the
client cannot reach any advertised address over UDP. `grpc (fallback)` means
the QUIC connection was lost mid-session and the client downgraded; the next
`telepresence connect` will try QUIC again.

## Troubleshooting

The upgrade is deliberately silent: a client that cannot reach the endpoint
connects normally over the port-forwarded transport. If `telepresence status`
keeps reporting `grpc`:

- Confirm the endpoint is advertised: either `quicTunnel.externalHost` is
  set, or discovery has found a candidate (`kubectl get svc
  traffic-manager-quic` shows an external IP/hostname for a `LoadBalancer`,
  or a `nodePort` for a `NodePort` Service the traffic-manager can list
  Nodes for). Otherwise the traffic-manager tells clients the endpoint is
  disabled.
- Confirm UDP actually reaches one of the advertised addresses from your
  network; corporate networks and some cloud load balancers drop or don't
  support UDP.
- The root daemon's log (`daemon.log`) contains the reason for a failed
  upgrade attempt at connect time.
