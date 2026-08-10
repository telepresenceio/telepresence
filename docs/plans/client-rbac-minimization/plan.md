# Minimizing the client RBAC footprint

## Motivation

A telepresence client needs a set of Kubernetes RBAC grants today that exist
for purely mechanical reasons: discovering the traffic-manager, establishing
the port-forward, watching namespaces, and fetching pod logs. None of these
grants exists because the client must be the one doing the work. The manager
authenticates every caller via TokenReview or x509 client certificate and
authorizes intercepts with SubjectAccessReview against the caller's verified
identity, and the agent's tunnels and file-sharing ports require
session-scoped credentials — so the manager can act as the client's deputy,
and every surface this plan moves traffic onto is an authenticated one.

This plan moves that functionality to the traffic-manager in four phases,
each independently shippable and backward compatible. The end state is a
client that needs no Kubernetes API access at all when an admin hands it an
external endpoint, and a single name-scoped `pods/portforward` grant
otherwise. Authorization comes first: phases 2–4 all move enforcement from
the API server to the manager, so the manager's authorization machinery must
be sound before any mechanics move.

## Current client RBAC and why each grant exists

Mandatory connect role (`charts/telepresence-oss/templates/clientRbac/connect.yaml`,
in the manager namespace):

| Grant                              | Mechanical reason                                                                 |
|------------------------------------|-----------------------------------------------------------------------------------|
| `services` get (`traffic-manager`) | `ConnectToManager` resolves the service to a pod (`pkg/client/portforward/resolve.go`) |
| `pods` get, list                   | Label-selector pod list/watch to pick the port-forward target                     |
| `pods/portforward` create          | The actual tunnel to the manager                                                  |

Per-namespace role (`clientRbac/cluster-scope.yaml` / `namespace-scope.yaml`):

| Grant                              | Mechanical reason                                                                 |
|------------------------------------|-----------------------------------------------------------------------------------|
| `pods` get, list                   | gather-logs agent discovery; namespace accessibility probe (`canAccessNS`)        |
| `pods/log` get                     | gather-logs fetches logs client-side (`pkg/client/userd/trafficmgr/gather_logs.go`) |
| `pods/portforward` create          | Direct client-to-agent connections (perf); also the intercept authz check         |
| `namespaces` get, list, watch      | Client-side namespace watcher (`pkg/client/k8s/cluster.go`), cluster-scope only   |

