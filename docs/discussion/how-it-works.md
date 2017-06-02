---
layout: doc
weight: 2
title: "How it works"
categories: discussion
---

### Goals

Our goals for Telepresence are:

1. **Transparency:** make the local proxied process match the Kubernetes environment as closely as possible.
2. **Isolation:** only the proxied process has its environment modified.
    This goal is much like that of a container: an isolated process-specific environment.
3. **Cross-platform:** Linux and macOS work the same way, when possible.
   Linux provides far more capabilities (mount namespaces, bind mounts, network namespaces) than macOS.
4. **Compatibility:** works with any program.

Achieving all these goals at the same time is not always possible.
We've therefore chosen to support more than one method of proxying, with the different methods each having its own [benefits and limitations](/references/methods.html).

### How it works

Telepresence works by building a two-way network proxy (bootstrapped using `kubectl port-forward` or `oc port-forward`) between a custom pod running inside a remote (or local) Kubernetes cluster and a process running on your development machine.
The custom pod is substituted for your normal pod that would run in production.
Typically you'd want to do this to a testing or staging cluster, not your production cluster.

Environment variables from the remote pod are made available to your local process.
In addition, the local process has its networking transparently overridden such that DNS calls and TCP connections are routed over the proxy to the remote Kubernetes cluster.
This happens one of two ways:

* When using `--method inject-tcp` this is implemented using `LD_PRELOAD`/`DYLD_INSERT_LIBRARIES` mechanism on Linux/OSX, where a shared library can be injected into a process and override library calls.
  In particular, it overrides DNS resolution and TCP connection and routes them via a SOCKS proxy to the cluster.
  We wrote [a blog post](https://www.datawire.io/code-injection-on-linux-and-macos/) explaining LD_PRELOAD in more detail.
* When using `--method vpn-tcp`, a VPN-like tunnel is created using a program called [sshuttle](http://sshuttle.readthedocs.io/), which tunnels the packets over the SSH connection, and forwards DNS queries to a DNS proxy in the cluster.

Volumes are proxied using [sshfs](https://github.com/libfuse/sshfs), with their location available to the container as an environment variable.

The result is that your local process has a similar environment to the remote Kubernetes cluster, while still being fully under your local control.
