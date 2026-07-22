---
title: Authentication and authorization
description: How the traffic-manager authenticates clients and agents against the cluster's Kubernetes identities and authorizes intercept creation with RBAC.
---

The traffic-manager's gRPC API is reachable by anything with network access to
its port, typically over a port-forward established through the Kubernetes
API server. Without authentication, a caller that can merely reach that port —
for example, any pod in the cluster — could act as any client or agent: create
intercepts in namespaces it has no RBAC over, or drive another caller's
session. The traffic-manager closes that gap by authenticating every caller's
Kubernetes identity and authorizing intercept creation against that identity's
RBAC.

## What is protected

- **Callers are authenticated.** The traffic-manager verifies who is calling —
  a real Kubernetes identity, not just a bearer of a session ID.
- **Sessions are bound to their owner.** Once a session is created by an
  authenticated identity, only that identity can drive it — connect, tunnel
  traffic, watch intercepts, or reconnect. Knowing a session ID is no longer
  enough to act on it, in any mode.
- **Intercept creation is authorized.** Creating an intercept requires the
  caller's Kubernetes identity to actually have RBAC access to the target
  namespace.

The net effect: a workload with mere network reachability to the
traffic-manager can no longer intercept arbitrary namespaces or act on other
callers' sessions. It still needs a Kubernetes identity that Kubernetes itself
would let touch the target namespace.

This is independent of, and does not replace, restricting network reachability
to the traffic-manager in the first place. NetworkPolicy and a namespaced
install (see [RBAC](rbac.md)) remain the recommended defense in depth.

## How identity is established

### Clients

A `telepresence` client authenticates with the bearer token that its active
kubeconfig context's credentials resolve to:

- A **static token** or **token file** credential is forwarded as-is.
- An **exec credential plugin** (the common case for managed clusters — EKS,
  GKE, AKS auth plugins) is run to obtain a token, including through the
  `kubeauth` stub used in Docker mode.
- A kubeconfig whose credentials are a **client certificate only** yields no
  token at all — see [Client-certificate-only kubeconfigs](#client-certificate-only-kubeconfigs)
  below.

The client re-resolves the token for every call rather than capturing it once
at connect, so short-lived OIDC or exec tokens are refreshed transparently
over the life of a session. The traffic-manager verifies the token with a
Kubernetes `TokenReview`, which returns the authenticated username and UID.

A kubeconfig configured to **impersonate** another user or group is authorized
as the *impersonating* identity's RBAC, not the impersonated one — the
forwarded token belongs to the credential actually presented.

### Agents

A traffic-agent authenticates with a **projected ServiceAccount token** bound
to the dedicated `traffic-manager` audience. Because the token's audience is
not the Kubernetes API server, it is useless against the API server even if
leaked, unlike the pod's ordinary ServiceAccount token.

A `TokenReview` of a bound token also returns the pod's binding claims (its
pod name and UID). The traffic-manager checks those claims against the pod
identity the agent presents on arrival, so a pod cannot register as a
traffic-agent for a workload it does not run, even though its own
ServiceAccount token would otherwise pass authentication.

## Authorization of intercepts

Creating an intercept requires the physical ability to receive the workload's
traffic, which already implies `create` access on `pods/portforward` in the
target namespace. The traffic-manager makes that requirement explicit:
`CreateIntercept` runs a Kubernetes `SubjectAccessReview` asking whether the
caller may `create` `pods/portforward` in the intercepted namespace.

The check runs in two steps:

1. A namespace-wide review (no specific pod name).
2. If that is denied, one review per current pod of the target workload.

The second step exists because a `SubjectAccessReview` with no resource name
only matches RBAC grants that are themselves unscoped; a grant restricted with
`resourceNames` never matches an unnamed review. Checking each pod by name
means `resourceNames`-scoped grants work too, as long as the workload's pods
already exist when the intercept is created.

## Modes

The traffic-manager's authentication posture is controlled by the Helm value
`security.authentication.mode`:

