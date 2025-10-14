---
title: Intercepting Applications Using TLS/mTLS
description: How to perform HTTP-filtered intercepts with encrypted data
hide_table_of_contents: true
---

# Intercepting TLS/mTLS Applications 

## Overview

Telepresence requires access to HTTP headers and paths to perform HTTP-filtered intercepts. When traffic is encrypted with TLS/mTLS, Telepresence must decrypt the data to inspect these headers. This document explains how to configure Telepresence to handle encrypted traffic.

## Decrypting Traffic

To decrypt TLS/mTLS traffic, Telepresence needs access to the TLS certificates used by your application. You can provide this access in two ways:

1. **Mount existing volumes**: Use certificates already mounted in a volume by your application.
2. **Reference a secret**: Mount a Kubernetes secret containing the certificate directly.

### Option 1: Using a Mounted Certificate

If your application mounts a volume containing TLS certificates, the Telepresence traffic-agent automatically mounts the same volume. You only need to specify the certificate's path using an annotation.

#### Example
Suppose your application defines a `tls` volume for the secret `tel-cert`, which contains `tls.crt` and `tls.key`:

```yaml
volumes:
  - name: tls
    secret:
      secretName: tel-cert
```

The volume is mounted at `/etc/certs`:

```yaml
        volumeMounts:
          - name: tls
            mountPath: /etc/certs
            readOnly: true
```

Add the following annotation to your workload to enable Telepresence to use this certificate:

```yaml
  template:
    metadata:
      annotations:
        telepresence.io/downstream-tls-path.8443: /etc/certs
```

This annotation directs Telepresence to use the certificate at `/etc/certs` for decrypting traffic on port 8443.

**Notes:**
- For multiple ports, repeat the annotation with different port suffixes.
- Ensure the certificate is mounted by all containers whose ports are specified in the annotation.

### Using a Secret

If your application containers do not mount the TLS certificate, the traffic-agent can independently mount a Kubernetes secret. The secret must reside in the same namespace as the workload.

Add the following annotation to your workload:
```yaml
  template:
    metadata:
      annotations:
        telepresence.io/downstream-tls-secret.8443: secret-name
```

This annotation prompts the traffic-agent injector to:
1. Add a volume for the specified secret to the pod
2. Mount that volume where the traffic-agent can access the certificate

## Encrypting Upstream Traffic

After decrypting traffic and inspecting HTTP filters, Telepresence must re-encrypt the traffic before forwarding it to the application. For applications requiring mutual TLS (mTLS), Telepresence must use the client-side TLS certificate for the upstream connection. Use annotations similar to those for decrypting traffic, but with the prefix `telepresence.io/upstream-tls-` instead of `telepresence.io/downstream-tls-`.

### Self-Signed Certificates
Self-signed certificates are common in development environments, and services that use them can be accessed using `curl --insecure` or `curl -k`. Telepresence cannot detect whether this option was used. If downstream traffic is decrypted for HTTP filtering and the application uses a self-signed certificate, Telepresence will fail to establish a secure upstream connection unless verification is skipped.

To bypass verification for self-signed certificates, add the following annotation to the workload:
```yaml
telepresence.io/upstream-insecure-skip-verify.<port>: enabled
```

## Using the --plaintext option

The `--plaintext` option for intercepts or wiretaps disables encryption of traffic sent to the client during an intercept or wiretap.

## Protocol Selection

Telepresence supports HTTP-filtered intercepts on both clear-text and TLS encrypted ports that use either HTTP/1.x and HTTP/2. Telepresence will automatically detect the protocol used by the application and use the appropriate protocol for the intercept. This is done by inspecting the [appProtocol](https://kubernetes.io/docs/concepts/services-networking/service/#application-protocol) of the Kubernetes service for the workload, or if that is not set, by looking at the port name and number or by probing the application. See below for more details.

Telepresence will never use TLS or HTTP/2 for services that have a `appProtocol` that is `tcp` or `udp`. Those values imply a "no app-layer sniffing" mode, so when set, they effectively rule out all uses of HTTP-filtered intercepts on encrypted traffic.

### TLS Detection
Telepresence will always use TLS for service ports that have:

- A TLS certificate configured using the `telepresence.io/downstream-tls-path.<port>` or `telepresence.io/downstream-tls-secret.<port>` annotation.`
- An `appProtocol` that is `kubernetes.io/wss`, `wss`, `http2`, `https`, or `grpc`.
- The name `https`.
- The number 443.

Telepresence will never use TLS for service ports that have a `appProtocol` that is `kubernetes.io/ws`, `kubernetes.io/h2c`, or `h2c`.

If the selection cannot be made from the above criteria, Telepresence will probe the application to determine if TLS is used.

> [!NOTE]
> Telepresence will never care about encryption on UDP ports because HTTP filters are only supported on TCP connections. Encrypted UDP traffic can still be intercepted, but the intercept must be global as it happens in the Transport Layer.

### HTTP/2 Detection

Telepresence will always use HTTP/2 for services that have `appProtocol` that is "kubernetes.io/h2c", "h2c", "http2", or "grpc".

If the selection cannot be made from the above criteria, Telepresence will probe the application to determine if HTTP/2 is used.

### Probing

Probing is a fallback mechanism for determining the protocol. It is recommended that it is avoided by setting the appropriate [appProtocol](https://kubernetes.io/docs/concepts/services-networking/service/#application-protocol). The probing takes place when the first connection is made to an intercepted service.

If the probe times out because the application is slow to start, the default timeout of 2 seconds can be overridden with the annotation `telepresence.io/upstream-probe-timeout.<port>`. The value must be a number followed by a duration unit, such as `10s` or `500ms`.
