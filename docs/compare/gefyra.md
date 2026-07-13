---
title: "Telepresence vs Gefyra"
hide_table_of_contents: true
---

# Telepresence vs Gefyra

Telepresence and Gefyra solve the same problem: run a service on your
workstation, with your own tools, while it behaves as if it were running
inside the cluster. Gefyra's README describes the project as "heavily
inspired by the free part of Telepresence2", and the two tools share much of
their vocabulary — connect, then bridge (intercept) traffic. Where they part
ways is the answer to one question: *what* is connected to the cluster. For
Telepresence it is your workstation; for Gefyra it is a set of local Docker
containers.

## Two architectures

**Telepresence connects the workstation.** A traffic-manager is installed in
the cluster once, by an administrator. When you connect, a virtual network
interface (VIF) and a DNS resolver make the cluster's subnets and service
names reachable from your workstation, for every local tool, tunneled over
the same connection your `kubectl` uses. To attach to a workload — by
[replace, intercept, wiretap, or ingest](../howtos/attach.md) — a
traffic-agent relays traffic, environment, and volumes between the pod and
your workstation, where your service runs any way you like: natively under
your IDE and debugger, or [as a container](../howtos/docker.md).

**Gefyra connects local containers.** Your service must run as a Docker
container, started with `gefyra run` in a dedicated Docker network. A gateway
container (Cargo) tunnels that network to the cluster over WireGuard —
through a UDP node port — and resolves cluster names with its own CoreDNS.
The workstation itself is never touched. To take over traffic, `gefyra
bridge` has the cluster-side operator duplicate the target workload and swap
the original's container for a proxy (Carrier) that forwards requests
matching your filter rules through the tunnel to your local container; the
duplicate serves everything else.

## Where Gefyra has the edge

- **Nothing on the workstation changes.** No virtual interface, no DNS
  reconfiguration, no root daemon: cluster access is confined to the
  containers Gefyra runs. Telepresence offers the same confinement with
  [`telepresence connect --docker`](../howtos/docker.md), but for Gefyra it
  is the only mode, so the tool is simpler to reason about.
- **A container-first workflow.** If your service already ships as an image,
  Gefyra leans into that: `--env-from` copies the environment of the running
  cluster container, `--cpu-from` and `--memory-from` copy its resource
  limits, and a Docker Desktop extension drives the whole flow from a GUI.
- **A WireGuard tunnel.** Traffic to the cluster rides an encrypted UDP
  tunnel rather than a TCP-based one, which avoids TCP-over-TCP overhead on
  bulk transfers. Telepresence offers an equivalent encrypted-UDP data path
  with its opt-in QUIC transport, but for Gefyra it is the default — there is
  no cluster-side decision to make.

## Where Telepresence has the edge

- **Your service runs natively.** No image build, no container: start the
  process straight from your IDE with the debugger attached. Gefyra requires
  every local workload to be a Docker container.
- **The whole workstation joins the cluster network.** Your browser, `curl`,
  database GUIs, and test suites can all reach cluster services by name.
  Telepresence can also be used as a plain cluster VPN, with no attachment
  at all.
- **The fast path is optional, not required.** Telepresence tunnels
  everything over the same Kubernetes API connection that `kubectl` uses, so
  it works wherever `kubectl` works. Gefyra's WireGuard tunnel needs UDP node
  port 31820 reachable from the workstation — frequently impossible on
  managed or otherwise firewalled clusters — and there is no fallback when it
  isn't. On clusters where a UDP port *can* be exposed, Telepresence's opt-in
  QUIC transport provides the same class of encrypted UDP data path, with the
  trust bootstrapped from your kubeconfig and a silent per-connection
  fallback to the API-server path when the UDP route is blocked.
- **Attachments don't rewrite your workloads.** A Gefyra bridge patches the
  target workload's manifest — its container image is swapped for the
  Carrier proxy — and creates a duplicate workload beside it, which restarts
  pods and registers as drift with GitOps controllers such as Argo CD or
  Flux. Telepresence injects the traffic-agent at the pod level through an
  admission webhook, or attaches with the [node-agent](../reference/node-agent.md)
  without modifying or restarting anything.