| Mode | Behavior |
|------|----------|
| `disabled` | No token validation at all. |
| `permissive` (default) | Tokens are validated and used for authorization checks and session binding, but no call is ever rejected for lacking or failing authentication. Decisions are logged for audit. |
| `enforcing` | Calls without a valid bearer token are rejected (`Unauthenticated`), and an unauthorized intercept is rejected (`PermissionDenied`). |

Set it at install or upgrade time:

```console
$ telepresence helm install --set security.authentication.mode=enforcing
```

or in a values file:

```yaml
security:
  authentication:
    mode: enforcing
```

> [!NOTE]
> The read-only `Version` handshake and health checks are always open,
> regardless of mode, so a client can always learn a manager's version and
> capabilities before authenticating.

Session ownership — once a session has an authenticated owner, only that
owner may drive it — is enforced in every mode, including `permissive`.
Sessions created without a token (calls from an older client or agent) have
no owner and remain governed by the mode.

If the `TokenReview` or `SubjectAccessReview` infrastructure itself is
unreachable — for example, the API server is down — the traffic-manager
reports `Unavailable` rather than rejecting the call as unauthenticated or
unauthorized, so an infrastructure outage is distinguishable from an actual
denial.

### Staged rollout

`permissive` is the default specifically so that upgrading the traffic-manager
never breaks an existing installation: administrators can watch the audit log
to see which callers would be rejected before opting in to `enforcing`.

Turn on `enforcing` only once your client and agent fleet is running this
release or later — an agent or client older than this release never sends a
token and is always rejected once the manager enforces authentication.
Clients and agents at this release or later always send a token when their
credentials can produce one, so they are unaffected by the switch.

## Requirements under enforcement

With `security.authentication.mode: enforcing`:

- Every caller must present a bearer token that a `TokenReview` accepts. See
  [Client-certificate-only kubeconfigs](#client-certificate-only-kubeconfigs)
  for the one case where a client cannot.
- Creating an intercept requires the caller's Kubernetes identity to have
  `create` access on `pods/portforward` in the target namespace — the same
  permission described in [RBAC](rbac.md). A grant scoped with `resourceNames`
  is honored as long as the workload's pods exist at the time the intercept is
  created (see [Authorization of intercepts](#authorization-of-intercepts)).
- Agents must be running this release or later, since only they present the
  audience-bound projected token that authentication requires.

## Caveats

### Client-certificate-only kubeconfigs

A kubeconfig whose active context authenticates with a client certificate and
no token — common for bare-metal or `kubeadm`-provisioned clusters — cannot
produce a bearer token to present to the traffic-manager, and the
port-forwarded connection to the manager does not carry the client
certificate either. Against an `enforcing` manager, such a client fails with a
clear error rather than a string of rejected calls, pointing back to this
section: use a context with token or exec-plugin credentials, or set the
Helm value `security.authentication.mode` to `permissive`.

### Environment access implied by portforward

A caller authorized to intercept — that is, one holding `pods/portforward` —
still receives the intercepted container's environment over the intercept,
which can include environment variables whose values were resolved from a
Kubernetes Secret, even if that caller has no direct RBAC to read Secrets.
This is accepted because `pods/portforward` already implies deep access to
the pod; it is not a new privilege introduced by this authorization check,
but it is worth being aware of when granting `pods/portforward` broadly.

### Impersonation

A kubeconfig using impersonation is authorized as the impersonating
identity's RBAC, not the impersonated identity's.

### This does not replace network controls

Authorization decisions are only as good as the identity behind them.
Restricting *who can reach* the traffic-manager in the first place —
with NetworkPolicy, or by running a namespaced install — remains recommended
defense in depth; see [RBAC](rbac.md).

## Traffic-manager RBAC

The traffic-manager needs `create` access on `tokenreviews`
(`authentication.k8s.io`) and `subjectaccessreviews` (`authorization.k8s.io`)
to authenticate and authorize callers. The Helm chart grants both
automatically in every install mode. Operators who manage the
traffic-manager's RBAC by hand (see [RBAC](rbac.md)) need to add these rules
to keep authentication and authorization working:

```yaml
- apiGroups: ["authentication.k8s.io"]
  resources: ["tokenreviews"]
  verbs: ["create"]
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create"]
```
