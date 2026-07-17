---
title: Choose between the sidecar and the node-agent
description: When to attach to workloads with the injected traffic-agent sidecar and when to use the node-hosted traffic-agent, with configuration examples for both.
hide_table_of_contents: true
---

# Choose between the sidecar and the node-agent

Telepresence can place its traffic-agent next to your application in two
ways. Both provide the same client experience — intercepts, wiretaps,
ingests, environment, and volume mounts — but they differ in how the agent
reaches the workload, what privileges they need, and what they leave behind.

- The **injected sidecar** (the default): a mutating webhook rewrites the
  workload's pod template, and the pods restart with the agent container
  inside. See the [Traffic Agent Sidecar](../reference/attachments/sidecar.md)
  reference.
- The **node-agent**: the traffic-manager creates a node-pinned agent that
  attaches to the *existing* pod's Linux namespaces from the outside. The
  workload is never modified and its pods never restart. See the
  [Node-hosted Traffic Agent](../reference/node-agent.md) reference.

## Pros and cons

| | Injected sidecar | Node-agent |
|---|---|---|
| Workload modification | Pod template is rewritten; pods restart once when the agent is first injected | None; the node-agent attaches to the existing pod as-is |
| Privileges required | None beyond the webhook; works under restrictive Pod Security Standards and on clusters like GKE Autopilot | Privileged: `hostPID`, `SYS_ADMIN`/`SYS_PTRACE`/`NET_ADMIN`/`NET_RAW`, and a read-only mount of the node's container-runtime socket. The traffic-manager's namespace must permit the *privileged* Pod Security Standard |
| Agent-injector webhook | Required (the API server must be able to reach it) | Not used; works with `agentInjector.enabled=false` |
| Replicas | The agent is injected into every pod of the workload | One node-agent Job per replica; every replica is attached, at the cost of one privileged Job per replica |
| `replace` | Supported | Not supported |
| Cleanup | The sidecar remains in the pods after the attachment ends (removed by `telepresence uninstall` or `helm uninstall`) | The agent Job is reaped as soon as nothing uses it, and on `helm uninstall` |
| Service mesh | Full support, including names only resolvable inside the mesh | Traffic interception coexists with a mesh, but mesh-only DNS names (e.g. Istio `ServiceEntry` hosts) do not resolve during the attachment |
| User-namespaced pods (`hostUsers: false`) | Supported | Not supported yet |
| Environment fidelity | The webhook copies the container's declared `Env`/`EnvFrom` | The *actual* runtime environment is read from the running process |

Rules of thumb:

- Prefer the **sidecar** when the cluster restricts privileged pods, when you
  need `replace`, or when you depend on mesh-internal DNS.
- Prefer the **node-agent** when restarting the workload is expensive or
  disallowed, when an operator reconciles the pod spec and fights the
  webhook, or when the agent-injector webhook cannot be used at all.

The two modes coexist in one cluster, but never on the same workload at the
same time — the traffic-manager rejects mixing them (see
[Coexistence rules](../reference/node-agent.md#coexistence-rules)).

## Configuration examples

### Default: sidecar only

Nothing to configure. The agent-injector is enabled by default and node-agent
mode is off:

```console
$ telepresence helm install
$ telepresence intercept my-service --port 8080
```

### Both modes available, the developer chooses

Enabling `nodeAgent.enabled` also flips the client-side default to the
node-agent (see the next section), so an administrator who wants to keep the
sidecar as the default while still making the node-agent available opts the
client-side default back out explicitly:

```console
$ telepresence helm install --set nodeAgent.enabled=true --set client.nodeAgent.enabled=false
```

A developer picks the node-agent per attachment:

```console
$ telepresence intercept my-service --port 8080 --node-agent
```

or makes it their personal default in
[`config.yml`](../reference/config.md#node-agent):

```yaml
nodeAgent:
  enabled: true
```

### The administrator makes the node-agent the cluster-wide default

Everything under the Helm chart's `client` section flows into the client
configuration that connecting workstations receive. `client.nodeAgent.enabled`
defaults to the chart's own `nodeAgent.enabled` value, so simply turning on
node-agent mode already makes it the client-side default too — no separate
`client` setting is required:

```yaml
# values.yaml
nodeAgent:
  enabled: true
```

```console
$ telepresence helm install -f values.yaml
```

Developers now attach through node-agents without passing any flag. A
workstation can still opt out of a single attachment with
`--node-agent=false` (falling back to the sidecar), but cannot opt out
through its local `config.yml` — for booleans, an explicit `false` is
indistinguishable from unset in the configuration merge.

### Node-agent only

Adding `agentInjector.enabled=false` removes the mutating webhook entirely,
so nothing can ever be injected:

```yaml
# values.yaml
nodeAgent:
  enabled: true
agentInjector:
  enabled: false
```

With this configuration `replace` is unavailable (it requires the sidecar),
and a per-attachment `--node-agent=false` fails with an
"agent-injector is disabled" error instead of falling back.

The node's container runtime is detected automatically: containerd, CRI-O,
k3s, and cri-dockerd (the docker runtime) are all recognized. Only when the
runtime listens on a nonstandard socket path does `nodeAgent.criSocket` need
to point at it explicitly.

### Flag and configuration precedence

From highest to lowest:

1. An explicit `--node-agent` flag (either value).
2. The workstation's `config.yml` `nodeAgent.enabled` (when set to a
   non-default value).
3. The cluster-provided `client.nodeAgent.enabled` from the Helm chart. This
   defaults to the chart's own `nodeAgent.enabled` value, unless the
   administrator sets `client.nodeAgent.enabled` explicitly (either value),
   which always overrides that default.
4. Off — the sidecar is used.

## Further reading

- [Node-hosted Traffic Agent](../reference/node-agent.md) — technical
  reference: Job lifecycle, namespace entry, packet routing, limitations.
- [Traffic Agent Sidecar](../reference/attachments/sidecar.md) — injection,
  annotations, and sidecar removal.
- [Traffic-agent packet routing](../reference/agent-packet-routing.md) — the
  nftables ruleset both modes share.
- [Client configuration](../reference/config.md) — the `config.yml` settings
  and how the Helm chart distributes them.
- [Cluster-side configuration](../reference/cluster-config.md) — webhook and
  injection controls.
