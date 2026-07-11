---
title: Glossary
description: Definitions of the terms used throughout the Telepresence documentation.
---

# Glossary

**Agent injector** — A mutating webhook served by the traffic-manager. It
rewrites a workload's pod template to add the traffic-agent sidecar when an
attachment requires one. See
[Traffic Agent Sidecar](../reference/attachments/sidecar.md).

<!-- The old term is mentioned on purpose. -->
<!-- vale Telepresence.Terminology = NO -->
**Attachment** — The coupling of your workstation to a container in a cluster
workload, giving your locally running code the container's traffic,
environment variables, and volumes. Created with one of the four mode
commands — `replace`, `intercept`, `wiretap`, or `ingest` — and ended with
`telepresence detach`. See [Attachments](attachments.md). Releases before
2.30 called this an *engagement*.
<!-- vale Telepresence.Terminology = YES -->

**Connection** — The link between your workstation and a cluster established
by `telepresence connect`. A connection makes the cluster's network reachable
locally and is a prerequisite for attachments. Multiple named connections can
be active at once.

**Ingest** — The attachment mode that makes a container's environment and
volumes (read-only) available locally without touching any traffic.

**Intercept** — The attachment mode that reroutes requests destined for a
service port to your workstation, optionally narrowed down with HTTP header
and path filters, while the remote container keeps running.

**Node-agent** — A traffic-agent that runs as a node-hosted pod and attaches
to an existing pod's Linux namespaces instead of being injected as a sidecar.
It leaves the workload unmodified and its pods unrestarted, but runs
privileged. See [Node-hosted traffic-agent](../reference/node-agent.md).

**Replace** — The attachment mode that removes the targeted container from
the workload's pods and reroutes everything intended for it to your
workstation. The container is restored when the attachment ends.

**Root daemon** — The Telepresence process that runs with elevated privileges
on your workstation and manages the virtual network interface and DNS. Not
needed when connecting with `--docker`.

**Sidecar** — The default way to run the traffic-agent: as an extra container
that the agent injector adds to the workload's pods. Injection restarts the
pods once; the node-agent is the alternative that avoids this.

**Traffic-agent** — The cluster-side component that serves attachments for
one workload: it relays traffic, environment, and volumes between the pod and
your workstation. Runs as a sidecar or as a node-agent.

**Traffic-manager** — Telepresence's central cluster-side component,
installed once per cluster by an administrator (`telepresence helm install`).
It coordinates clients and traffic-agents and serves the agent injector.

**User daemon** — The Telepresence process that runs with your user
privileges on your workstation. It talks to the traffic-manager and manages
the lifecycle of connections and attachments.

**Virtual network interface (VIF)** — The network device that the root daemon
creates on connect. It routes the cluster's subnets so that every local tool
can reach cluster services. See
[Connection Routing](../reference/routing.md#the-virtual-network-interface).

**Wiretap** — The attachment mode that sends a copy of a service port's
traffic to your workstation while the cluster serves all requests as usual.
Responses from your local service are discarded.
