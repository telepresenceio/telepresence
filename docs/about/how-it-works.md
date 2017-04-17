---
layout: doc
weight: 2
title: "How it works"
categories: about
permalink: /about/how-it-works
---

Telepresence works by building a two-way network proxy (bootstrapped using `kubectl port-forward`) between a custom pod running inside a remote Kubernetes cluster and a process running on your development machine.
The custom pod is substituted for your normal pod that would run in production.

Environment variables from the remote pod are made available to your local process.
In addition, the local process has its networking transparently overridden such that DNS calls and TCP connections are routed over the proxy to the remote Kubernetes cluster.
This is implemented using `LD_PRELOAD`/`DYLD_INSERT_LIBRARIES` mechanism on Linux/OSX, where a shared library can be injected into a process and override library calls.

Volumes are proxied using [sshfs](https://github.com/libfuse/sshfs), with their location available to the container as an environment variable.

The result is that your local process has a similar environment to the remote Kubernetes cluster, while still being fully under your local control.
