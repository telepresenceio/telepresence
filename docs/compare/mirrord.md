---
title: "mirrord vs Telepresence"
description: "An honest, maintainer-written comparison of mirrord and Telepresence for local Kubernetes development: architectures, security models, a feature table, and when each tool is the better alternative."
hide_table_of_contents: true
---

# mirrord vs Telepresence

Telepresence and mirrord solve the same problem: run one service on your
workstation, with your own IDE and debugger, while it behaves as if it were
running inside the cluster. They solve it with fundamentally different
architectures, and nearly every practical difference between the two tools
follows from that choice. Neither architecture is simply better — they
distribute their costs differently, and which costs you would rather pay is
what should decide, whether you are picking a first tool or looking for an
alternative to the one you use today.

## Two architectures

**Telepresence works at the network level.** A traffic-manager is installed in
the cluster once, by an administrator. When you connect, a virtual network
interface (VIF) and a DNS resolver make the cluster's subnets and service names
reachable from your workstation, for every local tool. To attach to a workload —
by [replace, intercept, wiretap, or ingest](../howtos/attach.md) — a
traffic-agent relays traffic, environment, and volumes between the pod and your
workstation.

**mirrord works at the process level.** Nothing is preinstalled in the cluster.
The mirrord CLI links your local process with a shared library
(`mirrord-layer`) that intercepts its system calls — network, file, and
environment access — and reroutes them to an ephemeral `mirrord-agent` pod,
created on demand next to the target and removed when the session ends. Only
that one process sees the cluster; nothing else on your workstation changes.

## Where mirrord has the edge

- **Nothing to install in the cluster.** The agent is created on demand and
  gone when you're done. Trying mirrord against a cluster takes one command;
  Telepresence always needs the traffic-manager installed first. The flip side:
  every mirrord OSS user needs RBAC to create pods with capabilities such as
  `CAP_SYS_ADMIN`, `CAP_SYS_PTRACE`, and `CAP_NET_ADMIN` — permissions many
  organizations reserve for administrators. Telepresence concentrates the
  privileged part in the one-time install and lets clients connect with next to
  no RBAC.
- **The effect is scoped to one process.** No virtual interface, no DNS
  reconfiguration, no subnet conflicts with corporate VPNs, and concurrent
  sessions against different clusters are trivial. Telepresence manages such
  conflicts well (see [Telepresence and VPNs](../reference/vpn.md)), but it has
  to manage them; mirrord sidesteps them by design.
- **Remote files without mount software.** File access is intercepted at the
  system-call layer, so remote files are visible to the process without FUSE.
  Telepresence's volume mounts rely on `sshfs` (FUSE-T on macOS, WinFSP on
  Windows).

## Where Telepresence has the edge

