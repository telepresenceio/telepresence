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
upgrade and its security model work.

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
`traffic-manager-quic` Service of type `LoadBalancer` in front of it.

The endpoint is not advertised to clients until you also set the address
that they should dial. Once the Service has an external address:

```console
$ kubectl get svc traffic-manager-quic -n ambassador
NAME                   TYPE           CLUSTER-IP     EXTERNAL-IP   PORT(S)
traffic-manager-quic   LoadBalancer   10.96.202.13   203.0.113.7   7778:31234/UDP
```

set it as the external host:

```console
$ telepresence helm upgrade --set quicTunnel.enabled=true --set quicTunnel.externalHost=203.0.113.7
```

### Using a NodePort instead

On clusters where a UDP `LoadBalancer` isn't available but the nodes are
reachable from the developer network, pin a node port and advertise a node
address:

```console
$ telepresence helm upgrade \
  --set quicTunnel.enabled=true \
  --set quicTunnel.service.type=NodePort \
  --set quicTunnel.service.nodePort=30777 \
  --set quicTunnel.externalPort=30777 \
  --set quicTunnel.externalHost=<address of a node>
```

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
advertised or the client cannot reach it over UDP. `grpc (fallback)` means
the QUIC connection was lost mid-session and the client downgraded; the next
`telepresence connect` will try QUIC again.

## Troubleshooting

The upgrade is deliberately silent: a client that cannot reach the endpoint
connects normally over the port-forwarded transport. If `telepresence status`
keeps reporting `grpc`:

- Confirm the endpoint is advertised: `quicTunnel.externalHost` must be set,
  or the traffic-manager tells clients the endpoint is disabled.
- Confirm UDP actually reaches the Service from your network; corporate
  networks and some cloud load balancers drop or don't support UDP.
- The root daemon's log (`daemon.log`) contains the reason for a failed
  upgrade attempt at connect time.
