# x509 client authentication (scoped for 2.31.1)

## Motivation

The 2.31.0 authentication has a known gap: a kubeconfig whose credentials
are a client certificate yields no bearer token, and the port-forwarded h2c
connection does not carry the certificate, so the traffic-manager cannot
identify such callers at all. They run unidentified in `permissive` mode —
the session-ownership protections cannot distinguish them from each other —
and are rejected outright in `enforcing` mode (documented caveat in
`docs/reference/authentication.md`). The caller has proven
`pods/portforward create` in the manager namespace to the API server just
to reach the manager, but that establishes authorization to connect, not
identity.

For cert-only kubeconfigs — predominantly kubeadm, bare-metal, and kind
clusters — `enforcing` mode is therefore unusable. Fixing that qualifies as
a bugfix: this plan is scoped so the whole change can ship in a 2.31.1
patch release. It is a carve-out of phase 1c of
`docs/plans/client-rbac-minimization/plan.md`; the x509 verifier built here
is later reused by that plan's phase 3 external endpoint, where the
certificate arrives on the main TLS handshake instead of a separate
exchange.

## Design

The port-forward delivers a raw byte stream, so TLS can run through it —
but the stream already rides inside the client-to-API-server TLS session,
and wrapping the whole gRPC channel in TLS would double-encrypt every
tunneled byte to obtain one handshake's worth of identity proof. Instead,
TLS is confined to a one-shot authentication exchange and the data channel
stays h2c:

1. The manager exposes an auth-only TLS listener
   (`ClientAuth: RequireAnyClientCert`) on a dedicated container port,
   advertised through the version/capability handshake.
2. A cert-only client performs a TLS handshake against that port,
   presenting its kubeconfig client certificate, over the same shared
   per-pod port-forward connection the gRPC channel uses. No new client
   RBAC: `pods/portforward create` is not port-scoped.
3. The manager verifies the presented chain against the cluster client CA
   published in the `extension-apiserver-authentication` ConfigMap in
   `kube-system` — the mechanism aggregated API servers use — and derives
   username/groups from CN/O with standard Kubernetes x509 semantics.
4. The manager returns a short-lived bearer credential scoped to the
   presented certificate: an opaque random token held in manager memory,
   mapped to the derived `Principal`, one active token per certificate,
   bounded by the certificate chain's validity, and invalidated when the
   client CA bundle changes. The manager is already a stateful singleton
   with in-memory session and QUIC-CA state; a restart invalidating
   outstanding tokens matches existing session semantics, and the client
   re-handshakes on every reconnect.
5. The client presents that token as the normal per-RPC `authorization`
   metadata on the existing h2c channel. The double encryption is confined
   to a few-kilobyte handshake; the data path carries no added per-byte
   cost.

The manager's server certificate for the handshake is ephemeral
self-signed, and the client skips verification: server authenticity is
already provided by the API server routing the port-forward to the
selected manager pod. The handshake exists only to carry the client's
certificate.

Alternative considered and rejected: a signed challenge in gRPC metadata
(certificate chain plus a signature over a manager-issued nonce), avoiding
TLS entirely. That is a bespoke proof-of-possession protocol; the TLS
handshake provides the same proof with standard, reviewed machinery.

## Work items

### Manager

- x509 verifier: load and cache the `client-ca-file` from the
  `extension-apiserver-authentication` ConfigMap, verify presented chains,
  map CN/O to a `Principal` (`cmd/traffic/cmd/manager/auth/`).
- Auth listener: TLS listener on a new container port serving the
  handshake-and-mint exchange; opaque token store with expiry.
- Interceptor: a second validator for manager-issued tokens alongside the
  TokenReview path in `auth/interceptor.go`; the resulting `Principal`
  feeds the same session binding and SAR authorization as
  token-authenticated callers.

### Client

- The manager connection is pinned to one concrete manager pod: the pod is
  resolved once, the gRPC channel dials it by name and UID (as agent
  connections already do), and the connection never migrates to another
  pod. When the pod goes away or stops accepting the client's credentials,
  the connection's RPCs fail and the session's existing reconnect logic
  builds a new connection against a fresh resolution.
- `newManagerTokenSource` (`pkg/client/k8s/manager_token.go`) already
  returns nil exactly when the kubeconfig is cert-only — the hook point
  exists. Add a token source that performs the handshake against the pinned
  pod's advertised auth port and caches the returned token until it
  expires, plugged into the existing `PerRPCCredentials` machinery. Each
  reconnect constructs a fresh source, so a manager that forgot its tokens
  (restart, CA change) is healed by the reconnect's fresh handshake.

### Proto

- One additive field in the version/capability handshake advertising the
  auth port. Wire-compatible in both directions: old client + new manager
  and new client + old manager behave exactly as today.

### Chart

- RoleBinding in `kube-system` to the stock
  `extension-apiserver-authentication-reader` Role for the manager's
  ServiceAccount (the ConfigMap is not readable by all authenticated
  identities by default; this is the same binding every aggregated API
  server ships).
- Values toggle `security.authentication.x509.enabled` (plain boolean,
  default `true`), gating both the listener and the kube-system RoleBinding.
  Decided: x509 auth is active only when `security.authentication.mode` is
  `enforcing` AND this value is not `false` — it has no effect under
  `disabled`/`permissive`, so enabling it there is not possible. This means
  flipping on enforcement authenticates cert-only clients instead of
  locking them out, without a separate admin action; `enabled: false` opts
  out under `enforcing`. The changelog entry is loud about the kube-system
  touch.
- New container port on the manager Deployment.

### Docs

- Update the client-certificate caveat in
  `docs/reference/authentication.md`: `enforcing` mode can now require
  identity from every client.
- `docs/reference/rbac.md` note on the manager's kube-system RoleBinding.

## Testing

kind clusters use cert-based kubeconfigs natively, so the integration test
exercises the real path with no scaffolding: connect against an
`enforcing` manager with a cert-only kubeconfig, assert identified
sessions, intercept authorization, and rejection when the certificate is
not signed by the cluster client CA.

## Release mechanics

Land on `release/v2` first, then cherry-pick onto a `thallgren/v2.31.1`
branch cut from the `v2.31.0` tag, per the usual release-branch workflow.
The rest of the client-RBAC-minimization phase 1 (namespaces/logs RPCs)
stays out of the patch and rides 2.32.0.

## Risks and open questions

- Security-critical code on a patch-release fuse: the verifier and token
  validator need unhurried review despite the schedule. No bespoke crypto
  is involved (standard TLS client auth, standard x509 chain
  verification).
- Token lifetime, and whether refresh happens over the h2c channel or by
  repeating the handshake.
- Clusters fronted by an authenticating proxy may sign user certs with a
  different CA than `client-ca-file`; x509 also provides no revocation.
  Tokens remain the primary mechanism; x509 composes as an additional
  authenticator.