- **Remote volumes.** Telepresence mounts the remote container's volumes
  locally; Gefyra only bind-mounts local directories into the local
  container.
- **More attachment modes.** Besides intercepting, Telepresence can mirror
  traffic with wiretap, ingest a container's environment and volumes without
  touching traffic, and intercept all traffic to a port — plain TCP
  included — where a Gefyra bridge requires at least one HTTP header or path
  filter rule.
- **Local routing between concurrent attachments.** When you run several
  services of a call chain locally, a request from one of them to another is
  connected directly on your workstation instead of doing a round trip to
  the cluster (the [local shortcut](../reference/config.md#intercept)).
- **Made for organization-wide rollout.** One audited, privileged install;
  clients with minimal RBAC; centralized client configuration through the
  Helm chart.

## Choosing between them

If your team is all-in on containers — every service ships as an image, and
Docker Desktop is the daily driver — and your cluster's nodes accept UDP
connections from your workstation, Gefyra's container-first model will feel
natural. If you want to run your service natively under a debugger, work
against managed clusters where node ports can't be opened, keep GitOps
controllers happy, mirror traffic or mount remote volumes, or roll a tool
out to an organization, Telepresence's trade-offs are the ones you want.

## Feature comparison

This comparison applies to the Open Source editions of both products.

| Feature                                                          | Telepresence | Gefyra |
|------------------------------------------------------------------|--------------|--------|
| Runs the local service natively, without Docker                  | ✅            | ❌      |
| Can run unmodified Docker containers locally                     | ✅            | ✅      |
| Does not need administrative permission on workstation           | ✅ [^1]       | ✅      |
| Cluster network available to all local tools (including browser) | ✅ [^2]       | ❌      |
| Can act as a cluster VPN only                                    | ✅            | ❌ [^3] |
| Tunnels over the Kubernetes API connection (no extra open ports) | ✅            | ❌      |
| Encrypted UDP data path (QUIC / WireGuard)                       | ✅ [^5]       | ✅      |
| Falls back to the Kubernetes API connection when UDP is blocked  | ✅            | ❌      |
| Mounts remote volumes locally                                    | ✅            | ❌      |
| Copies the remote container's environment                        | ✅            | ✅      |
| Can intercept traffic                                            | ✅            | ✅      |
| Can filter intercepted traffic on HTTP headers and paths         | ✅            | ✅      |
| Can intercept all traffic without HTTP filter rules              | ✅            | ❌      |
| Can mirror traffic                                               | ✅            | ❌      |
| Can replace a container                                          | ✅            | ✅      |
| Can ingest a container                                           | ✅            | ❌      |
| Routes traffic between concurrent local attachments locally      | ✅            | ❌      |
| Works without restarting the remote workload                     | ✅ [^4]       | ❌      |
| Does not create duplicate workloads in the cluster               | ✅            | ❌      |
| Docker Desktop extension                                         | ❌            | ✅      |
| Integrates with Docker Compose                                   | ✅            | ❌      |
| Centralized client configuration through a Helm chart            | ✅            | ❌      |

[^1]: Telepresence does not require root access on the workstation when
installed using a package installer (which configures the root daemon as a
system service) or when running in docker mode.

[^2]: When connecting with `telepresence connect --docker`, the network access
is confined to containers instead of affecting the whole workstation — the
same model Gefyra uses exclusively.

[^3]: A container started with `gefyra run` can reach the cluster without any
bridge, but only that container does; nothing else on the workstation can.

[^4]: With the default sidecar, the pods restart once when the traffic-agent
is injected (pre-installing the agent avoids this). Attaching with the
optional [node-agent](../reference/node-agent.md) mode never modifies or
restarts the workload.

[^5]: Opt-in: the cluster operator enables the QUIC endpoint in the
traffic-manager's Helm chart. Clients use it automatically when reachable and
otherwise stay on the API-server path.
