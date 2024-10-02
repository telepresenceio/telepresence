---
title: Traffic Agent Sidecar
---
# Intercepts

When intercepting a service, the Telepresence Traffic Manager ensures
that a Traffic Agent has been injected into the intercepted workload.
The injection is triggered by a Kubernetes Mutating Webhook and will
only happen once. The Traffic Agent is responsible for redirecting
intercepted traffic to the developer's workstation.

The intercept will intercept all`tcp` and/or `udp` traffic to the
intercepted service and send all of that traffic down to the developer's
workstation. This means that an intercept will affect all users of
the intercepted service.

## Supported workloads

Kubernetes has various
[workloads](https://kubernetes.io/docs/concepts/workloads/).
Currently, Telepresence supports intercepting (installing a
traffic-agent on) `Deployments`, `ReplicaSets`, `StatefulSets`, and `ArgoRollouts`.

### Enable ArgoRollouts

In order to use `ArgoRollouts`, you must pass the Helm chart value `workloads.argoRollouts.enabled=true` when installing the traffic-manager.
It is recommended to set the pod template annotation `telepresence.getambassador.io/inject-traffic-agent: enabled` to avoid creation of unwanted
revisions.

> [!NOTE]
> While many of our examples use Deployments, they would also work on other supported workload types.
