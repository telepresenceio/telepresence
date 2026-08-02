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

## Traffic-agent ports

A traffic-agent listens on more than the gRPC port that carries intercepted
traffic: it also runs an FTP and an SFTP server for remote file mounts, and
its gRPC surface accepts tunnel and dial-watcher calls from the client's root
daemon. All three ports are reachable by anything with network access to the
agent's pod — the same "any pod in the cluster" reachability described
above — and none of them used to check who was calling: a caller that could
merely reach the pod could drive another client's tunnel, register as its
dial watcher, or read and write its mounted files. The traffic-manager and
agent close that gap the same way the manager's own gRPC surface does: every
caller presents a session credential, and the agent verifies it.

### The session credential

The credential is rooted in the traffic-manager's in-memory QUIC CA (the same
CA that signs the QUIC tunnel's certificates), regenerated on every manager
restart, which revokes every outstanding credential at once. It takes two
forms, both minted by a single `GetSessionCredential` call and sharing the
same 24-hour expiry:

- A **client certificate**, CommonName set to the session ID, used wherever
  the transport is already TLS — the QUIC tunnel's mutual-TLS handshake.
- A **signed bearer token**, verified offline against the CA certificate
  without a round trip to the manager, used wherever the transport can't
  carry a certificate: the FTP password, and gRPC metadata on tunnel and
  dial-watcher calls (the QUIC and port-forwarded paths to one agent share a
  single gRPC connection, so the same metadata covers both).

`GetSessionCredential` mints both forms only for the calling session's own
owner, the same ownership check the manager's other session-scoped RPCs use.

### Enforcement mirrors the authentication mode

An agent has no way to tell an old client from an attacker, so it can't
require a credential unconditionally without breaking every client that
predates this feature. Enforcement therefore mirrors the manager's
`security.authentication.mode`, which is propagated to each agent on every
reconnect:

| Mode | Behavior |
|------|----------|
| `disabled` / `permissive` | A credential is verified when presented and a failure is logged; a connection or call without one is still served. |
| `enforcing` | No valid session credential, no file access and no tunnel or dial-watcher call. |

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

- **Confinement.** The server is confined to the agent's exports tree; it
  cannot open a path outside it regardless of what the client requests.
- **A source gate, active in enforcing mode only.** Every legitimate
  consumer — sshfs, the IPv6 mount path, and the `--local-mount-port` bridge
  used by docker-volume-telemount — reaches the SFTP port through the
  telepresence tunnel, and the agent dials that connection itself, so it
  always arrives from the pod's own address or loopback. In enforcing mode
  the agent refuses any SFTP connection arriving from anywhere else;
  disabled/permissive modes serve every connection as before (logged).
  Session authentication is therefore transitive: the client proves its
  session at the tunnel, the agent dials its own SFTP listener from that
  tunnel, and the listener trusts only connections that arrived that way.

SFTP is deliberately not wrapped in its own TLS layer: the QUIC tunnel is
already TLS 1.3 end-to-end, and the port-forward path is already TLS on its
client-to-manager leg, so an SFTP-level TLS wrap would double-encrypt the
bulkiest data path telepresence carries for no real confidentiality gain.

Two weaknesses remain, accepted as improvements over the previous, fully
open listener rather than as complete mitigations:

- An agent injected into a `hostNetwork` workload shares its pod address with
  every other workload on that node, so the source gate degrades to
  same-node granularity there.
- A pod CIDR that is routable but never proxied through the tunnel can still
  reach the SFTP port directly; only enforcing mode cuts it off.

### Version skew

An old client presents no credential at all and is served normally by
disabled/permissive agents; only enforcing mode requires it to be upgraded
first. A new client talking to an old agent gets no benefit from its
credential — the old agent has no verification code path, so its ports stay
exactly as open as before — but nothing breaks. Talking through a manager
that predates `GetSessionCredential`, a new agent never receives credential
material to verify against, so it falls back to accepting every connection
and call unauthenticated, the same as before this feature existed. Every
combination keeps working outside enforcing mode.

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
- Agents must be running this release or later, since only they present the
  audience-bound projected token that authentication requires.

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
clients still need a context with token or exec-plugin credentials, or must
run with `security.authentication.mode` set to `permissive`.

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
