---
title: Traffic Agent Sidecar
---
# Traffic Agent Sidecar

When replacing a container or intercepting a service, the Telepresence Traffic Manager ensures
that a Traffic Agent has been injected into the targeted workload.
The injection is triggered by a Kubernetes Mutating Webhook and will
only happen once. The Traffic Agent is responsible for making the environment and volumes available
on the developer's workstation, and also for redirecting traffic to it.

When replacing a workload container, all traffic intended for it will be rerouted to the local workstation, unless
limited using the `--port` flag.

When intercepting, all `tcp` and/or `udp` traffic to the targeted port is sent to the developer's workstation.

This means that both a `replace` and an `intercept` will affect all users of the targeted workload.

## Supported workloads

Kubernetes has various
[workloads](https://kubernetes.io/docs/concepts/workloads/).
Currently, Telepresence supports installing a
Traffic Agent container on `Deployments`, `ReplicaSets`, `StatefulSets`, and `ArgoRollouts`. A Traffic Agent is
installed the first time a user makes a `telepresence replace WORKLOAD`, `telepresence ingest WORKLOAD`,
`telepresence intercept WORKLOAD`, or a `telepresence connect --proxy-via CIDR=WORKLAOD`.

A Traffic Agent may also be installed up front by adding a `telepresence.io/inject-traffic-agent: enabled`
annotation to the WORKLOADS pod template.

### Sidecar injection

The actual installation of the Traffic Agent is performed by a mutating admission webhook that calls the agent-injector
service in the Traffic Manager's namespace.

The configuration for the sidecar, which is automatically generated, resides in the configmap `telepresence-agents`.

### Uninstalling the Traffic Agent

A Traffic Agent will normally remain in the workload's pods once it has been installed. It can be explicitly removed by
issuing the command `telepresence uninstall WORKLOAD`. It will also be removed if its configuration is removed
from the `telepresence-agents` configmap.

Removing the `telepresence-agents` configmap will effectively uninstall all injected Traffic Agents from the same
namespace.

> [!NOTE]
> Uninstalling will not work if the Traffic Agent is installed using the pod template annotation.

### Disable Traffic Agent in a workload

The Traffic Agent installation can be completely disabled by adding a `telepresence.io/inject-traffic-agent: disabled`
annotation to the WORKLOADS pod template. This will prevent all attempts to do anything with the workload that will
require a Traffic Agent.

### Disable workloads

By default, traffic-manager will observe `Deployments`, `ReplicaSets` and `StatefulSets`.
Each workload used today adds certain overhead. If you are not engaging a specific workload type, you can disable it to reduce that overhead.
That can be achieved by setting the Helm chart values `workloads.<workloadType>.enabled=false` when installing the traffic-manager.
The following are the Helm chart values to disable the workload types:

- `workloads.deployments.enabled=false` for `Deployments`,
- `workloads.replicaSets.enabled=false` for `ReplicaSets`,
- `workloads.statefulSets.enabled=false` for `StatefulSets`.

### Enable ArgoRollouts

In order to use `ArgoRollouts`, you must pass the Helm chart value `workloads.argoRollouts.enabled=true` when installing the traffic-manager.
It is recommended to set the pod template annotation `telepresence.io/inject-traffic-agent: enabled` to avoid creation of unwanted
revisions.

> [!NOTE]
> While many of our examples use Deployments, they would also work on other supported workload types.
