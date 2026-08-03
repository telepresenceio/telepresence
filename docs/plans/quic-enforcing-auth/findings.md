# QUIC forwarder in enforcing mode: investigation findings

Investigated 2026-08-02 against `thallgren/regression-test-framework` (tip `cbc552d1e`).

Two questions were asked: why does the QUIC forwarder deny everything when
`security.authentication.mode=enforcing`, and does that failure mask a case where a
client reaches a pod over QUIC that it has no port-forward access to. Both are
answered below. The first is a straightforward bug with a clear fix. The second is
real, but it is not caused by, or unique to, QUIC — and one part of it *is* new.

**Outcome (decided 2026-08-02).** Fix Finding 1. Fix `SetLogLevel`'s missing
authorization. Treat Finding 4 (unauthenticated agent file sharing) as the real
priority. Finding 2 is explicitly **not** Telepresence's problem — pod and port
reachability belongs to Kubernetes NetworkPolicy. Finding 3's session binding is
deferred and should be built on top of Finding 4's work, since both need the same
primitive. Sections on Finding 2 and the certificate-scoping design are retained as a
record of the reasoning, not as proposals.

## Finding 1 — the forwarder cannot authenticate to the manager (confirmed)

The quic-forwarder gets its backend allowlist by calling the traffic-manager's
`WatchQuicBackends` RPC. It dials the manager's ordinary gRPC port with no
credentials at all:

`cmd/traffic/cmd/quicforwarder/allowlist.go:180`

```go
conn, err := grpc.NewClient(address,
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    ...
```

`address` is `MANAGER_HOST:MANAGER_PORT`, which the chart sets to the manager's
`apiPort` (`charts/telepresence-oss/templates/quicforwarder.yaml:66`). That is the
same port as `SERVER_PORT`, served by `service.serveHTTP`, which installs the
authentication interceptor (`cmd/traffic/cmd/manager/manager.go:415`).

The interceptor exempts only two methods:

`cmd/traffic/cmd/manager/auth/interceptor.go:71`

```go
func skipAuth(method string) bool {
    return method == versionMethod || strings.HasPrefix(method, healthMethodPrefix)
}
```

`WatchQuicBackends` is not among them, so in `ModeEnforcing` the stream is rejected
with `Unauthenticated` before the handler ever runs. From there the failure cascades
deterministically:

1. `WatchAllowlist` retries every 5s and is rejected every time, so `Allowlist.update`
   is never called and `Allowlist.Ready()` stays false forever.
2. `Router.Route` drops every datagram at its first line as `dropNotReady`
   (`cmd/traffic/cmd/quicforwarder/router.go:143`), logging
   `"no backend allowlist yet; dropping all QUIC traffic until the first snapshot arrives"`
   exactly once.
3. `/healthz` returns 503 forever (`health.go:26`), so the quic-forwarder pod never
   becomes Ready and the `traffic-manager-quic` Service ends up with no endpoints.

Nothing in the forwarder's deployment could fix this by configuration: its
ServiceAccount deliberately has no Role or RoleBinding, and the code never reads a
projected token even though one is mounted by default.

The manager's side of the feature is unaffected — `GetQuicTunnelEndpoint` keeps
advertising the endpoint — so clients keep attempting QUIC, failing, and falling back
to the port-forwarded path silently. In enforcing mode the QUIC transport is
therefore entirely dead, and the only outward symptom is a permanently NotReady
forwarder pod.

### Reproducer

Verified with a temporary test in `cmd/traffic/cmd/quicforwarder`: start the existing
`fakeQuicBackendsServer` behind a stream interceptor that rejects everything except
`/telepresence.manager.Manager/Version` with `codes.Unauthenticated`, run
`WatchAllowlist` against it, and assert that `allowlist.Ready()` is still false after
1.5s and that a subsequent `Router.Route` increments `dropped[dropNotReady]`. Both
held. This should become the regression test accompanying the fix (asserting the
*fixed* behaviour, i.e. that the allowlist does become ready).

### Fix options