Code injection is a trade-off Telepresence knows well: Telepresence 1.x used
the [same approach](https://www.getambassador.io/blog/code-injection-on-linux-and-macos)
and abandoned it because injection only works for dynamically linked
executables on platforms that permit it. This is where the network-level
architecture pays off:

- **Runs natively on Windows.** Library injection has no native Windows
  support.
- **Works with any executable.** Statically linked binaries — the norm for Go —
  cannot be reliably injected. macOS SIP blocks `DYLD_INSERT_LIBRARIES`,
  forcing workarounds on Apple silicon.
- **Runs unmodified containers.** A container image can be run locally as-is,
  with the remote environment and volumes, via
  [`telepresence docker-run`](../howtos/docker.md) or the
  [Docker Compose integration](../howtos/docker-compose.md); injection would
  require rebuilding the image with the layer inside.
- **The whole workstation joins the cluster network.** Your browser, `curl`,
  database GUIs, and test suites can all reach cluster services by name — not
  just the one injected process. Telepresence can also be used as a plain
  cluster VPN, with no attachment at all.
- **Made for organization-wide rollout.** One audited, privileged install;
  clients with minimal RBAC; centralized client configuration through the Helm
  chart.
- **More attachment modes.** Besides intercepting and mirroring traffic,
  Telepresence can replace a container entirely (useful for queue consumers
  that must not run twice) and ingest a container's environment and volumes
  without touching traffic.
- **Local routing between concurrent attachments.** When you run several
  services of a call chain locally — multiple intercepts or replaces at once —
  a request from one of them to another is connected directly on your
  workstation instead of doing a round trip to the cluster just to be routed
  back again (the [local shortcut](../reference/config.md#intercept)). With
  per-process injection, every hop between the local services goes through
  the cluster.
- **An optional QUIC data path.** By default, both tools tunnel all traffic
  through the Kubernetes API server: one TCP connection, where a single lost
  packet stalls every flow sharing it (head-of-line blocking) and a busy
  apiserver throttles the session. For mirrord that path is the only one.
  Telepresence keeps it as the zero-configuration default, but a cluster
  operator can opt in to a QUIC endpoint: tunneled flows then ride
  independently retransmitted streams over an encrypted UDP path that leaves
  the apiserver out entirely, survives the laptop switching networks, and
  falls back to the API-server path silently whenever UDP is blocked. Trust
  still bootstraps from your kubeconfig — no new credentials to manage.

## Choosing between them

If you are an individual developer with broad permissions on the cluster and
you want to be productive in the next five minutes, mirrord's zero-install
onboarding is hard to beat. If you develop on Windows or in Go, run your
services as containers, need more than one process to see the cluster, or are
a platform team rolling a tool out to an organization with locked-down RBAC,
Telepresence's trade-offs are the ones you want.

## Feature comparison

This comparison applies to the Open Source editions of both products.

| Feature                                                              | Telepresence | mirrord |
|----------------------------------------------------------------------|--------------|---------|
| Requires nothing preinstalled in the cluster                         | ❌            | ✅       |
| Client needs no elevated cluster permissions (RBAC)                  | ✅            | ❌       |
| Does not need administrative permission on workstation               | ✅ [^1]       | ✅       |
| Effect is limited to the targeted process                            | ❌ [^2]       | ✅       |
| Remote volumes without extra mount software (FUSE)                   | ❌            | ✅       |
| Cluster network available to all local tools (including browser)     | ✅            | ❌       |
| Can act as a cluster VPN only                                        | ✅            | ❌       |
| Runs natively on Windows                                             | ✅            | ❌       |
| Works with statically linked binaries                                | ✅            | ❌       |
| Can run unmodified Docker containers locally                         | ✅            | ❌       |
| Integrates with Docker Compose                                       | ✅            | ❌       |
| Can intercept traffic                                                | ✅            | ✅       |
| Can filter intercepted traffic on HTTP headers and paths             | ✅            | ✅       |
| Can mirror traffic                                                   | ✅            | ✅       |
| Can intercept traffic to and from the pod's localhost                | ✅            | ❌       |
| Can replace a container                                              | ✅            | ❌       |
| Can ingest a container                                               | ✅            | ❌       |
| Routes traffic between concurrent local attachments locally          | ✅            | ❌       |
| Optional QUIC transport that takes the data path off the API server  | ✅            | ❌       |
| Works without restarting the remote workload                         | ✅ [^3]       | ✅       |
| Centralized client configuration through a Helm chart                | ✅            | ❌       |

[^1]: Telepresence does not require root access on the workstation when
installed using a package installer (which configures the root daemon as a
system service) or when running in docker mode.

[^2]: When connecting with `telepresence connect --docker`, the network access
is confined to containers instead of affecting the whole workstation.

[^3]: With the default sidecar, the pods restart once when the traffic-agent is
injected (pre-installing the agent avoids this). Attaching with the optional
[node-agent](../reference/node-agent.md) mode never modifies or restarts the
workload; like mirrord's agent, it runs privileged, whereas the default sidecar
needs no special capabilities.
