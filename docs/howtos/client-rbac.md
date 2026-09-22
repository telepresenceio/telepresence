---
title: Minimize the client's cluster permissions
description: Shrink the Kubernetes permissions a Telepresence client needs, step by step, down to a single named grant — or none at all.
---

# Minimize the client's cluster permissions

A Telepresence client has never needed elevated cluster permissions, but it
used to need a handful of mechanical grants: resolving the traffic-manager
to a pod, listing namespaces for name completion, reading pod logs for
`telepresence gather-logs`. None of those exist because the client must be
the one doing the work. The traffic-manager authenticates every caller as a
real Kubernetes identity and authorizes what that identity may do (see
[Authentication and authorization](../reference/authentication.md)), so it
can act as the client's deputy: serve namespace discovery and log gathering
itself, and enforce policy with `SubjectAccessReview`s against the caller's
verified identity instead of relying on what the client can physically
reach.

That turns the client's RBAC footprint into a dial. Each step below removes
a class of grants, states what it requires, and what — if anything — it
costs. The steps are independent unless noted; the end of the page has the
whole ladder in one table.

## Step 1: drop the discovery and diagnostic grants

```yaml
clientRbac:
  legacyAccess: false
```

The traffic-manager runs as a single-replica StatefulSet, so its pod name
is known without looking: `traffic-manager-0`. With `legacyAccess: false`
the chart stops rendering the discovery rules (`services` get, `pods`
get/list in the manager's namespace) and the per-namespace diagnostic
grants (`pods` get/list, `pods/log` get), leaving a connect Role with a
single rule: `pods/portforward` create, scoped by `resourceNames` to that
one pod. Namespace discovery and `telepresence gather-logs` keep working —
the manager serves both, and controls log access per namespace with the
`logs.telepresence.io` grant described below.

Against a traffic-manager at v2.32 or later, an explicit
`--mapped-namespaces` list needs no `pods` grant either: the client trusts
the manager's own attachment review instead of probing `get pods` in each
listed namespace.

This requires clients at the release that introduced known-name connection
or later, and the default `apiPort`; older clients must resolve the
`traffic-manager` Service to a pod themselves, which is exactly what the
legacy grants permit. See [RBAC](../reference/rbac.md) for the rendered
roles in both forms.

## Step 2: enforce authentication

```yaml
security:
  authentication:
    mode: enforcing
```

Steps 3 and 4 move enforcement from the API server to the traffic-manager,
which only means something when the manager rejects callers it cannot
authenticate. Under the default `permissive` mode an unauthenticated caller
is still admitted (and merely logged), so the required grant below
would have nothing to bite on. [Authentication and
authorization](../reference/authentication.md) covers what enforcing mode
requires — most notably that every client's kubeconfig can produce a bearer
token or a verifiable client certificate.

## Step 3: authorize with Telepresence's own grants

```yaml
security:
  authorization:
    requiredGrant: telepresence
```

By default, the manager authorizes a connection or an attachment by asking
whether the caller holds `pods/portforward` in the relevant namespace —
the permission a client historically exercised to reach the manager or an
agent, doubling as policy. Requiring the `telepresence` grant replaces that
proxy with grants that exist purely as policy, in the `telepresence.io` API
group:

| Grant | Authorizes |
|-------|------------|
| `connections` create (manager namespace) | Establishing a session. |
| `attachments` create / get (target namespace) | Attaching to a workload: create for intercept, replace, and wiretap; get for ingest. |
| `logs` and `logs/yaml` get (target namespace) | Gathering that namespace's pod logs (`logs`) and including pod manifests in the result (`logs/yaml`). The two are reviewed independently: a caller granted `logs` alone gets the logs, with the manifests simply omitted. |

These resources are never exercised against the Kubernetes API server —
the manager evaluates them with `SubjectAccessReview`s — so granting them
confers nothing outside Telepresence. They are ordinary RBAC in every
other way: bind them with Roles per namespace or a ClusterRole, and scope
`attachments` down to individual workloads with `resourceNames`. The chart
renders matching client Roles for whichever grant is required
(`clientRbac.subjects` decides who they bind).

With `telepresence` as the required grant, the per-namespace
`pods/portforward` grant disappears from the client Roles. Its mechanical
use goes with it: the client's first direct port-forward dial to a
traffic-agent in that namespace is refused, and there is no manager relay
to fall back on. An attachment in that namespace then needs the [QUIC
transport](../reference/quic-transport.md), decided independently of this
grant when an agent is first dialed; without it, the intercept, replace, or
ingest fails immediately with a clear error. The intermediate `any` setting
(the default) accepts either grant during a migration.

This is what the client's permissions look like at this step, for a
developer who connects and attaches to two named workloads in the `shop`
namespace. One Kubernetes grant remains: the port-forward to the
traffic-manager's pod, which is how the client still reaches the manager.
Everything else is policy in the `telepresence.io` group:

```yaml
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: traffic-manager-connect
  namespace: ambassador
rules:
  # The one remaining Kubernetes grant: reaching the manager's pod.
  - apiGroups: [""]
    resources: ["pods/portforward"]
    resourceNames: ["traffic-manager-0"]
    verbs: ["create"]
  # Policy: may this identity establish a session?
  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]
---
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: telepresence-shop
  namespace: shop
rules:
  # Policy: may this identity attach to these workloads? "create" covers
  # intercept, replace, and wiretap; "get" covers ingest.
  - apiGroups: ["telepresence.io"]
    resources: ["attachments"]
    resourceNames: ["cart", "checkout"]
    verbs: ["create", "get"]
  # Policy: may this identity gather the namespace's pod logs?
  - apiGroups: ["telepresence.io"]
    resources: ["logs", "logs/yaml"]
    verbs: ["get"]
```

Bind both Roles to the developer with ordinary RoleBindings. Nothing here
lets the identity read or change pods, services, or namespaces through the
Kubernetes API.

## Step 4: Direct Connect, no Kubernetes API access at all

```yaml
externalEndpoint:
  enabled: true
  tls:
    secretName: traffic-manager-tls   # an existing kubernetes.io/tls Secret
```

The final step removes the last grant — and with it the client's need to
contact the Kubernetes API server. The manager publishes a TLS endpoint of
its own; a client configured with `cluster.managerAddress` dials it
directly instead of port-forwarding through the API server, and makes no
Kubernetes API request from either of its daemons. The client still
authenticates with its kubeconfig credentials; reading the kubeconfig and
running its exec plugin are local operations. Attachment traffic needs the
QUIC endpoint published alongside.

This mode requires enforcing authentication — without the API server
vouching for whoever reaches the port, an unauthenticated caller must not
be admitted — and pairs naturally with `telepresence` as the required
grant and `clientRbac.create: false`, so that no Kubernetes grant, held
for whatever reason, can establish a session. Publishing the endpoint also
withholds the mechanical `pods/portforward` grants from any client Roles
the chart still renders — those clients never port-forward — leaving only
the `telepresence.io` policy grants, unless the required grant is
`portforward`. See
[External control endpoint](../reference/external-endpoint.md).

The same developer as in step 3, connecting through Direct Connect, needs
no Kubernetes grant at all. The connect Role loses its `pods/portforward`
rule, and what is left in both Roles is policy that the traffic-manager
evaluates and the Kubernetes API server never sees:

```yaml
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: traffic-manager-connect
  namespace: ambassador
rules:
  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]
---
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: telepresence-shop
  namespace: shop
rules:
  - apiGroups: ["telepresence.io"]
    resources: ["attachments"]
    resourceNames: ["cart", "checkout"]
    verbs: ["create", "get"]
  - apiGroups: ["telepresence.io"]
    resources: ["logs", "logs/yaml"]
    verbs: ["get"]
```

The Kubernetes API server would grant this identity nothing: the
`telepresence.io` resources do not exist there. Whether the developer may
connect, attach, or read logs is decided entirely by the traffic-manager,
against the identity it verified when the client authenticated. That is
what Direct Connect brings: the client's cluster footprint is the policy
you wrote, and nothing else.

## The ladder

| Client's Kubernetes permissions | Helm values | Requires |
|---------------------------------|-------------|----------|
| Discovery, diagnostics, and port-forward grants | defaults | — |
| One named `pods/portforward` in the manager namespace, `pods/portforward` per attached namespace | `clientRbac.legacyAccess: false` | current clients, default `apiPort` |
| Policy-only `telepresence.io` grants | + `security.authorization.requiredGrant: telepresence` | `security.authentication.mode: enforcing`; QUIC tunnel for attachments |
| None | + `externalEndpoint`, `clientRbac.create: false` | enforcing mode, a persisted TLS certificate, QUIC for Direct Connect attachments |

`telepresence setup` asks about each step above — whether to enforce
authentication, which grant to require, whether to enable Direct Connect,
and whether legacy clients need access — and writes the resulting values;
see [Guided cluster setup](../reference/setup.md).