Preferred: add `WatchQuicBackends` to `skipAuth`. It carries no `SessionInfo`, is
already documented as callable without a session, and returns only pod IPs and ports
of manager/agent pods — which any principal that can read pods already has. This
matches how `Version` is treated.

Alternative: have the forwarder present its own projected ServiceAccount token as
per-RPC credentials. This authenticates the caller but adds no authorization (the
interceptor only authenticates), so it costs a token-projection volume, a refresh
loop, and a rolling-upgrade ordering constraint in exchange for very little. Not
recommended unless the RPC is also given a real authorization check.

Either way, a forwarder that cannot reach the manager should be louder than one
`Info` line; the repeated `WatchQuicBackends` rejection is currently only visible in
the watcher's retry logging.

## Finding 2 — the port-forward RBAC gate does not exist on the QUIC path (confirmed, but pre-existing)

The user's hypothesis is correct as stated: a client can reach an agent pod over QUIC
that it has no `pods/portforward` access to. The chain is:

1. A client establishes a manager session. This requires only
   `create pods/portforward` in the **traffic-manager's own namespace**, granted by
   the chart's `traffic-manager-connect` Role.
2. `GetQuicTunnelEndpoint` mints it a client certificate with no further checks
   beyond session ownership (`service.go:1466`).
3. The client dials the forwarder with SNI `<podUID>.agent.telepresence`. The
   forwarder resolves it against an allowlist containing **every live agent in the
   cluster** — `WatchQuicBackends` applies no namespace filter (`service.go:1613`).
4. The agent's QUIC listener accepts any certificate chaining to the manager's CA and
   applies no further check
   (`cmd/traffic/cmd/agent/quicserver/listener.go:130`, and see the explicit comment
   at line 196).
5. The client now has the agent's full gRPC surface, including `Tunnel`, which is an
   unfiltered dialer running inside that pod (`cmd/traffic/cmd/agent/server.go:70`).

Compare the transport this replaces. The chart's per-namespace client Role grants
`pods/portforward` create in exactly the namespaces the user is scoped to, and the
template says so outright: *"All traffic will be routed via the traffic-manager unless
a portforward can be created directly to a pod"* (`_helpers.tpl:249`). On the
port-forward path the API server enforces that per pod, per namespace. On the QUIC
path nothing does.

**However**, the architecture document already anticipates this
(`docs/reference/quic-transport-architecture.md`, "Agent connections over QUIC"), and
its argument checks out: an authenticated session can *already* reach any pod IP and
port in the cluster through the manager's own tunnel, because `State.Tunnel` →
`clientTunnel` → `tunnel.NewDialer` performs no destination filtering whatsoever
(`cmd/traffic/cmd/manager/state/state.go:765-782`). The manager dials whatever a
tunnel stream names, using the manager's cluster access. So agent-QUIC does not
create the escalation; the unfiltered manager tunnel already did, and the
session-scoped certificate makes the QUIC path marginally *stricter* than the VPN
path it parallels.

Conclusion: this is a real property worth documenting and, if we want per-namespace
enforcement, worth fixing — but it must be fixed at the manager tunnel, not only at
the agent QUIC listener. Fixing only the QUIC path would close a door while the
adjacent one stays open. It is not a regression introduced by the QUIC work, and it
is not masked by Finding 1 in any meaningful sense: Finding 1 disables QUIC, but the
manager-tunnel route to the same capability stays open in every mode.

## Finding 3 — the agent never binds a connection to the session it claims (new)

This one is *not* covered by the architecture document's argument, and is the part of
the user's concern that does represent a genuine gap.

The manager's QUIC listener binds each stream to the identity in the client
certificate:

`cmd/traffic/cmd/manager/quictunnel/listener.go:211`

```go
if sid := string(stream.SessionID()); sid != certCN {
    clog.Errorf(ctx, "quictunnel: stream session id %q does not match client certificate CN %q; closing", sid, certCN)
```

The agent's QUIC listener has no equivalent check — it explicitly declines to make one
(`quicserver/listener.go:196-199`). The certificate's CommonName is the minting
client's session ID, but nothing compares it to the session ID the caller then names
in its RPCs. That matters because the agent keys real state on a caller-supplied
session ID:

