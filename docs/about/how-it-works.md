---
layout: doc
weight: 2
title: "How it works"
categories: about
---

### Why it works the way it does

Our goals for Telepresence are:

1. **Transparency:** make the local proxied process match the Kubernetes environment as closely as possible.
2. **Isolation:** only the proxied process has its environment modified.
    This goal is very much like that of a container: an isolated process-specific environment.
3. **Cross-platform:** Linux and macOS work the same way, when possible.
   In general Linux provides far more capabilities (mount namespaces, bind mounts, network namespaces) than macOS.

Achieving all these goals is not always possible, of course.

One approach we considered early on was using a VPN for network proxying, but that is hard to reconcile with the goal of isolation (at least on macOS.)
Our chosen approach, described below, suffers from [some limitations](/user-guide/limitations-and-workarounds.html), so we might add support for VPN-based proxying in the future.
If this interests you please leave a comment or vote on the [VPN issue in GitHub](https://github.com/datawire/telepresence/issues/128).

### How it works

Telepresence works by building a two-way network proxy (bootstrapped using `kubectl port-forward`) between a custom pod running inside a remote Kubernetes cluster and a process running on your development machine.
The custom pod is substituted for your normal pod that would run in production.

Environment variables from the remote pod are made available to your local process.
In addition, the local process has its networking transparently overridden such that DNS calls and TCP connections are routed over the proxy to the remote Kubernetes cluster.
This is implemented using `LD_PRELOAD`/`DYLD_INSERT_LIBRARIES` mechanism on Linux/OSX, where a shared library can be injected into a process and override library calls.

Volumes are proxied using [sshfs](https://github.com/libfuse/sshfs), with their location available to the container as an environment variable.

The result is that your local process has a similar environment to the remote Kubernetes cluster, while still being fully under your local control.

