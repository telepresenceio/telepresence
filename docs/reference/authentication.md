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
- **Namespace content is authorized.** In `enforcing` mode, the
  traffic-manager shows a caller the workloads, traffic-agents, and agent
  configuration of a namespace only where the caller's Kubernetes identity
  holds a Telepresence grant, and removes traffic-agents only for a caller
  that holds one.

The net effect: a workload with mere network reachability to the
traffic-manager can no longer intercept arbitrary namespaces or act on other
callers' sessions. It still needs a Kubernetes identity that Kubernetes itself
would let touch the target namespace.

This is independent of, and does not replace, restricting network reachability
to the traffic-manager in the first place. NetworkPolicy and a namespaced
install (see [RBAC](rbac.md)) remain the recommended defense in depth.

## Trust model

Telepresence connects a developer's workstation to the cluster network,
and lets a program on that workstation take the place of a workload in
the cluster. That takes trust in the developers who are given access,
and no setting removes the need for it.

Authentication and authorization keep each developer inside the limits
that an administrator sets with Kubernetes RBAC: which namespaces a
developer can connect to, which workloads a developer can intercept or
ingest, and which of their volumes a developer can mount. They are not
designed for a cluster in which one of the following two is hostile:

- **Whoever can run containers in a namespace.** A traffic-agent is
  trusted with the sessions of the clients that use it, and so is
  everything else that runs in its pod. Someone who can deploy containers
  to a namespace, or change the pod specification of a workload there,
  is therefore trusted by every developer who connects to workloads in
  that namespace.
- **The traffic-manager.** A client sends the credential it
  authenticates with to the traffic-manager on every call, and takes
  network routes, DNS settings and configuration from it. Whoever
  controls the traffic-manager can act in the cluster as the users who
  connect to it. Connect only to clusters whose administrators you
  trust.