`cmd/traffic/cmd/agent/server.go:104-113`

```go
func (s *state) WatchDial(session *rpc.SessionInfo, server agent.Agent_WatchDialServer) error {
    sid := tunnel.SessionID(session.SessionId)
    s.dialWatchers.Store(sid, drCh)
```

`Store` is unconditional, so a caller that names another client's session ID
**overwrites** that client's dial-watcher channel. `CreateClientStream` then sends
that session's `DialRequest`s to the overwriting caller
(`server.go:142`), which is intercepted traffic destined for another user's
workstation. `Tunnel`'s `awaitingForwards` lookup is keyed the same way
(`server.go:76`).

Strictly, this hijack is also reachable over the port-forward path — but there it
requires `pods/portforward` on that specific agent pod, i.e. the attacker must already
be scoped to that namespace. Over QUIC, as things stand today, any client with a
manager session can reach any agent in the cluster, so the set of principals who can
attempt it widens to every Telepresence user.

### Ramifications, given Finding 2 is not being fixed

An earlier revision of this section reasoned about a world where Finding 2 was fixed,
which would have confined the attacker to namespaces it already held
`pods/portforward` on. **That is not the decision taken** (see "Suggested order of
work"): pod reachability is left to NetworkPolicy, so nothing in Telepresence narrows
who can reach an agent's gRPC surface.

The practical effect is that this is *not* a QUIC-specific issue and never becomes
one. Any user who can `telepresence connect` can reach any agent in the cluster —
over QUIC once Finding 1 lands, and over the manager tunnel regardless. So the
attacker set is every Telepresence user, on every transport, and the only thing
standing between them and the hijack below is knowledge of the victim's session UUID.

Given Alice (holding an intercept on workload W) and Bob (any connected user, holding
no intercept and needing no RBAC in W's namespace), Bob can:

* **Intercept Alice's traffic.** Bob calls `WatchDial` naming Alice's session ID,
  displacing her channel. The next intercepted request the agent handles is routed
  to Bob via `CreateClientStream` → `dialWatchers.Load(Alice)`
  (`fwd/tcp.go:194` → `server.go:142`). Bob completes the leg by opening `Tunnel`
  with a stream declaring Alice's session ID and the matching `ConnID`, which
  `awaitingForwards` hands straight to the waiting dialer. Bob then reads the full
  request — bodies, headers, any bearer tokens or cookies they carry.
* **Answer it.** The same stream is bidirectional, so Bob controls the response
  returned to the real in-cluster caller. This is active manipulation, not just
  eavesdropping.
* **Break the intercept, persistently.** `WatchDial`'s
  `defer s.dialWatchers.Delete(sid)` removes the entry outright when Bob disconnects,
  so Alice's intercept keeps failing with "no dial watcher" after the attacker is
  gone, until she reconnects.
* **Do it invisibly.** Alice's own `WatchDial` stream is never closed, so her client
  observes nothing. The manager still attributes the intercept — and, via
  `ReportMetrics`'s caller-declared `ClientSessionId` (`server.go:96`), the traffic
  metrics — to Alice.

Bob wins the race deterministically by re-registering periodically, since `Store` is
last-writer-wins.

The one real cost to the attacker is that **`WatchDial` requires the victim's session
ID**, which is a UUIDv4 (`state.go:440`) and therefore not guessable. It has to be
harvested. The most direct route is the agent's own logs: `WatchDial` logs the client
session ID at Debug (`server.go:106-107`), and the chart's client Role grants
`pods/log` `get` in the user's namespaces (`_helpers.tpl:247`). Debug is not the
default, but `SetLogLevel` (`service.go:1912`) performs **no authorization check of
any kind** — no session, no principal, no RBAC — and propagates cluster-wide to every
agent via `WatchLogLevel`. So the attacker can enable the logging that leaks the
identifier it needs. That missing check is worth fixing on its own merits.

What this does *not* give an attacker: no access to workloads nobody is intercepting,
no cluster credentials from this vector, and no ability to register as a fake agent —
`ArriveAsAgent` rejects a principal whose bound-token pod claims don't match the
presented identity when in enforcing mode (`service.go:335-339`), which closes the
route to reading other clients' session IDs out of the agent-scoped `WatchIntercepts`
feed. Clients themselves only ever see their own intercepts (`service.go:1024-1028`).

Net: an integrity and confidentiality attack against a colleague's active intercept,
available to any connected user, gated only by knowledge of a session UUID that today
the attacker can make the agents log for it.

The fix is discussed under "Why Finding 3's binding is deferred, not dropped" in
"Suggested order of work" — briefly, the obvious version (bind to the QUIC peer
certificate's CommonName) only covers the QUIC transport, and the port-forward
transport delivers no caller identity to the agent at all, so a complete fix needs a
session credential the client presents and the agent can verify offline.

## On "QUIC security on par with port-forward"

Fixing Finding 1 alone does unblock QUIC without restoring the `pods/portforward`
gate on the agent-connection path. That much is true. But the parity target needs
stating carefully, because **port-forward security is not itself RBAC-uniform**.

There are two client -> cluster data paths, and only one of them has ever been gated
by per-namespace `pods/portforward`:

| Path | Gate on the port-forward transport | Gate on QUIC |
|---|---|---|
| Agent gRPC connection (`agentpf`) | apiserver enforces `pods/portforward` per agent pod | None — any CA-signed session cert |
| Manager tunnel / VPN (`rootd`) | None — `State.Tunnel` dials any destination | None |

The manager tunnel has no destination filtering at all
(`state.go:765-782`), and it rides the port-forwarded gRPC connection by default. So a
connected client can already dial any pod IP and port in the cluster — including an
agent's own API port — over the *port-forward* transport, with no per-namespace check.
The architecture document states this outright.

Consequences for the goal:

* Restoring a `pods/portforward`-equivalent check on the agent QUIC path would achieve
  literal parity with the port-forward transport.
* It would **not** achieve "a client can only reach what its RBAC allows", because the
  manager tunnel remains an unfiltered cluster-wide dialer on both transports. Closing
  the QUIC door leaves the larger one open.

**This was decided against** (see "Suggested order of work"): pod and port
reachability is a cluster networking concern, enforced with NetworkPolicy, not
something the traffic-manager should reimplement per-user. Everything from here to the
end of this section is the design that *would* have been needed, retained to document
the cost that was weighed.

Where the check has to live is forced by the topology, and it is worth spelling out
because it rules out the obvious options:

* **Not the forwarder.** It is stateless by design and never terminates TLS, so it
  cannot see who is connecting — only an SNI it must take at face value.
* **Not the manager at dial time.** The manager is not in the QUIC data path at all.
  It never observes the connection.
* **Not the advertised SNI list.** Scoping what `WatchAgentPods` advertises is
  advisory only: the client chooses the SNI it dials, and the forwarder's allowlist is
  cluster-wide.

The only artifact that travels from the manager (which knows the caller's RBAC) to the
agent (which terminates the connection) is the **client certificate**. So the
authorization decision has to be made at mint time and carried inside the certificate.
That is what "the certificate-scoping work" means, and it is expanded below.

### The certificate-scoping work, in detail

Today `MintClientCert` (`quictunnel/ca.go:169`) puts one thing in the certificate: the
session ID, as the CommonName. The agent checks only that it chains to the CA
(`quicserver/listener.go:130`). The work is to add an *authorization scope* to that
certificate and make the agent enforce it. Two shapes are viable.

**Option A — one session certificate carrying a namespace list.**

At `GetQuicTunnelEndpoint`, the manager enumerates its managed namespaces, calls
`Authorizer.CanPortForward(principal, ns, nil)` for each, and embeds the allowed set in
the minted certificate. The agent knows its own namespace, so it adds a
`VerifyPeerCertificate` to the `tls.Config` that `getConfigForClient` already builds
per handshake, and rejects a peer whose scope set doesn't contain it.

* Cost: N SubjectAccessReviews once per session (N = managed namespaces, typically one
  to a handful). The client flow is unchanged — still one certificate for the session.
* Weakness: the scope is frozen at mint time for the certificate's whole lifetime,
  currently 24h (`clientCertValidity`). Revoking a user's RBAC does not revoke their
  QUIC reach until it expires.

**Option B — a certificate per agent, minted on demand (recommended).**

Add an RPC mirroring the existing `GetQuicAgentCert` (which mints the agent's *server*
certificate): `GetQuicAgentClientCert(session, podUID)`. The manager resolves the pod
UID to its agent session, takes that agent's namespace and pod name, calls
`CanPortForward(principal, namespace, []string{podName})`, and mints a client
certificate naming that one agent only if the review allows it. The agent verifies
that the presented certificate names *its own* SNI — a string compare against the
`Sni` it already receives from `GetQuicAgentCert` and currently only logs
(`agent/quic.go:66`).

* Client side is a contained change: `agentpf.dialAgent` already runs per agent and
  already holds `ai.PodId` and the namespace (`clients.go:178-190`). The certificate
  moves out of the session-wide `quicEndpoint` and becomes per-agent, so
  `quicEndpoint.tlsConfig(sni)` takes the certificate as a parameter instead of
  reading `e.cert`.
* Cost: one extra unary RPC per agent per session, on a path that already makes a
  manager RPC before its first dial.
* Uses `CanPortForward`'s per-pod-name fallback, so RBAC grants scoped to specific pod
  names are honoured — the same behaviour the apiserver gives the port-forward path.
* Naturally short-lived and single-target, so the revocation window is one agent and a
  lifetime we choose (these certificates should not use the 24h
  `clientCertValidity`; minutes is appropriate since they are re-minted per dial).

Recommendation: **B**. It authorizes against the actual target instead of a
precomputed set, needs no namespace enumeration, yields a scope the agent can check
with a string compare, and composes with Finding 3 — one certificate then carries both
the session ID that Finding 3's binding needs and the target that Finding 2 needs, so
doing the two together is cheaper than doing them separately.

**What it achieves, and what it does not.**

* Achieves literal parity with the port-forward transport on the agent-connection
  path: same verb, same resource (`create pods/portforward`), same per-pod-name
  fallback, evaluated against the same principal.
* Does **not** achieve freshness parity. The apiserver re-evaluates RBAC on every
  port-forward; a certificate is a bearer credential valid for its lifetime. Option B
  narrows the window rather than closing it.
* Does **not** touch the manager tunnel, which stays an unfiltered cluster-wide dialer
  (the larger half of Finding 2). Parity on the agent path does not mean a client is
  confined to its RBAC.
* Does **not** by itself fix Finding 3, though it lays the plumbing that fix needs.

**Rollout compatibility — the part that makes this more than a patch.**

* *New agent, old manager*: no scoped certificate exists. The agent must choose
  fail-open (accept, preserving today's gap) or fail-closed (reject). Fail-closed is
  tolerable because the client's fallback is already exercised and silent —
  `agentDialer` drops to `dialFallback` on any QUIC error (`agentpf/quic.go:187-202`) —
  but it must be gated on a detectable manager capability, not assumed.
* *Old agent, new manager*: the old agent ignores the scope and accepts any CA-signed
  certificate. The manager cannot compensate, so the gap persists until agents are
  upgraded — and agents upgrade with workload rollouts, not with the chart. This
  should be stated in the release notes rather than discovered.

So the work is an RPC addition, a certificate format change, an agent-side
verification path, and a two-sided version gate — not a one-line fix.

**Open questions to settle before implementing.**

* *Node agents.* A node-agent pod appears to be created in the **manager's** namespace
  (`ensureNodeAgentTarget(ctx, jobsClient, mgrNs, ...)`,
  `state/nodeagent_watch.go:253`) while serving a workload in another namespace. If
  the authorization check uses the agent *pod's* namespace, a node-agent becomes
  authorizable by any connected user, since every user has portforward in the manager
  namespace — which would be a hole, not a fix. The check must use the target
  workload's namespace. `AgentSession.Namespace` is the likely carrier (it is what
  `createAgentPodWatchers` filters clients on, `service.go:642-645`), but I did not
  confirm what it holds for a node agent. **Verify before designing around it.**
* Certificate lifetime for per-agent certificates, and whether they are re-minted per
  dial or cached for a bounded period.
* Whether fail-closed on an unscoped certificate is default or opt-in, and under which
  chart value.
* Whether the same scoping should apply to the manager-bound QUIC connection, or
  whether that is left to the (larger) manager-tunnel authorization question.

### Is Finding 1 safe to ship on its own?

It does not create a new class of exposure. `quicTunnel.enabled` defaults to `false`
(`values.yaml:264`) and the default authentication mode is `permissive`
(`values.yaml:84`), so every QUIC install running today already has exactly this gap;
Finding 1's fix extends the existing behaviour to enforcing-mode installs rather than
introducing it.

The counter-argument is about audience, and it is a strong one: enforcing mode is
chosen precisely by operators who want strict authorization. Handing that population a
QUIC path that skips RBAC is the wrong default for them specifically. So either ship
Finding 1 together with the certificate-scoping work, or ship it with QUIC still
requiring an explicit opt-in under enforcing mode and the authorization model
documented as session-scoped rather than RBAC-scoped. What should not happen is
Finding 1 landing quietly as "QUIC now works in enforcing mode".

## Finding 4 — the agent's file-sharing ports have no authentication (confirmed, and narrower than first written)

An earlier revision of this section claimed the SFTP server ignores `MountPolicies`
and that its read-write behaviour was a defect. **Both claims were wrong** and have
been removed; the corrections are recorded below because they materially shrink the
finding.

### What is actually enforced (and enforced well)

`MountPolicies` are applied at **injection time**, in
`ContainerBuilder.appendVolumeMounts` (`pkg/agentconfig/volumes.go:106-128`), not by
the symlink tree under `/tel_app_exports`:

* `MountPolicyIgnore` and `MountPolicyLocal` — the volume is never added to the agent
  container's `VolumeMounts` at all. The kubelet never mounts it, so it does not exist
  in the agent container's filesystem and no file server of any kind can reach it.
* `MountPolicyRemoteReadOnly` — mounted with `ReadOnly: true` plus
  `RecursiveReadOnlyIfPossible`. Read-only is enforced by the kernel, so an `sftp`
  server option would be redundant.
* `MountPolicyRemote` (the default) — mounted read-write, deliberately, so an attached
  client can edit files in the workload's volumes.

This is a stronger boundary than anything in userspace: it is the pod spec. Coverage
exists in `integration_test/ignored_mounts_test.go` and
`regression_test/suites/mounts/ignored.go`, plus the injector unit tests
(`mutator/agent_injector_test.go:567+`).

App volumes are also **re-rooted** under the container's mount point
(`volumes.go:121`), not left at their original paths, so they do not appear at e.g.
`/var/run/secrets/...` inside the agent container.

### What is genuinely wrong: no authentication

`agent.go:142` is `sftp.NewServer(conn)` where `conn` is a bare TCP connection off
`l.Accept()`. There is no SSH transport — no key exchange, no user authentication, no
encryption. `pkg/sftp`'s server expects an already-authenticated SSH channel and is
handed a raw socket. The client confirms the intent by running
`sshfs -o directport=<port>` (`remotefs/sftp.go:96`), which bypasses SSH entirely.

FTP is the same story with a nominal check: `go-ftpserver` registers a single
`anonymous` user whose password is the wildcard `"*"`, so any password authenticates
(`server.go:121-127`).

Verified empirically against the agent's own `sftpServer` (temporary probe, since
removed): `sftp.NewClientPipe` completed a handshake over a plain TCP socket with no
credentials whatsoever.

### Secondary: the SFTP path scope is the process root, not the exports tree

`NewServer` is called with no options, so `workDir` is empty and absolute paths
resolve against the process filesystem (`pkg/sftp@v1.13.11/server.go:92-114`,
`159`). The same probe confirmed `ReadDir("/")` returning the real filesystem root,
`Open("/etc/hostname")` succeeding, and a write landing on disk outside any exports
directory. FTP does not share this: it is confined by
`afero.NewBasePathFs(..., /tel_app_exports)`.

Because policy-excluded volumes are absent from the container entirely, this does
**not** expose anything an operator excluded by policy. What it adds beyond the
intended share surface is the agent container's own contents, notably:

* `/var/run/telepresence.io/manager-token` — the projected manager-audience token
  (`agentconfig/sidecar.go:51-57`).
* the agent pod's own default ServiceAccount token, when the workload automounts it.
* any TLS secret volumes mounted for the agent itself.
* write access anywhere the agent container's filesystem is writable.

No test asserts the SFTP path scope, which is consistent with it having gone
unnoticed.

### Net assessment

The exposure is: **anyone who can reach the pod can read and write the workload's
remote-mountable volumes, plus the agent's own credentials, with no authentication.**
Reachability is broad — both ports are published in `AgentInfo`
(`manager.proto:37-38`) and `WatchAgents` streams that to any client session in the
namespace (`service.go:862-868`) — and, given the accepted fact that the manager
tunnel dials anything, any connected user can reach them in any namespace without an
intercept.

What it is *not* is a bypass of `MountPolicies`, and read-write is not itself the bug.

### Fix directions

* **Authentication is the actual fix.** The manager already mints session-scoped
  certificates for QUIC; the same CA could gate the file-sharing ports, which would
  also make them session-scoped rather than pod-scoped. This is the real work and
  deserves its own plan.
* **Do not add `sftp.ReadOnly()`** — it would break the deliberate read-write
  behaviour of `MountPolicyRemote`, and `MountPolicyRemoteReadOnly` is already
  enforced by the kernel.
* `sftp.WithServerWorkingDirectory(agentconfig.ExportsMountPoint)` is worth considering
  as defence in depth, to bring SFTP's scope in line with FTP's and keep the agent's
  own token out of the served tree. Confirm first that no client flow depends on
  absolute paths outside the exports tree — the client mounts
  `ExportsMountPoint/<container>` (`agent.go:320`), so this looks safe, but it needs
  checking against the `sshfs`, FUSE-FTP, and docker-volume paths before changing.

### Not verified

* Whether writes succeed end-to-end through the FTP driver (`BasePathFs` over `OsFs`
  is read-write and the driver exposes STOR, but this was not exercised).
* `AuthUser`'s guard is `!(ok && user.password == "*" || user.password == password)`.
  On a map miss `ok` is false and `user` is nil, and the second `||` operand
  dereferences it — this looks like a nil-pointer panic on any unknown username. Not
  executed, and whether the ftpserver recovers per-connection panics was not checked.

## Suggested order of work

Decision taken 2026-08-02: **fix Finding 1 and stop there** on the QUIC authorization
question. The per-namespace `pods/portforward` gate on agent dialing protects nothing
in practice, because the manager tunnel is a cluster-wide dialer that will reach an
agent's gRPC port (or any other port) regardless of transport, and a modified client
can simply use it. Findings 2 and the certificate-scoping design above are therefore
recorded as analysis, not as planned work. Finding 4 is the priority instead.

**Finding 2 is not planned work, and not a Telepresence concern.** Decided 2026-08-02:
constraining which pods and ports are reachable belongs to the cluster, enforced with
Kubernetes NetworkPolicy, not reimplemented inside the traffic-manager. The
per-namespace `pods/portforward` gate protects nothing in practice — the manager tunnel
is a cluster-wide dialer that reaches an agent's gRPC port (or any other port)
regardless of transport, and a modified client can simply use it — so adding a
per-namespace check to the QUIC path would close one route while leaving the equivalent
one open, at real cost. An operator who needs that boundary applies NetworkPolicy to the
traffic-manager and quic-forwarder pods, which constrains both transports uniformly and
at the layer that actually owns the question.

Note the granularity this does and does not give: NetworkPolicy bounds what the
Telepresence components may reach, cluster-wide. It is not per-user, and per-user
reachability is explicitly not a goal. The analysis and certificate-scoping design
above are retained as a record of why that was decided, not as a proposal.

1. **Finding 1** — `skipAuth` for `WatchQuicBackends`, plus the regression test. Small,
   self-contained, unblocks QUIC in enforcing mode.
2. **`SetLogLevel` authorization** (from Finding 3's analysis) — `service.go:1912`
   takes no session, checks no principal, and performs no RBAC, yet propagates
   cluster-wide to every agent via `WatchLogLevel`. Any authenticated cluster user can
   raise every Telepresence component to Debug. Worth fixing on its own merits, and it
   is the self-service lever that makes agents log the client session IDs Finding 3's
   hijack needs.
3. **Finding 4** — authentication on the file-sharing ports. The largest concrete
   exposure; needs its own plan. Optionally scope SFTP to `ExportsMountPoint` first as
   cheap defence in depth (but *not* `ReadOnly()` — read-write is deliberate and
   `MountPolicies` already handle the rest).
4. **Finding 3's session binding** — deferred, and best done *with* item 3. See below.

### Why Finding 3's binding is deferred, not dropped

What it would buy, precisely: it stops one authorized user from silently stealing or
breaking another authorized user's live intercept — receiving their intercepted
requests (including whatever credentials those carry), answering on their behalf, and
leaving the victim's intercept broken after the attacker disconnects, with the manager
still attributing everything to the victim.

That is *not* implied by accepting Finding 2. Accepting Finding 2 means accepting flat
network reach: any connected user can talk to any pod's ports. It does not follow that
any connected user should be able to take over another user's intercept. Those are
different properties, and only the first was conceded.

Two things argue for eventually fixing it:

* The manager already refuses to treat a caller-supplied session ID as authority — it
  binds sessions to authenticated principals and rejects mismatches with "bound to
  another identity" (`ClientOwnershipError`, asserted by the enforcing regression
  suite). The agent is the one remaining place that accepts a session ID as proof of
  being that session. Session IDs are identifiers, not secrets, and they are logged.
* After `SetLogLevel` is fixed the attack needs a session UUID the attacker can no
  longer make agents emit on demand — but residual leaks remain (an admin who enables
  Debug, manager logs where the attacker holds `pods/log` in the manager namespace,
  and any future code that logs a session ID above Debug).

Two things argue for deferring:

* **The cheap half-measure does not actually work.** Making `dialWatchers.Store`
  non-displacing looks like the 80/20, but it does not hold: the attacker simply waits
  for the victim's `WatchDial` stream to drop (the `defer` clears the entry), claims
  the slot, and it is then the victim's reconnect that gets refused. It converts silent
  theft into a race the attacker still wins, plus a denial of service. It is the real
  binding or nothing.
* **The real binding costs more than "add a check", because on the port-forward path
  the agent receives no caller identity at all.** The API server authenticates the
  port-forward, but what arrives at the agent is a plain TCP connection from the
  kubelet — there is nothing to bind to. Only the QUIC path carries an identity (the
  peer certificate CommonName). A transport-independent fix therefore requires
  introducing a session credential that the client presents to the agent and the agent
  can verify offline — which also means the manager's CA must exist unconditionally,
  where today `NewCA()` is only called when QUIC is enabled (`service.go:149`).

### Why it should be done with Finding 4

Both need the same primitive: **a session-scoped credential the client presents to the
agent, verifiable by the agent without calling the manager.** Finding 4 needs it to
authenticate the file-sharing ports; Finding 3 needs it to bind `WatchDial`/`Tunnel` to
a session. The agent already fetches the manager's CA PEM (`GetQuicAgentCert` →
`ParseMaterial`), so offline verification is a short step once the CA is always
present.

Build that primitive once for Finding 4, and Finding 3's binding becomes a small
addition on top rather than a separate plumbing exercise. Doing Finding 3 first, alone,
would mean building the same machinery twice.
