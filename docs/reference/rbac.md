---
title: RBAC
toc_min_heading_level: 2
toc_max_heading_level: 3
---

# Telepresence RBAC

Two Kubernetes identities matter to a Telepresence installation: the
`traffic-manager` ServiceAccount that the cluster-side components run as,
and the identity of each connecting user, whose kubeconfig credentials the
traffic-manager [authenticates and authorizes](authentication.md). This
page documents the permissions the Helm chart grants to each, and why, for
administrators who audit them or manage RBAC themselves.

The chart creates all of these objects: `managerRbac.create` and
`clientRbac.create` toggle the manager and client subsets, and
`rbac.only=true` installs the RBAC objects without the traffic-manager
itself. See
[Static Namespace Selection RBAC](../install/manager.md#static-namespace-selection-rbac)
and [Installing RBAC only](../install/manager.md#installing-rbac-only).
The chart is the authoritative source; to see exactly what a given
configuration grants, render it:

```console
$ helm template traffic-manager datawire/telepresence-oss -n ambassador \
    -f values.yaml -s templates/trafficManagerRbac/cluster-scope.yaml
```

The examples below assume a manager namespace of `ambassador`. The client
grants can be pared down step by step, ultimately to nothing; the
walk-through is in
[Minimize the client's cluster permissions](../howtos/client-rbac.md).

## Administrating Telepresence

Installing the traffic-manager means creating its StatefulSet, Service,
webhook configuration, and the RBAC objects on this page — including a
ClusterRole and ClusterRoleBinding in a cluster-wide installation. This is
typically done by a cluster administrator. `telepresence setup` probes
whether the current identity can create everything the chart needs and
itemizes anything missing; see
[Guided cluster setup](setup.md).

## Traffic Manager Permissions

How much the traffic-manager itself may see is decided by its namespace
selector: without one, or with a dynamic (label-based) selector, it needs
cluster-wide access; with a static selector it is confined to the selected
namespaces. See
[namespace selection](../install/manager.md#static-namespace-selection-rbac).

### Cluster-wide installation

The chart renders one ClusterRole and ClusterRoleBinding:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: traffic-manager-ambassador
rules:
  # Track workloads and their pods, discover subnets from nodes, and watch
  # namespaces -- also on behalf of connected clients, which no longer need
  # to watch namespaces themselves.
  - apiGroups: [""]
    resources: ["nodes", "services", "namespaces", "pods"]
    verbs: ["get", "list", "watch"]

  # Serve "telepresence gather-logs" on behalf of clients.
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]

  # Evict pods so that the mutating webhook re-injects them when an agent
  # is added or removed. Omitted when agentInjector.enabled is false.
  - apiGroups: [""]
    resources: ["pods/eviction"]
    verbs: ["create"]

  # The workload kinds enabled in the "workloads" Helm value; deployments,
  # replicasets, and statefulsets by default, rollouts when
  # workloads.argoRollouts.enabled is set. "patch" (agent annotation) is
  # omitted when agentInjector.enabled is false.
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets", "statefulsets"]
    verbs: ["get", "list", "watch", "patch"]

  # Surface why a pod failed to become ready (ImagePullBackOff,
  # FailedScheduling, ...) when an attachment waits on it.
  - apiGroups: ["events.k8s.io"]
    resources: ["events"]
    verbs: ["get", "watch"]

  # Describe how workloads are exposed through ingress routes, and discover
  # the service CIDR on clusters that expose the ServiceCIDR resource.
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses", "servicecidrs"]
    verbs: ["get", "list", "watch"]

  # Authenticate callers (bearer-token verification) and authorize them
  # (connect, attachment, and log-access reviews). See
  # "Authentication and authorization".
  - apiGroups: ["authentication.k8s.io"]
    resources: ["tokenreviews"]
    verbs: ["create"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["subjectaccessreviews"]
    verbs: ["create"]

  # Only used when upgrading from older versions.
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["update"]
```

A Role in the manager's own namespace supplements it: `create` on
`services`, and `get`/`list`/`watch`/`patch`/`update` on the
`traffic-manager` and `traffic-manager-install` ConfigMaps (plus
namespace-scoped `create`, which Kubernetes cannot restrict by name).

### Namespaced installation

With a static namespace selector, the ClusterRole shrinks to what is
inherently cluster-scoped — `servicecidrs` get/list/watch, `tokenreviews`
create, and `subjectaccessreviews` create — and the remaining rules become
a Role and RoleBinding in each selected namespace and in the manager's
own. Compared to the cluster-wide rules: `nodes` and `namespaces` access
disappears (the manager gets its own namespace by name, for the
install-id), the ConfigMap rules are scoped as above, and on clusters
older than Kubernetes 1.33 the manager-namespace Role adds `create` on
`services`, used for a deliberately failing dummy create whose error
message reveals the service CIDR.

### Additional bindings

- **x509 client-certificate authentication** (see
  [Authentication and authorization](authentication.md#client-certificate-only-kubeconfigs))
  adds a RoleBinding in `kube-system` to the stock
  `extension-apiserver-authentication-reader` Role, which lets the manager
  read the cluster's client CA to verify client certificates. x509 auth is
  on by default once `security.authentication.mode` is `enforcing`
  (disable it with `security.authentication.x509.enabled: false`), and the
  binding is created whenever it is active, as long as `managerRbac.create`
  is also `true`.
- **The node-agent** (`nodeAgent.enabled`) adds a Role in the manager's
  namespace granting `get`, `list`, `watch`, `create`, `delete`, and
  `deletecollection` on `batch` `jobs` — the manager provisions node-agent
  Jobs there. See [Node-hosted Traffic Agent](node-agent.md).

## Telepresence Client Access

A client acts as the Kubernetes identity of its kubeconfig context, so
"client RBAC" is ordinary RBAC bound to your users, groups, or
ServiceAccounts — the chart binds whatever `clientRbac.subjects` lists.
Two rule sets are involved: a connect Role in the manager's namespace, and
a per-namespace Role (or a ClusterRole, in a cluster-wide installation)
for the namespaces the client works in. `clientRbac.namespaces` restricts
the latter to an explicit list.

> [!IMPORTANT]
> The roles below are what the chart renders with
> `clientRbac.legacyAccess: false`. That value currently defaults to `true`
> for backward compatibility and adds the extra grants described in
> [Legacy access](#legacy-access) — turn it off to avoid the additional
> RBAC.

### Connecting

The traffic-manager runs as a single-replica StatefulSet, so its pod is
always named `traffic-manager-0`, and its API always listens on port
`apiPort` (default `8081`). A client dials that pod name and port
directly, so connecting through the Kubernetes API server takes exactly
one named grant:

```yaml
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: traffic-manager-connect
  namespace: ambassador
rules:
  # Rendered when clients port-forward through the API server (no
  # external endpoint published), and when the required grant is
  # "portforward", which reviews possession of this grant.
  - apiGroups: [""]
    resources: ["pods/portforward"]
    resourceNames: ["traffic-manager-0"]
    verbs: ["create"]
  # Rendered whenever the required grant isn't "portforward": the policy
  # grant the manager's connect review looks for.
  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]
```

The manager's connect review accepts one of these two grants, depending on
the configured required grant, and the Role always carries at least one
that satisfies it. With a published
[external control endpoint](external-endpoint.md) and the default required
grant, that is the policy grant alone: whether a client may connect is
decided solely by that grant.

### Working in a namespace

For each namespace the client attaches workloads in (every namespace, in a
cluster-wide installation — the same rules then form a ClusterRole), the
chart renders:

```yaml
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: telepresence-ambassador
  namespace: some-namespace
rules:
  # Log gathering through the traffic-manager: "logs" authorizes streaming
  # a namespace's pod logs, "logs/yaml" additionally authorizes including
  # pod manifests. Reviewed independently: with "logs" alone, gather-logs
  # returns the logs and simply omits the manifests.
  - apiGroups: ["telepresence.io"]
    resources: ["logs", "logs/yaml"]
    verbs: ["get"]

  # Rendered whenever the required grant isn't "telepresence": lets the
  # client open port-forwards directly to traffic-agents (better
  # throughput than routing via the manager), and doubles as the
  # authorization for attaching when the required grant is pods/portforward.
  # Withheld with an external endpoint published (those clients never
  # port-forward) unless the required grant is "portforward", where
  # possession of it is itself the attachment policy.
  - apiGroups: [""]
    resources: ["pods/portforward"]
    verbs: ["create"]

  # Rendered whenever the required grant isn't "portforward": the policy
  # grant the manager's attachment review looks for. "create" authorizes intercept,
  # replace, and wiretap; "get" authorizes ingest. Scope it to individual
  # workloads with resourceNames if desired.
  - apiGroups: ["telepresence.io"]
    resources: ["attachments"]
    verbs: ["create", "get"]
```

The `telepresence.io` resources are never exercised against the Kubernetes
API server — the traffic-manager evaluates them in `SubjectAccessReview`s
against the caller's verified identity — so granting them confers nothing
outside Telepresence. `clientRbac.ruleExtras` appends additional rules to
these Roles.

Without direct `pods/portforward` in the namespace, all attachment traffic
is routed via the traffic-manager, at a modest throughput cost, unless the
[QUIC transport](quic-transport.md) provides the direct path instead.

### Legacy access

Clients that predate the known-name connection cannot dial
`traffic-manager-0` directly: they resolve the `traffic-manager` Service
to a pod themselves, gather logs by reading `pods/log`, and probe
namespace accessibility by listing pods. `clientRbac.legacyAccess: true` —
currently the default, for backward compatibility with such clients — adds
the grants this requires:

- In the connect Role, the single named `pods/portforward` rule is
  replaced by discovery rules: `get`/`list` on `pods`, `get` on the
  `traffic-manager` Service, and unscoped `create` on `pods/portforward`.
- The namespace Roles gain `get`/`list` on `pods` and `get` on
  `pods/log`; the cluster-wide form also gains `get`/`list`/`watch` on
  `namespaces`.

An installation that overrides `apiPort` away from its default needs these
rules regardless of client version — a changed port breaks the known-name
connection, and clients fall back to resolving the Service. Set
`clientRbac.legacyAccess: false` to render only the roles documented
above; the value's comment in `values.yaml` records when the default flips
and when the toggle is removed.

## Creating a kubeconfig for a ServiceAccount

Human users normally connect with their own kubeconfig, but a dedicated
ServiceAccount is convenient for CI or for handing out narrowly scoped
access. Create the ServiceAccount in the manager's namespace, list it in
`clientRbac.subjects`, and mint a token for it:

```console
$ kubectl create serviceaccount tp-user -n ambassador
$ kubectl create token tp-user -n ambassador --duration 24h
```

`kubectl create token` returns a bound, expiring token (Kubernetes no
longer auto-creates Secret-based tokens for ServiceAccounts). For a
long-lived credential, create a `kubernetes.io/service-account-token`
Secret manually, as described in the
[Kubernetes ServiceAccount documentation](https://kubernetes.io/docs/concepts/security/service-accounts/#get-a-token).
Put the token in a kubeconfig context in the usual way:

```console
$ kubectl config set-credentials tp-user --token <token>
$ kubectl config set-context tp-user --cluster <cluster> --user tp-user
```

Under enforcing authentication the traffic-manager accepts exactly what
the API server accepts, so nothing beyond a valid credential is required;
which permissions the identity needs is decided by the values above — or
by none at all, at the far end of
[the minimization ladder](../howtos/client-rbac.md).
