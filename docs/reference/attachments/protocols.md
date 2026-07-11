---
title: Protocol selection
description: How Telepresence determines whether a service port uses TLS and HTTP/1.x or HTTP/2, using appProtocol, annotations, and probing.
---

# Protocol selection

Telepresence supports HTTP-filtered intercepts on both clear-text and TLS encrypted ports that use either HTTP/1.x and HTTP/2. Telepresence will automatically detect the protocol used by the application and use the appropriate protocol for the intercept. This is done by inspecting the [appProtocol](https://kubernetes.io/docs/concepts/services-networking/service/#application-protocol) of the Kubernetes service for the workload, or if that is not set, by looking at the port name and number or by probing the application. See below for more details.

Telepresence will never use TLS or HTTP/2 for services that have a `appProtocol` that is `tcp` or `udp`. Those values imply a "no app-layer sniffing" mode, so when set, they effectively rule out all uses of HTTP-filtered intercepts on encrypted traffic.

## TLS Detection
Telepresence will always use TLS for service ports that have:

- A TLS certificate configured using the `telepresence.io/downstream-tls-path.<port>` or `telepresence.io/downstream-tls-secret.<port>` annotation.`
- An `appProtocol` that is `kubernetes.io/wss`, `wss`, `http2`, `https`, or `grpc`.
- The name `https`.
- The number 443.

Telepresence will never use TLS for service ports that have a `appProtocol` that is `kubernetes.io/ws`, `kubernetes.io/h2c`, or `h2c`.

If the selection cannot be made from the above criteria, Telepresence will probe the application to determine if TLS is used.

> [!NOTE]
> Telepresence will never care about encryption on UDP ports because HTTP filters are only supported on TCP connections. Encrypted UDP traffic can still be intercepted, but the intercept must be global as it happens in the Transport Layer.

## HTTP/2 Detection

Telepresence will always use HTTP/2 for services that have `appProtocol` that is "kubernetes.io/h2c", "h2c", "http2", or "grpc".

If the selection cannot be made from the above criteria, Telepresence will probe the application to determine if HTTP/2 is used.

## Probing

Probing is a fallback mechanism for determining the protocol. It is recommended that it is avoided by setting the appropriate [appProtocol](https://kubernetes.io/docs/concepts/services-networking/service/#application-protocol). The probing takes place when the first connection is made to an intercepted service.

If the probe times out because the application is slow to start, the default timeout of 2 seconds can be overridden with the annotation `telepresence.io/upstream-probe-timeout.<port>`. The value must be a number followed by a duration unit, such as `10s` or `500ms`.