Only the last column's *mechanics* need the grants. The intercept
authorization check (`authorizeIntercept` in
`cmd/traffic/cmd/manager/service.go`) needs the client to *hold*
`pods/portforward` in the target namespace, not to exercise it — holding and
exercising become separable once the mechanics move server-side. Separating
them is what turns RBAC from a self-enforcing mechanism into declarative
policy, which is a design question in its own right; see
[Authorization](#authorization-policy-without-mechanism).

The manager already has everything it needs on its side: `pods/log` get in
both install modes, an RBAC-aware namespace watcher with a static fallback
(`cmd/traffic/cmd/manager/namespaces/watcher.go`), `tokenreviews` and
`subjectaccessreviews` create, a pod informer, and authoritative knowledge
of agent pods. For cert-only kubeconfigs it runs an auth-only TLS listener
over the same port-forward that verifies the kubeconfig's client
certificate against the cluster client CA and mints a short-lived bearer
token the client presents as ordinary per-RPC metadata
(`cmd/traffic/cmd/manager/auth/clientca.go`,
`pkg/client/k8s/x509_token.go`); phase 4 reuses that verifier — though not
that listener mechanism — for direct mTLS on the external listener.

## Phase 1: Authorization foundation

Goal: make the manager's authorization sound and complete before any
mechanics move onto it. This phase changes no client RBAC by itself; it is
the prerequisite for every later phase.

### 1a. Authorize before the first side effect

`authorizeIntercept` has a single call site, inside `CreateIntercept`
(`cmd/traffic/cmd/manager/service.go`). But substantial work happens
earlier: `PrepareIntercept` resolves and prepares the workload
configuration (and may create the namespace's initial config, or provision
the node-agent Job in node-agent mode), and `EnsureAgent` provisions agents
independently. An authenticated but unauthorized caller can therefore
trigger manager-side Kubernetes mutations before eventually being refused.

Centralize the review in a helper and call it from every entry point that
precedes a side effect:

- `PrepareIntercept` — it already has the workload name and namespace the
  review needs, before anything mutates.
- `EnsureAgent`.
- `CreateIntercept` — kept, defensively.
- The restore path (1b below).

### 1b. Reviews on the restore path

`ReconnectClient` (`service.go`) restores a client into a fresh manager
process after checking only session ownership and that the namespaces are
still managed; it then calls `RestoreClient`, `RestoreAgents`, and
`RestoreIntercepts` with no connect review and no per-intercept review. The
restored intercept list is client-provided and must not be trusted without
reauthorization.

- `ArriveAsClient`: run the connect review before `AddClient`.
- `ReconnectClient`, when the session is unknown (state was lost): run the
  connect review before `RestoreClient`, then review every restored
  intercept before `RestoreIntercepts`.
- `ReconnectClient`, when the session is live and owned: return after the
  ownership check, consistent with the no-periodic-review policy below.

Reauthorization alone does not make the payload trustworthy. Today
`RestoreIntercepts` stores each incoming `InterceptInfo` directly
(`state.go`), manager-owned fields included — ID, `ClientSession`,
disposition, pod identity, environment, mounts, ports — while the normal
creation path validates the workload, checks policy and conflicts,
constructs the ID, and waits for agent review. A SAR only proves the
caller may attach to a workload, not that an arbitrary serialized
intercept is valid, so an authorized caller could bypass the creation
path and plant manager state. Restoration therefore treats the payload as
desired intercept *specifications*:

- Consume only the safe subset (principally `Spec`). Reconstruct the
  intercept ID from the reconnecting session and intercept name, and
  overwrite `ClientSession` — never accept either from the payload.
- Ignore or reconstruct disposition, pod identity, environment, mounts,
  agent-populated ports, messages, and child intercepts.
- Re-run static validation, manager policy (global-intercept and
  node-agent restrictions), and conflict checks.
- Restore accepted intercepts as `WAITING`, letting reconnecting agents
  repopulate runtime state and review them again.

This stays wire-compatible: old clients keep sending full
`InterceptInfo`; the new manager reads the safe subset.

Partial restoration has defined semantics. The client treats any
`ReconnectClient` error as failure of the whole reconnect, so one revoked
grant among five intercepts must not wedge the client into resending all
five forever:

- Connect review denied: reject the entire reconnect.
- Connect review unavailable (infrastructure): fail the reconnect for
  retry.
- One intercept denied: omit it, restore the session and the authorized
  intercepts, and let the next intercept snapshot remove the denied one
  from client state.
- An intercept review unavailable: fail the restoration for retry —
  never silently convert infrastructure failure into denial.

This is what makes the decided revocation story true: reviews run at
session and intercept creation only — no periodic re-review (decided).
Revoking a grant does not tear down what is already running; when a
revocation is time critical, the admin restarts the manager, clients
re-establish their sessions and intercepts automatically, and the restore
path re-runs the reviews, so the revoked caller is the one that does not
come back. Against the pre-phase-1 code that mechanism does not exist —
restoration performs no reviews — which is why it lands here and not later.

### 1c. The required-grant model

Implement `security.authorization.requiredGrant` (`portforward` |
`telepresence` | `any`) as described in
[Authorization](#authorization-policy-without-mechanism), wired through the
connect, prepare/ensure, create, and restore reviews alike, with the chart
rendering client Roles to match. The grant is decided in exactly one place
(decided): `auth.Authorizer.Authorize` consumes a per-operation
`auth.Review` (attributes, pod grounding, wording) built by constructors,
and a single service-level wrapper applies the authentication mode — the
nil-principal policy and the not-enforced warning have one home each,
and per-session caching of a permissive verdict warns once per session
rather than per call. One rendering invariant (decided): the
connect Role always carries at least one grant that satisfies the
configured required grant. The discovery and port-forward rules are
transport, not policy, and render whenever the port-forward path is in
use — removing them when the required grant is `telepresence` would leave
a client with no path to the manager at all. With an external endpoint
published (phase 4) clients never port-forward, so the mechanical rule is
dropped — except when the required grant is `portforward`, where
possession of the grant is itself the connect policy and the named rule
renders even though nothing exercises it. The golden chart tests assert
the required-grant × legacy-toggle cross-product, including the phase-3
minimal rule and the external-endpoint renderings.

Verification for this phase needs negative cases per required-grant value:
an authenticated caller whose Role has been withheld must be refused a
*session*, and must be refused at `PrepareIntercept` — before any
mutation — not merely at `CreateIntercept`.

## Phase 2: Namespaces and diagnostics

Goal: remove the client's `namespaces` watch, its per-namespace `pods`
list, and its `pods/log` get.

### 2a. WatchNamespaces RPC

The manager already computes the namespace list and pushes it to clients
embedded in the `GetClientConfig` YAML (`MappedNamespaces`,
`cmd/traffic/cmd/manager/config/config.go`). Promote this to a typed
server-stream:

- New RPC in `rpc/manager/manager.proto`:
  `rpc WatchNamespaces(SessionInfo) returns (stream NamespaceList)`. The
  handler validates and binds the supplied client session before
  subscribing — it is not a bare `namespaces.Subscribe` wrapper.
- The stream carries the *managed* namespace set, served unfiltered — no
  per-caller filtering (decided). That set is what the DNS and dialer
  expose anyway once fully qualified names are used, so filtering would
  add SAR volume (namespaces × clients) without hiding anything.
- Unfiltered is only safe because the plan distinguishes three namespace
  concepts that today's client conflates (`refreshNamespaces` marks a
  namespace accessible via a `get pods` probe, and the agent-pod set is
  filtered further by `CanPortForward`):
  - *Managed* namespaces — the manager's scope; drives DNS search paths
    and generic namespace awareness.
  - *Authorized* namespaces — where this principal passes the selected
    attachment policy; gates workload listing, completion, and workload
    streams. Authorized lazily on first use per namespace and cached per
    session — no proactive namespaces × clients sweep — with the cache
    lifetime following the no-periodic-review policy.
  - *Direct-agent* namespaces — where the client can actually exercise
    `pods/portforward`; relevant only when that optimization is enabled.
  The distinction is load-bearing: today's `WatchWorkloads` returns
  workload metadata for any managed namespace a session names, so
  swapping the client's access-filtered map for the raw managed list
  without the authorized-set gate would widen information visibility
  beyond namespace names.
- Client: replace `Cluster.StartNamespaceWatcher` consumption in the
  connector with the RPC stream when the manager supports it (capability via
  `VersionInfo2`), keeping the existing watcher as fallback for old
  managers. Startup order is part of the work, not a footnote:
  `NewCluster` performs API discovery and may start the namespace watcher
  before the manager is dialed, so phase 2 defers that watcher until
  after capability detection (or bootstraps against the configured
  manager namespace without it) — a connect-sequence refactor, not just a
  consumer swap.

### 2b. Streaming log retrieval

`rpc/manager/manager.proto` carries a deprecated, stubbed `GetLogs` RPC.
Replace it with a server-streaming variant rather than reviving the unary
one (message-size and timeout limits are what killed it):

- New RPC: `rpc StreamLogs(StreamLogsRequest) returns (stream LogChunk)`,
  where the request selects manager and/or agents and follows the existing
  `GetLogsRequest` shape for container selection and pod YAML inclusion —
  plus the `SessionInfo` that deprecated shape lacks; the handler calls
  `ensureClientSession`.
- Authorization uses a Telepresence policy attribute, not `pods/log`:
  before streaming a pod's log, SAR the caller for `get` on
  `logs.telepresence.io` in that pod's namespace. Reviewing `pods/log`
  would contradict the phase's own goal — the reduced-RBAC client no
  longer holds it and would fail its own feature. Diagnostic
  authorization is independent of `security.authorization.requiredGrant`:
  the required grant governs the connect/attachment policy and never
  alters the log review,
  and the chart renders the diagnostic grants independently of it.
- Log streaming always requires an authenticated client session, in every
  authentication mode. The general permissive-mode posture (admit and skip
  review) must not apply here: the manager would be exercising its own
  `pods/log` permission on behalf of a caller who has proven nothing — a
  confused-deputy expansion on a data-exfiltration endpoint. Where the
  configured mode cannot produce a principal — `mode: disabled`, or
  permissive with a cert-only kubeconfig (the x509 token mint runs only
  in enforcing mode) — `StreamLogs` is simply unavailable, and such
  installations keep the client-side direct-API fallback and the
  `pods/log` RBAC that serves it; the manager does not grow a bespoke
  authentication path for this one endpoint.
- Pod YAML inclusion is a separate disclosure (metadata, environment
  references) with its own attribute in the same group: `get` on the
  `yaml` subresource of `logs` (decided), so the chart's diagnostic rule
  reads `resources: ["logs", "logs/yaml"]`. A caller granted `logs` but
  denied `logs/yaml` receives the logs with the manifests silently
  omitted (decided) — the denial is the grant's shape, not an error to
  report.
- The manager enumerates agent pods from its pod informer (or an API
  list), not from `AgentSession` state. The session projection misses
  exactly the diagnostically interesting cases: an injected agent that
  crashed, hung, or never completed `ArriveAsAgent`.
- The stream reports per-component errors (a pod denied or disappeared)
  instead of aborting the whole collection, and is bounded (decided):
  defaults of 64 KiB chunks, at most 4 concurrent pod readers per
  request, a 10 MiB per-pod byte cap, and a 5-minute request deadline,
  each a Helm value because `gather-logs` exists primarily for error
  reporting — an admin sizing an installation for support workflows must
  be able to raise the caps rather than fight them. The per-session
  concurrency is not a value: it is fixed at one active `StreamLogs` per
  client session (decided) — a client makes one call per collection, so
  anything more is amplification, not use.
- Client: `gather-logs` calls the RPC when the manager supports it, writes
  chunks to the existing cache-dir layout, and falls back to the current
  direct-API path against old managers.
- Remove the deprecated `GetLogs` stub in the same change.

RBAC effect after phase 2: the per-namespace role can shrink to
`pods/portforward` create (still wanted for direct agent dials and as the
intercept-authz policy bit when the required grant is `portforward`), plus the
`telepresence.io` grants. `pods` get/list, `pods/log` get, and the
cluster-scope `namespaces` get/list/watch become unnecessary for a
phase-2 client — but the chart keeps rendering them (the compatibility
matrix's "old paths intact" depends on it); their removal rides the same
values toggle and cadence as phase 3's discovery grants.

## Phase 3: Deterministic manager pod name

Goal: reduce the mandatory connect role to a single name-scoped rule.

The API server only supports port-forward against a pod name; discovery
exists solely because Deployment pod names are random.

- Convert the traffic-manager Deployment to a StatefulSet with the stable
  pod name `traffic-manager-0`. Enforce `replicaCount == 1`
  unconditionally — the manager is semantically a singleton (CA and
  session state are process-local; the chart already hard-fails
  `replicaCount > 1` when QUIC is enabled), and the known-name scheme
  assumes it. The conversion must carry over `deployment.yaml`'s
  scope-restart markers (`NOT_USED_NSS`, `NOT_USED_SCOPE`) that roll the
  pod when the managed-namespace configuration changes.
- Add a dedicated headless Service as the StatefulSet's governing
  `serviceName`. Keep the existing `traffic-manager` ClusterIP Service
  as-is for in-cluster clients and agents — converting it to headless
  would remove its virtual IP and load balancing.
- Availability changes and must be acknowledged, not glossed: the
  Deployment updates with `maxSurge: 1` / `maxUnavailable: 0`, and a
  one-replica StatefulSet cannot surge a second ordinal-zero pod, so every
  image update includes a window with no ready manager. Clients survive a
  manager restart — sessions re-establish with intercepts intact, through
  the phase-1 restore reviews — so the window is a transient blip rather
  than session loss, but the new restart behavior needs an explicit test,
  and the docs state it plainly.
- Helm upgrade path: a Deployment cannot be patched into a StatefulSet,
  so the chart ships a pre-upgrade hook (decided) — an idempotent Job
  that deletes the old Deployment if present and waits for its pods to be
  gone before the StatefulSet is applied, preventing the two from ever
  co-managing pods behind the same selector. The hook's ServiceAccount
  gets a Role scoped to deleting exactly the named Deployment. It stays
  in the chart until the release that also drops the legacy
  discovery-RBAC toggle. Rollback is uninstall-and-reinstall of the older
  chart, documented as such — `helm rollback` is blocked by the same kind
  flip in reverse. `telepresence helm upgrade` drives Helm underneath and
  inherits the migration for free; unattended paths (CI, GitOps) keep
  working, which is why a release-notes-only instruction was rejected.
- Connect flow (`pkg/client/k8s/connect.go`): attempt
  `pods/portforward` create on `traffic-manager-0` directly. On failure
  (older chart still running a Deployment, or pod not ready), fall back to
  today's resolution (`services` get, `pods` list/watch) when the client has
  the RBAC for it. Searching becomes a capability, not a requirement — and
  the known-name path carries its own bounded retry with backoff, because
  during an ordinary StatefulSet rollout the pod is briefly absent and a
  minimal-role client has no discovery RBAC to fall back to. Discovery is
  the compatibility fallback for an old Deployment, not the retry
  mechanism.
- Chart: reduce `clientRbac/connect.yaml` to

  ```yaml
  - apiGroups: [""]
    resources: ["pods/portforward"]
    resourceNames: ["traffic-manager-0"]
    verbs: ["create"]
  ```

  This rule is on solid ground: `pods/portforward` create is a subresource
  request, the exact case Kubernetes documents as name-scopeable (same
  pattern as locked-down `pods/exec`). The old discovery rules stay
  available behind a values toggle — `clientRbac.legacyAccess`, named for
  everything it controls: manager discovery, namespace watching, pod
  reads, and `pods/log` — with a decided cadence: the toggle defaults to
  legacy-on when phase 3 ships, flips to the minimal role two minor
  releases later (announced in the phase-3 release notes, giving admins a
  two-release runway to move clients forward or pin the toggle), and is
  removed — together with the migration hook — four minors after phase 3.
  Both events get release-note entries, and the connect error a legacy
  client sees against a minimal-role install names the toggle explicitly;
  that error message is the migration UX. One deprecation consequence
  must ride the same release notes: disabling the toggle also disables
  the direct diagnostic fallback, and `StreamLogs` refuses an
  unauthenticated caller in every mode. Once the toggle is removed,
  gathering manager and agent logs requires an authentication
  configuration capable of producing a principal; installations that
  remain in `disabled` mode, or permissive mode with certificate-only
  clients, no longer support cluster log gathering through the client.
- The pod name is a contract (decided): the chart already hard-codes the
  workload name to `traffic-manager` regardless of release name or
  `nameOverride` (`_helpers.tpl`, required since v2.20.3), so the
  StatefulSet pod is `traffic-manager-0` under every install
  configuration. Phase 3 promotes that from helper behavior to stated
  API — the client dials the name directly — and the golden
  chart-rendering matrix asserts it so the helper can never regress to
  honoring overrides.
- Docs: `docs/reference/rbac.md` gets the new minimal role; note that the
  discovery grants are only needed for pre-phase-3 clients.

RBAC effect after phase 3: mandatory client RBAC is one rule. The
per-namespace `pods/portforward` grant remains as an optional performance
feature (direct agent dials) and as the intercept-authz policy declaration
when the required grant is `portforward`.

## Phase 4: External control endpoint, zero mechanical RBAC

Goal: a client that never contacts the Kubernetes API server, given an
admin-supplied endpoint.

The control plane and the data plane are different protocols and stay
that way. The existing QUIC endpoint is tunnel data plane only: it accepts
QUIC streams carrying framed tunnel messages and feeds them into
`state.Tunnel` (`cmd/traffic/cmd/manager/manager.go`), while the Manager
gRPC service is registered on the TCP server. Exposing the control plane
externally is therefore new server surface, not a re-use of the
quic-forwarder plumbing.

### 4a. Server: a separate client-only TLS gRPC listener

- Add an external TLS/TCP gRPC listener for bootstrap and control-plane
  RPCs, with an explicit client-method surface: either an allowlist on the
  external server or a protobuf split of the Manager service into client,
  agent, and forwarder services. The full internal service must not be
  published: `WatchQuicBackends` is deliberately exempt from
  authentication (it feeds the RBAC-less quic-forwarder) and exposes
  backend addresses, pod UIDs, and ports — on a public listener that
  stream would be anonymously reachable. The forwarder feed and the agent
  RPCs stay on internal listeners.
- An allowlist of existing methods is not by itself a safe contract:
  several nominally client-facing RPCs carry internal, global, or weakly
  checked request forms. `WatchIntercepts` with an empty session ID
  watches all non-child intercepts; `WatchWorkloads` verifies only that
  an explicitly named namespace is managed; `WatchClusterInfo` checks
  that a session exists but not that the caller owns it; and
  `GetClientConfig` takes `Empty` — no session at all — and returns the
  managed-namespace list. The external surface is therefore a wrapper
  service, not the internal implementation re-registered: every
  post-session method performs `ensureClientSession`-equivalent
  ownership verification, legacy and internal request forms
  (empty-session watches, explicit-namespace shortcuts) are rejected,
  and the client configuration is delivered as part of successful
  session establishment rather than pre-session. The TCP tunnel fallback
  binds the declared tunnel session to the authenticated principal —
  possession of a session ID is not sufficient.
- Phase 4 records an explicit external RPC contract: for every exposed
  method, whether it is pre- or post-session, its authentication
  requirement, whether the connect authorization review applies, the
  ownership check it performs, the request forms permitted externally, and
  which peers may
  call it (clients only). Pre-session, the deliberately public surface
  is `Version` and health — nothing else.
- After session establishment over the external connection, the existing
  `GetQuicTunnelEndpoint` mechanism moves tunnel streams onto QUIC. The
  TLS gRPC connection is retained for control RPCs and doubles as the
  tunnel fallback where UDP is blocked (in scope, decided). The fallback
  covers manager tunnels (outbound cluster access) only: agent-bound
  streams — intercepted-traffic delivery and volume mounts — require a
  client-to-agent channel, which in external mode is the QUIC tunnel,
  since there is no Kubernetes port-forward to fall back to and no
  manager-mediated reverse dial (the same reason `agentPortForward:
  false` refuses attachments today). An external-only deployment
  publishes the QUIC endpoint alongside the control endpoint. The client
  must not silently create an attachment that cannot carry traffic:
  when the transport is external-only, no Kubernetes agent port-forward
  is available, and the manager reports that the QUIC tunnel endpoint
  is disabled or unpublished, intercept and ingest creation fail early
  with an actionable error, consistent with today's `agentPortForward:
  false` refusal. A published endpoint that is temporarily unreachable
  does not block attachment creation; connection failures are handled
  by the ordinary retry and reprobe machinery, and a mid-session QUIC
  outage remains a recoverable degraded state.
- Server trust: the trust material must survive manager restarts. The
  in-memory QUIC CA is ephemeral by design and cannot anchor an
  admin-distributed pin — every restart would invalidate it. The external
  listener terminates with a persisted certificate: cert-manager or
  equivalent via Helm values, or a chart-provisioned pinned CA/cert backed
  by persistent Secret material.
- Publishing an endpoint requires `security.authentication.mode:
  enforcing`, validated at chart render and again at manager startup — a
  hard failure, not a documentation recommendation. In permissive mode an
  unauthenticated caller is admitted and its review skipped, which is
  tolerable today only because reaching the manager takes
  `pods/portforward` and the API server has vouched for the caller; an
  external endpoint removes that precondition, leaving permissive with no
  access control whatsoever.
- Admission controls in front of TokenReview: the external listener is a
  TokenReview amplification surface. Each previously unseen invalid token
  causes up to two TokenReview calls — first with the manager audience,
  then the deliberate no-audience fallback that serves tokens minted for
  the API server only — and the token-keyed negative cache is useless
  against unique random tokens. The fallback stays (it exists for
  legitimate client tokens); the mitigation is bounding the path before
  either review: a global cap on concurrent authentication attempts, an
  authentication QPS/burst limiter, a maximum token/metadata length,
  per-connection RPC and stream limits, TLS-handshake and
  unauthenticated-idle deadlines, and metrics separating cache hits,
  first reviews, audience-fallback reviews, rate-limited requests,
  invalid tokens, and API-server failures. Network-level restrictions
  (LoadBalancer source ranges, NetworkPolicy) stay recommended defense in
  depth.

### 4b. Client authentication

- Bearer token, as today: the client already attaches its kubeconfig
  token to every RPC (`pkg/client/k8s/manager_token.go`); the manager
  already validates it via TokenReview. Nothing new server-side.
- Client certificate becomes direct, optional mTLS on the external gRPC
  listener — and this is new server behavior, not a relocation of the
  current auth-only listener (which verifies the cert in a separate
  handshake and mints a bearer token): configure the listener's
  `grpc.Creds` with a TLS configuration that verifies a client
  certificate when one is presented while still admitting bearer-only
  clients, reuse `ClientCAPool` and the existing
  certificate-to-Kubernetes-identity conversion, and attach the
  `Principal` derived from the transport's verified peer certificate to
  the request context before authorization. Credential semantics: the
  client presents exactly one mechanism — bearer whenever a bearer source
  exists, certificate only otherwise, matching today's fallback order —
  and the server rejects a call presenting both as ambiguous. That
  sidesteps defining identity equality across mechanisms, where username,
  groups, and extra claims can all legitimately differ and would change
  the subsequent SAR. Bearer-token refresh stays independent of the
  connection-level certificate identity. Verification must outlive the
  handshake: the transport principal carries the verified CA generation
  and the certificate's `NotAfter` — the same properties the token-mint
  path enforces today by tying minted tokens to the CA generation — the
  interceptor rejects a principal whose generation is stale or whose
  certificate has expired, and a client-CA change proactively closes
  certificate-authenticated external connections. Tests cover bearer
  only, certificate only, both-presented rejection, and — against an
  already-established connection, not merely a fresh handshake —
  certificate expiry and client-CA rotation. The externalized token-mint
  exchange remains a fallback design if direct mTLS proves troublesome.

### 4c. Client: a manager-transport abstraction

Adding an address override to `connect.go` is not sufficient. Today the
user daemon constructs a `Cluster`, unconditionally calls discovery
`ServerVersion`, may start the namespace watcher, and scans for the
manager Service — and the root daemon independently builds its own
kubeconfig and `Cluster` and connects to the manager through Kubernetes,
including on reconnect (`pkg/client/rootd/session.go`). Symbolic
service-port resolution also queries Kubernetes directly.

- Introduce a manager-transport abstraction, threaded through both
  daemons, covering: initial dialing, reconnection, TLS trust
  configuration, credential acquisition, whether Kubernetes fallback is
  allowed, and whether direct agent port-forwards are available.
- Client config distinguishes the roles with an explicit scheme; the QUIC
  address continues to come from the manager after authentication:

  ```yaml
  cluster:
    managerAddress: tls://tm.example.com:8443
    managerServerCA: <pem or file>   # pin, unless publicly trusted
  ```

- In external-only mode, client startup bypasses API discovery, namespace
  watching, manager-Service lookup, `CanPortForward` probes, and API-based
  reconnect entirely. Symbolic service-port resolution moves manager-side
  (decided): a `ResolveServicePort` RPC, served on both listeners, returns
  the service's ClusterIP and numeric port. The user daemon calls it and
  keeps the direct Kubernetes lookup only as a fallback when the manager
  answers `Unimplemented`; in external mode that fallback degrades to the
  explicit use-a-numeric-port error. The root daemon's `ResolvePort` RPC
  is removed outright: the user daemon resolves numeric-port hostnames
  through the root daemon's existing `LookupIP` (the identical local-DNS
  lookup), so nothing port-related remains in the root daemon.
- `telepresence setup` is the natural surface for emitting and validating
  the config: it already verifies external QUIC reachability with a real
  handshake after `--apply`, and its facts record which credential kinds
  the client's kubeconfig can produce — exactly the two mechanisms above.
  It validates the TLS control endpoint independently of QUIC — a
  `Version` call and an authenticated-session probe over TLS/gRPC — since
  the control endpoint must work precisely where UDP is blocked, and a
  QUIC handshake proves nothing about it.

The decisive regression test blackholes the Kubernetes API server from
the client while leaving the external endpoint reachable: connect,
reconnect after a manager restart, DNS, intercept creation, log
collection, and shutdown must all succeed with zero API requests from
either daemon.

RBAC effect after phase 4: zero mechanical client RBAC. The remaining
grants exist only as authorization policy read by the manager via SAR.

### Making the external endpoint the only path

Publishing an endpoint stops the chart from granting the port-forward
bootstrap (decided): the rendered connect role drops its mechanical
`pods/portforward` rule, keeping only the grants the required grant
reviews, since external clients never port-forward (the `portforward`
required-grant exception is described under the required-grant model).
Nothing server-side can disable the path itself: the manager's in-cluster
gRPC port must stay open for agents, and the port-forward is a Kubernetes
API operation the API server enforces. What remains is RBAC granted
elsewhere:

- Set `requiredGrant: telepresence`. Identities holding `pods/portforward` on the
  manager namespace for unrelated reasons (narrowly scoped debugging
  roles) can still open the tunnel, but the connect-time review demands
  `create` on `connections.telepresence.io`, so the tunnel yields no
  session. The same setting moves the per-namespace grants off
  `pods/portforward`, retiring the direct-agent-dial path with it.

Neither part constrains wildcard identities: `cluster-admin` passes every
SubjectAccessReview, including for Telepresence's attributes (see the
wildcard caveat under [Authorization](#authorization-policy-without-mechanism)).

## Authorization: policy without mechanism

Today a client's RBAC is both the policy and the mechanism.
`pods/portforward` is what an admin grants to express "may intercept here",
and it is also what the client physically exercises to do it, so the API
server is the enforcement point and the manager's `SubjectAccessReview` in
`authorizeIntercept` only makes an already-binding requirement explicit.

Moving the mechanics server-side breaks that coincidence. The grants remain
the policy language, but the traffic-manager becomes the enforcement point.
Two things follow.

### Connect must be authorized, not just authenticated

`ArriveAsClient` checks that the namespace is managed and records the
caller's principal on the session, but runs no review — `authorizeIntercept`
is the manager's only authorization call site. That is sound today only
because reaching the manager's gRPC port at all requires `pods/portforward`
on the manager pod: the connect Role *is* the connect control point,
enforced by the API server.

Phase 4 removes that control point. Without a replacement, any caller holding a valid
cluster identity — an arbitrary ServiceAccount token included — that can
reach the external endpoint may establish a session and obtain the managed
namespace list, cluster DNS, and routing. Intercepts would still be denied,
but "no Telepresence access at all" would no longer be expressible by
withholding a Role.

Phase 1 therefore adds the review at session establishment (and on the
restore path), before any external endpoint exists to need it.

### What the review should ask

`pods/portforward` works today as a proxy: a caller holding it could reach
the pod by hand anyway, so Telepresence grants no capability the caller
lacked. Once port-forwards are optional the proxy weakens in both
directions — an admin can neither allow Telepresence while withholding raw
`kubectl port-forward`, nor deny Telepresence to someone who holds
`pods/portforward` for unrelated debugging. The grant also turns vestigial:
admins would grant a permission precisely so that it is never exercised.

The alternative is to review Telepresence's own attributes in a
`telepresence.io` group: `create` on `connections` in the manager
namespace to connect, an *attachment* review in the target namespace —
`attachments` being the project's umbrella term for intercepts and
ingests, which share the `EnsureAgent` surface and its exposure of
container environment and mounts — and `get` on `logs` for diagnostics.
The attachment review names the workload as the resource name, with the
verb separating the traffic-affecting operation from the read-only one,
so chart authors can write ordinary rules:

```yaml
- apiGroups: ["telepresence.io"]
  resources: ["attachments"]
  resourceNames: ["payments"]
  verbs: ["create"]        # intercept; "get" authorizes ingest
```

reviewed as `{group: telepresence.io, resource: attachments,
name: payments, verb: create|get}`. The review does not qualify the
workload kind as a subresource (decided): which kinds exist at all is
governed globally by the `workloads.*.enabled` Helm values, no plausible
policy admits a user to Deployments but not StatefulSets, and Kubernetes
RBAC has no `attachments/*` form, so per-kind subresources would force
every hand-written Role to enumerate them. A Deployment and a StatefulSet
sharing a name are therefore both covered by one name-scoped grant — an
accepted imprecision. For the same reason, an ambiguous same-name
workload keeps resolving by priority order across the enabled kinds
(decided): the request-level kind disambiguation a kind-qualified review
would have required goes away with it. No CRD is needed. The RBAC
authorizer matches rule
strings and never consults discovery, so a Role naming a resource the API
server has never heard of authorizes normally. Verified against a live cluster: a
`SubjectAccessReview` for
`{group: telepresence.io, resource: intercepts, verb: create}` returns
`allowed: true` with the granting RoleBinding named in `status.reason`, and
`kubectl auth can-i --list` shows the rule. Point queries
(`kubectl auth can-i create intercepts.telepresence.io`) answer `no`
whatever the grants, because kubectl cannot map an unregistered resource to
its group, so docs should send admins to `--list` or a raw review.

Two properties of this scheme must be stated plainly rather than assumed:

- **Wildcard identities always pass.** Kubernetes RBAC is additive and
  allow-only, and wildcard rules match every resource in every group —
  including unregistered ones. `cluster-admin` and similar roles therefore
  pass the SAR for Telepresence's attributes without any Telepresence
  Role. The required grant separates Telepresence access from *narrowly
  scoped* portforward/debugging roles; it cannot express a denial for an
  identity that already holds wildcard authorization.
- **Name-scoping `create` here works because the review is synthesized.**
  Kubernetes documents that top-level `create` cannot generally be
  restricted by `resourceNames` because the name is unknown at admission;
  a manually constructed `SubjectAccessReview` populates
  `ResourceAttributes.Name`, and the RBAC authorizer compares it against
  `resourceNames`. The regression suite covers the grant matrix:
  namespace-wide grant, matching workload name, non-matching name, empty
  name, wildcard roles.

That buys precision `pods/portforward` cannot express:

- Telepresence access becomes separable from port-forward access, in both
  directions (wildcard identities excepted, as above).
- `resourceNames` can scope to *workload* names, which are stable. The
  current two-step review (namespace-wide, then once per pod) exists only
  because pod names are random; reviewing a Telepresence resource drops
  both the loop and the requirement that the pods already exist.
- Intent is legible in the Role instead of inferred from a mechanical
  permission.

The cost is real and must be documented rather than glossed: when the
required grant is `any` or `telepresence`, the manager grants pod-level
reach — traffic, and the container environment including Secret-derived
values — to a caller who may hold no `pods/portforward` at all. The admin
is delegating enforcement of the `telepresence.io` group to Telepresence
rather than observing the API server's.

Recommended shape: a `security.authorization.requiredGrant` value taking
`portforward` (today's proxy alone), `telepresence` (review Telepresence
attributes only), or `any` (either passes). The default is `any`, in
every authentication mode, from phase 1 on (decided): a non-breaking
superset of today's behavior that lets `telepresence.io`-only Roles work
immediately and starts the migration clock. While the required grant is
`any` or `portforward`, the manager logs a per-session warning whenever a
caller authorized only via the legacy `pods/portforward` grant — that log
is the admin's inventory of hand-rolled Roles still needing migration.
Connect and attachment reviews follow the same setting, and the chart
renders client Roles to match; diagnostic (log) authorization is
independent of the required grant, as phase 2 specifies. The
enforcing-mode default flips to `telepresence` (decided) no earlier than
two minor releases
after phase 4, preferentially at the next major version — breaking only
for hand-rolled Roles that still carry nothing but `pods/portforward`,
since chart-rendered Roles migrate with the chart upgrade. The
non-enforcing modes stay on `any`.

Reviews run at session and intercept creation only — no periodic
re-review (decided). Revoking a grant therefore does not tear down what
is already running; when a revocation is time critical, the admin
restarts the manager: clients re-establish their sessions and intercepts
automatically, and the phase-1 restore reviews re-run, so the revoked
caller is the one that does not come back.

## Compatibility matrix

| Client \ Manager | pre-phase-2                  | phase 2                      | phase 3                        | phase 4                |
|------------------|------------------------------|------------------------------|--------------------------------|------------------------|
| pre-phase-2      | today                        | works (old paths intact)     | works (discovery still works)  | works via port-forward |
| phase 2          | falls back to client-side    | RPCs used                    | RPCs used                      | RPCs used              |
| phase 3          | falls back + discovery       | falls back to discovery      | known-name port-forward        | known-name or external |
| phase 4 (config) | n/a (no endpoint published)  | n/a                          | n/a                            | external endpoint      |

(Phase 1 is server-side only and absent from the matrix: it changes no
wire format and no capability negotiation. It does change observable
behavior — an unauthorized caller is now refused at session establishment
and `PrepareIntercept` — which is its purpose.)

The matrix states *protocol* compatibility; chart configuration is a
separate axis, in both directions. A pre-phase-2 client "works via
port-forward" against a phase-4 installation only while that installation
still renders the connect Role, and old clients work after phase 3 only
while the legacy discovery grants toggle remains enabled; symmetrically, a
phase-2 client "falls back to client-side" against an old manager only
where the installation still grants the namespace, pod, and log RBAC that
fallback exercises. Capability detection uses the
existing `VersionInfo2`/`GetClientConfig` handshake; no new negotiation
mechanism is needed.

## Verification

Each phase lands with suites in the regression suite (`regression_test/`,
`make check-regression`). The touched paths map to the `auth` area (token
and x509 authentication), `connect` (discovery and port-forward bootstrap),
and `namespaces` (managed-set propagation). Chart changes — the connect-role
reduction and the StatefulSet conversion — additionally surface in the
golden chart-rendering matrix (`regression_test/golden`), which renders the
chart over a value matrix with no cluster.

Phase-specific assertions:

- Phase 1 (`auth`): per required-grant value, an authenticated caller without the
  Role is refused a session and refused at `PrepareIntercept` before any
  mutation; a restored session and its intercepts are re-reviewed after a
  manager restart, and a revoked caller's restore is refused. A mixed
  restoration case: two authorized intercepts and one revoked, with the
  session and both authorized intercepts restored and the revoked one
  omitted. Restoration normalization: a restore payload carrying a forged
  ID, foreign `ClientSession`, or fabricated runtime state yields a
  normalized `WAITING` intercept, not the payload's contents.
- Phase 3: a real manager rollout with a connected client, an active
  intercept, continuous traffic, and in-flight control RPCs; assert
  reconnection, restored intercept state, resumed traffic, and no
  duplicate intercepts or agents. This runs alongside the phase-1
  restore-review assertions — a fresh manager must reauthorize restored
  state, not merely accept the client's restoration payload.
- Phase 4, two transport cases. First, the blackhole test — API server
  unreachable from the client, external control endpoint and QUIC both
  reachable: the full lifecycle succeeds with zero client API requests,
  including actual intercepted-traffic delivery before and after a
  manager restart (the assertion that exercises the client-to-agent
  QUIC channel), plus agent-sourced environment retrieval through
  `--env-file` — not merely intercept creation. Second, the UDP-blocked case — external control endpoint
  reachable, QUIC not published: connect, DNS, workload browsing, log
  gathering, and manager-mediated outbound traffic all work, and
  intercept/ingest creation fails clearly with the capability-check
  error rather than appearing functional. Plus an anonymous probe of the
  external listener proving the internal-only methods (agent RPCs,
  `WatchQuicBackends`) are absent, and an authenticated caller lacking
  the connection grant refused every method except the deliberately
  public `Version`/health surface.
- The attachment-review grant matrix: namespace-wide grant, matching and
  non-matching workload name, empty name, wildcard roles.