On the workstation, the root daemon serves one session at a time, and
only the user daemon that created the session can read, change or end
it (see [Who may call the daemons](../concepts/architecture.md#who-may-call-the-daemons)).
It is meant for a single user at a time.

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

In `enforcing` mode the traffic-manager accepts an agent only when all of
these hold:

- **The token carries the `traffic-manager` audience.** A token that
  passes review with only the API server's audience, such as the pod's
  ordinary ServiceAccount token, is rejected with `PermissionDenied` on
  every call an agent makes, not only when it registers.
- **The pod exists and matches the token.** A `TokenReview` of a bound
  token returns the pod's name and UID. The pod must exist with that UID,
  and the pod IP that the agent reports must be one of the pod's
  addresses.
- **The pod is a traffic-agent pod of the workload it claims.** The pod
  must have a `traffic-agent` container. The workload that its owner
  references lead to, checked by UID at each step, must have the kind and
  name that the agent reports.
- **A node-agent's pod belongs to its Job.** For an agent running in
  [node-agent mode](node-agent.md), the pod must be controlled by a Job
  (matched by UID) that the traffic-manager created for the reported
  workload and namespace.

A rejected agent gets `PermissionDenied`, and the traffic-manager logs the
reason. If the traffic-manager can't look up the pod or its owners because
the API server is unreachable, it reports `Unavailable`, and the agent
retries.

Only a traffic-agent can register an agent session. The traffic-manager
ignores traffic-agents that a client lists when it reconnects after a
traffic-manager restart. Each traffic-agent registers itself again.

## Authorization of intercepts

Creating an intercept requires the physical ability to receive the workload's
traffic, which already implies `create` access on `pods/portforward` in the
target namespace. The traffic-manager makes that requirement explicit: when
an intercept is created, it runs a Kubernetes `SubjectAccessReview` asking
whether the caller may `create` `pods/portforward` in the intercepted
namespace.

The check runs in two steps:

1. A namespace-wide review (no specific pod name).
2. If that is denied, one review per current pod of the target workload.

The second step exists because a `SubjectAccessReview` with no resource name
only matches RBAC grants that are themselves unscoped; a grant restricted with
`resourceNames` never matches an unnamed review. Checking each pod by name
means `resourceNames`-scoped grants work too, as long as the workload's pods
already exist when the intercept is created.

## Authorization of namespace content

A connected client asks the traffic-manager for data about namespaces: the
workload list behind `telepresence list`, the traffic-agents and their
pods, the agent configuration of a workload, and the address of
a service port. In `enforcing` mode the traffic-manager reviews the
caller's access before it answers. The namespace given to
`telepresence connect` is reviewed like any other.

The review has two levels:

1. **The whole namespace.** The caller may `create` or `get`
   `attachments.telepresence.io` in the namespace, or holds the legacy
   `pods/portforward` grant there. Everything in the namespace is shown.
2. **Individual workloads.** If the first review is denied, a workload and
   its traffic-agent pods are shown only when the caller holds the grant
   for that workload by name. This is what a Role scoped with
   `resourceNames` gives.

A caller without any grant in a namespace gets an empty list from it, not
an error. Two calls are refused with `PermissionDenied` instead: reading
the agent configuration of a workload the caller holds no grant for, and
resolving a named service port without a grant for the whole namespace.
With grants on individual workloads only, give service ports as numbers.

Removing traffic-agents is reviewed in the same way.
`telepresence uninstall <workload>` needs the grant for every workload it
names, and removes nothing when one of them is refused.
`telepresence uninstall --all-agents` needs the grant for the whole
namespace. With grants on individual workloads only, name the workloads.

A verdict is kept for the lifetime of the session. A changed grant takes
effect when the client reconnects.

### What every connected client can do

Some things come with the connect grant alone and need no grant in a
namespace:

- **Network access** to what the traffic-manager can reach, and name
  resolution for it, in every namespace. Use NetworkPolicy to restrict
  that; see [This does not replace network controls](#this-does-not-replace-network-controls).
- **The names of the managed namespaces.** The client uses them for name
  resolution. Nothing inside the namespaces is shown.
- **The log level of the traffic-manager.** `telepresence loglevel` can
  raise or lower it for a limited time.

## Modes

The traffic-manager's authentication posture is controlled by the Helm value
`security.authentication.mode`:

| Mode | Behavior |
|------|----------|
| `disabled` | No token validation at all. |
| `permissive` | Tokens are validated and used for authorization checks and session binding, but a call with a missing or invalid token is served as unauthenticated. Decisions are logged for audit. |
| `enforcing` (default) | Calls without a valid bearer token are rejected (`Unauthenticated`), and an unauthorized intercept is rejected (`PermissionDenied`). |

In every mode, a call whose token could not be checked because the
traffic-manager's limit on token reviews was reached is refused with
`ResourceExhausted`. A token the manager accepted earlier is checked under a
separate allowance, so a flood of unknown tokens does not lock it out.

The default is `enforcing`. Set another mode at install or upgrade time:

```console
$ telepresence helm install --set security.authentication.mode=permissive
```

or in a values file:

```yaml
security:
  authentication:
    mode: permissive
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

### Moving an existing installation to `enforcing`

An installation that never set `security.authentication.mode` becomes
`enforcing` on its next `telepresence helm upgrade` to 2.34.0 or later. An
installation that set the mode explicitly, for example to `permissive`, keeps
it. See [Upgrading](../install/manager.md#upgradingdowngrading-the-traffic-manager)
for how the command treats values.

An `enforcing` traffic-manager refuses:

- Clients older than 2.31.0. They send no token.
- Traffic-agents older than 2.31. They send no token.
- A user who lacks the grant a call needs. To connect, the user needs
  `create` on `connections.telepresence.io` in the traffic-manager's
  namespace, or `pods/portforward` there. To intercept, replace, or
  wiretap, the user needs `create` on `attachments.telepresence.io` for the
  workload, or `pods/portforward` in its namespace. To ingest, the user
  needs `get` on the attachment, or the same `pods/portforward`. With the
  default `security.authorization.requiredGrant: any`, either kind of grant
  is accepted. See [Client RBAC](../howtos/client-rbac.md).
- A kubeconfig that authenticates with a client certificate, when the x509
  listener is disabled or the API server is behind an authenticating proxy
  that uses another CA. See
  [Client-certificate-only kubeconfigs](#client-certificate-only-kubeconfigs).

To prepare, upgrade the clients to 2.31.0 or later, check that each user holds
one of the grants above, and check that each kubeconfig can produce a bearer
token or a verifiable client certificate. Clients and agents at 2.31 or later
send a token whenever their credentials can produce one.

To stay on `permissive` for a while, set the mode when upgrading. An upgrade
with value flags uses only the values given on that command line, so repeat
the values that you set earlier, for example from your values file:

```console
$ telepresence helm upgrade -f my-values.yaml --set security.authentication.mode=permissive
```

Later upgrades without value flags keep the mode.

In `permissive` mode, the traffic-manager logs its authentication and
authorization decisions for audit.

## Traffic-agent ports

A traffic-agent listens on more than the gRPC port that carries intercepted
traffic: it also runs an FTP and an SFTP server for remote file mounts, and
its gRPC surface accepts tunnel and dial-watcher calls from the client's root
daemon. All three ports are reachable by anything with network access to the
agent's pod — the same "any pod in the cluster" reachability described
above. The traffic-manager and agent protect them the same way the manager's
own gRPC surface is protected: every caller presents a session credential,
and the agent verifies it. In `enforcing` mode the agent also asks the
traffic-manager what the credential's session may do (see
[Access levels](#access-levels)).

### The session credential

The credential is rooted in the traffic-manager's in-memory QUIC CA (the same
CA that signs the QUIC tunnel's certificates), regenerated on every manager
restart, which revokes every outstanding credential at once. It takes two
forms, both minted by the manager in a single request and sharing the
same 24-hour expiry:

- A **client certificate**, CommonName set to the session ID, used wherever
  the transport is already TLS — the QUIC tunnel's mutual-TLS handshake.
- A **signed bearer token**, verified offline against the CA certificate
  without a round trip to the manager, used wherever the transport can't
  carry a certificate: the FTP password, and gRPC metadata on tunnel and
  dial-watcher calls (the QUIC and port-forwarded paths to one agent share a
  single gRPC connection, so the same metadata covers both).

The manager mints both forms only for the calling session's own owner, the
same ownership check its other session-scoped calls use.

Because a restart replaces the CA, an agent fetches the CA certificate when
its manager session starts and again each time it re-registers with a
restarted manager, retrying a failed fetch with backoff until it succeeds or
that manager session ends. Until then, the agent keeps verifying tokens
against the previous manager's key.

### Enforcement mirrors the authentication mode

An agent has no way to tell an old client from an attacker, so it can't
require a credential unconditionally without breaking every client that
predates this feature. Enforcement therefore mirrors the manager's
`security.authentication.mode`, which is propagated to each agent on every
reconnect:

| Mode | Behavior |
|------|----------|
| `disabled` / `permissive` | A credential is verified when presented and a failure is logged; a connection or call without one is still served. |
| `enforcing` | No valid session credential, no file access and no tunnel or dial-watcher call. A valid credential is not enough: the session must also hold the [access level](#access-levels) that the call needs. |

### Access levels

In `enforcing` mode a traffic-agent asks the traffic-manager what a client
session may do through it, and the traffic-manager answers with one of three
levels. Each level includes the ones below it.

| Level | What the session may do through the agent | What the client needs |
|-------|-------------------------------------------|-----------------------|
| none | Nothing. Tunnel, dial-watcher, FTP and SFTP requests are refused with `PermissionDenied`. | No access to the agent's namespace or workload, a namespace that the traffic-manager doesn't manage, or a session that no longer exists. |
| network | Tunnels to anything except the agent's own file servers and loopback, and the dial watcher. | The ability to read the agent's namespace or the agent's workload, as described in [Authorization of namespace content](#authorization-of-namespace-content). |
| files | Everything in network, plus the FTP and SFTP servers, and tunnels to loopback or to the pod's own addresses on the file ports. | The network level, and the `create` or `get` grant on `attachments.telepresence.io` for the workload (by name or for the whole namespace), as described for [individual workloads](#authorization-of-namespace-content). |

The traffic-manager reviews the grants for the client's own Kubernetes
identity, never the agent's. An intercepting or ingesting client holds `create` or `get` on the
workload's attachment, so it gets the files level. A client that can read
the namespace but holds neither grant on that workload reaches the agent's
network side only.

The agent remembers each answer for five minutes per client session and
credential, except a `none` answer, which it remembers for fifteen
seconds. Access is checked when a connection or a call starts, so a
change in a client's grants does not take effect at once:

- After an attachment grant is revoked, new connections to the file
  servers are refused within about seven minutes. Connections that are
  already open stay open.
- Read access to a namespace is evaluated once per session, so the
  network level lasts until the session ends.
- A grant that is added takes effect within fifteen seconds for a
  session that was refused.

A failed call to the traffic-manager counts as `none` for that request
and is not remembered.

### FTP

The FTP password is the signed token. The agent's password validator checks
its signature and expiry against the CA; disabled/permissive modes accept an
invalid or absent password too (logged), enforcing mode does not.

### Agent gRPC: tunnel and dial-watcher calls

The client attaches the token as gRPC metadata on every tunnel and
dial-watcher call. The agent verifies it and compares the session it names
against the session the call declares: a mismatch is refused in every mode,
not only enforcing — a caller proving ownership of a different session is
never ambiguous, only wrong. An absent or unverifiable token is refused only
in enforcing mode. The dial watcher adds one more rule on top: a verified
caller may always displace whatever watcher is currently registered for its
session (a reconnect), but an unverified caller may only register when none
is registered yet — it can never take over a live one.

### SFTP

SFTP carries no credential of its own. Two things protect it instead:

- **The tunnel is the only way in, in every mode.** The agent announces an
  SFTP port, and a client addresses a mount by the pod's address and that
  port, but the port serves no connection. When a tunnel stream targets
  it, the agent connects the stream to the SFTP server inside its own
  process. The tunnel call is what authenticates the client: in enforcing
  mode the agent verifies the session and requires the files
  [access level](#access-levels) before it connects the stream. Nothing
  else reaches the server, the other containers of the agent's pod
  included.
- **Confinement.** The server is confined to the agent's exports tree; it
  cannot open a path outside it regardless of what the client requests.

SFTP is deliberately not wrapped in its own TLS layer: the QUIC tunnel is
already TLS 1.3 end-to-end, and the port-forward path is already TLS on its
client-to-manager leg, so an SFTP-level TLS wrap would double-encrypt the
bulkiest data path telepresence carries for no real confidentiality gain.

### Version skew

An old client presents no credential at all and is served normally by
disabled/permissive agents; only enforcing mode requires it to be upgraded
first. A new client talking to an old agent gets no benefit from its
credential — the old agent has no verification code path, so its ports stay
exactly as open as before — but nothing breaks. Talking through a manager
that predates session credentials, a new agent never receives credential
material to verify against. The manager tells it so, and the agent then
accepts every connection and call unauthenticated. An agent that has not yet
heard from its manager refuses every FTP login, SFTP connection, `WatchDial`
and `Tunnel` call until it has: the gRPC calls report `Unavailable`, which a
client can retry.

The [access levels](#access-levels) add two more cases:

- **An agent of version 2.34 or later with an older traffic-manager.** The
  agent requires a traffic-manager of version 2.34 or later when
  authentication is enforced. The traffic-manager answers the access call
  with `Unimplemented`, and the agent refuses every session, logging an
  error, until the traffic-manager has been upgraded. Upgrade the
  traffic-manager before the agents are restarted.
- **An agent older than 2.34 with a current traffic-manager.** The agent
  never asks for access levels and checks the session credential only. The
  traffic-manager does not replace a pod when only the agent image changes,
  so such an agent keeps this behaviour until its pod is restarted (for
  example with `kubectl rollout restart`), which injects the current agent
  image.

The checks on the agent's identity depend only on the traffic-manager, so
they apply to agents of every version that sends the `traffic-manager`
audience token.

## Requirements under enforcement

With `security.authentication.mode: enforcing`:

- Every caller must present one of:
  - a bearer token that a `TokenReview` accepts, or
  - a client certificate that the manager can verify against the cluster's
    client CA. This path is enabled by default under `enforcing` mode and can
    be turned off with `security.authentication.x509.enabled`; see
    [Client-certificate-only kubeconfigs](#client-certificate-only-kubeconfigs)
    for how it works and what it requires.
- Creating an intercept requires the caller's Kubernetes identity to have
  `create` access on `pods/portforward` in the target namespace — the same
  permission described in [RBAC](rbac.md). A grant scoped with `resourceNames`
  is honored as long as the workload's pods exist at the time the intercept is
  created (see [Authorization of intercepts](#authorization-of-intercepts)).
- Agents must be running version 2.31 or later, since only they present the
  audience-bound projected token that authentication requires. Agents older
  than 2.34 don't ask for [access levels](#access-levels); see
  [Version skew](#version-skew).

## Caveats

### Client-certificate-only kubeconfigs

A kubeconfig whose active context authenticates with a client certificate and
no token — common for bare-metal or `kubeadm`-provisioned clusters — cannot
produce a bearer token to present to the traffic-manager, and the
port-forwarded connection used for the gRPC API does not carry the client
certificate either.

x509 client-certificate authentication closes this gap, and is active by
default whenever `security.authentication.mode` is `enforcing`: the manager
opens a second, auth-only TLS listener on a dedicated container port,
reachable through the same `pods/portforward` grant the client already used
to reach the gRPC port — no additional client RBAC is needed. A cert-only
client performs a one-shot TLS handshake against that port, presenting the
same client certificate its kubeconfig would otherwise send to the API
server. The manager verifies the certificate chain against the cluster's
client CA — published in the `extension-apiserver-authentication` ConfigMap
in `kube-system`, the same mechanism aggregated API servers use — and derives
the caller's identity from the certificate exactly as the API server would:
username, groups (including `system:authenticated`), UID, and credential
identifier. On success the manager issues a short-lived bearer token, scoped
to the presented certificate, that the client presents on the normal gRPC
channel, refreshing it with a new handshake as needed. A token never
outlives the certificate chain's validity, and every outstanding token is
invalidated when the manager restarts or the cluster's client CA bundle
changes. The auth listener is reachable on the manager's pod IP inside the
cluster, so the network-level restrictions described in
[This does not replace network controls](#this-does-not-replace-network-controls)
apply to it as well.

The identity conversion follows the Kubernetes library semantics compiled
into the traffic-manager (currently those of Kubernetes 1.36): notably, the
UID is parsed from the certificate's x509 UID attribute, which the API
server itself only does on Kubernetes 1.33 or later and only when its
`AllowParsingUserUIDFromCertAuth` feature gate is enabled. On clusters that
diverge from those defaults, the manager may attribute a UID (or extra
attributes) that the API server would not; standard RBAC keys on username
and groups and is unaffected, but custom webhook authorizers that inspect
the UID may see a difference.

Set the Helm value `security.authentication.x509.enabled` to `false` to opt
out of this under `enforcing` mode. The value has no effect under any other
mode: x509 client-certificate authentication is only ever active when `mode`
is `enforcing`.

This requires the traffic-manager's ServiceAccount to read the
`extension-apiserver-authentication` ConfigMap, so whenever x509 auth is
active — the default under `enforcing` — the chart also creates a
RoleBinding in `kube-system` to the stock
`extension-apiserver-authentication-reader` Role (see
[Traffic-manager RBAC](rbac.md#traffic-manager-permissions)), provided
`managerRbac.create` is `true`. That RoleBinding is the only touch this
feature makes outside the manager's own namespace.

x509 authentication has no revocation: a certificate is valid until it
expires, and the manager has no way to learn that a certificate was revoked
early. Tokens remain the primary mechanism; x509 composes as an additional
one for the clients that cannot produce a token at all. It also does not
help every cert-only cluster: if the cluster is fronted by an authenticating
proxy that signs user certificates with a CA other than the one published in
`client-ca-file`, the manager cannot verify those certificates, and such
clients still need a context with token or exec-plugin credentials, or the
manager must run with `security.authentication.mode` set to `permissive`.

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
