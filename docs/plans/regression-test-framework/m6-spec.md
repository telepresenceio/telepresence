# M6 spec (security area: refusals, boundaries, and restoration honesty)

Goal: cover the paths where a *missing* error is the bug. The suite today is
almost entirely happy-path: an operation that should be refused but silently
succeeds — another developer's traffic hijacked, an unauthorized intercept
created, a foreign session reaching a QUIC backend — passes every test we
have. Every refusal below exists in production code and is unit-tested;
none is asserted end-to-end by `integration_test/` or `regression_test/`.

New area `security` (suites/security/, area "security"). Restoration lands
in `install`, whose fixtures already own uninstall.

## Framework extensions (one agent)

1. **`rt.ConnAs(identity string) ConnOpt`** — override the `--as` identity a
   connection uses. `connectArgs` (framework/rt/fixture_connection.go:205)
   currently hard-codes `connectAs`; the option must *replace* that flag
   rather than append a second one, and fold into the fixture hash like
   every other ConnOpt.

2. **`rt.LimitedIdentityFixture(name string, rules ...)`** (or a fixed
   `rt.RestrictedIdentity()` if one shape suffices) — a ServiceAccount plus
   a Role/ClusterRole granting strictly less than the framework's
   `rtest-test-developer` ClusterRole (fixture_manager.go:44). Two shapes
   are needed: connect-capable-but-not-intercept (everything except
   `pods/portforward create` in the app namespace), and no-grants. Model on
   the deleted integration_test/testdata/k8s/manager_auth_connect_only.goyaml.

3. **`managers.Values.QuicForwarderLogLevel`** (chart
   `quicTunnel.forwarder.logLevel`, quicforwarder.yaml) — the forwarder's
   per-drop lines and its periodic `quic-forwarder counters: ...
   dropped[<reason>]=N` line are debug-only. Without this the allowlist
   suite has no observable signal. Typed field + merge func + reuse of the
   existing inline-Spec overlay pattern.

4. **`rt.WorkloadSpec(e, ns, kind, name) (map[string]any, error)`** and a
   diff helper — `kubectl get -o json` of a workload's
   `spec.template.spec`, normalized (drop `metadata.annotations`
   `telepresence.io/restartedAt`, `kubectl.kubernetes.io/*`,
   `creationTimestamp`, and status-ish server defaults) so before/after
   equality is assertable. Needed by install/Restoration.

5. **Second-session helper** — the conflict suite needs two live client
   sessions whose incumbent keeps its session marked active (see the
   inactive-client escape hatch below). `rt.ConnNamed` already gives two
   connections; the extension is a helper that keeps the incumbent's
   session fresh (a periodic cheap RPC, e.g. `list`) for the duration of
   the second session's attempt, since an incumbent idle beyond
   `InterceptInactiveBlockTimeout` is *deliberately* overridden rather than
   refused.

## Suites

### security area (suites/security/, area "security", NeedsManager(...) per suite)

- CrossSessionConflict (NeedsManager(Default)): two sessions
  (`rt.ConnNamed`), incumbent holds a global intercept on a workload, the
  second session's intercept on the same container port must be refused
  with `conflict with intercept <id> on port <n> created by client <who>:
  one intercept has no filters (intercepts all traffic)`
  (state/intercept.go:404 + icept/conflicts.go:156). Assert the incumbent
  is still attached and still serving afterwards — the refusal is
  worthless if it half-detaches the incumbent. Second test: overlapping
  header filters across sessions refused with `header filters overlap`.
  Third: same workload, *different* container port succeeds (the negative
  control that keeps the suite from passing vacuously).
  Supersedes inactive_client_test.go's conflict half.
  **Note:** an incumbent whose client stops pinging past
  `InterceptInactiveBlockTimeout` is intentionally overridden (the
  incumbent gets `AGENT_ERROR`); the fourth test pins that documented
  behavior explicitly, so nobody later "fixes" it into a refusal.

- InterceptAuthorization (NeedsManager(managers.AuthEnforcing) — reuse the
  auth area's enforcing spec): a client that connects successfully but
  whose identity lacks `pods/portforward create` in the target namespace
  must be refused at intercept with `codes.PermissionDenied` and
  `<identity> is not permitted to create pods/portforward in namespace
  <ns>` (service.go:1296, authorizeIntercept). Uses
  `rt.ConnAs` + the limited-identity fixture. Second test: in permissive
  mode the same intercept *succeeds* (service.go:1248 — the check is
  enforcing-mode only), which is the behavior difference the two modes
  exist for. Supersedes manager_auth_test.go's
  Test_EnforcingDeniesUnauthorizedIntercept.

- MountCollision (NeedsManager(Default)): with mounts enabled (the
  framework's attach suites all pass `--mount=false`, which no-ops these
  refusals), a second attachment reusing a mount point or mount port must
  be refused — `mount point %s already in use by ingest %s` /
  `mount port %d already in use by intercept %s`
  (trafficmgr/mount.go:78-110, `codes.AlreadyExists`). Requires FUSE, so
  `rt.Requires(rt.FUSE)`. Supersedes ingest_test.go's three conflict tests.

- QuicSessionBinding (NeedsManager(QuicNodePort)): the manager mints client
  certs whose CN is the session ID and refuses any stream whose session ID
  doesn't match its cert CN (quictunnel/listener.go:211,
  `errSessionCertMismatch`). Two live sessions over quic must each stay on
  their own streams; assert the manager never logs
  `quictunnel: stream session id %q does not match client certificate CN
  %q` during legitimate two-session use (a false positive here means
  sessions are crossing), and that both sessions' traffic stays correct.
  The negative direction (forging another session's cert) is not
  black-box reachable from the CLI and stays unit-only
  (listener_test.go's TestListener_MismatchedSessionIsRejected,
  TestListener_CrossSessionResumptionFailsClosed).

- QuicAllowlist (inline QuicNodePort + forwarder debug spec,
  WithLabels(Slow)): the forwarder drops everything until the manager's
  first `WatchQuicBackends` snapshot and reports 503 on `/healthz` with
  `no backend allowlist snapshot received yet` (health.go:23). Restart the
  forwarder with the manager scaled to zero and assert the 503 body and
  the one-shot info line `quic-forwarder: no backend allowlist yet;
  dropping all QUIC traffic until the first snapshot arrives` — i.e. a
  forwarder that cannot reach the manager fails **closed**, never open.
  Then restore the manager and assert `/healthz` goes ready and the
  `received first backend allowlist snapshot` line appears. Probing with a
  forged SNI is out of scope (no client-side lever); the
  `dropped[unresolved-sni]`/`dropped[allowlist-miss]` counters stay
  unit-covered (router_test.go).

- AgentQuicBoundary (NeedsManager(QuicNodePort)): **pins deliberate
  behavior, not a refusal.** The agent's QUIC listener verifies client
  certs against the manager's CA but performs no per-session check
  (quicserver/listener.go:200 handleConn) — any currently-connected
  client's cert is accepted by any agent, and CA rotation on manager
  restart is the only revocation. Assert the rotation half end-to-end:
  after a manager restart, a client holding pre-restart material can no
  longer use the agent's QUIC port until it re-fetches. Documents the real
  boundary so a future reader doesn't assume per-session isolation exists.

### install area addition (suites/install/)

- Restoration (NeedsManager(Default)): snapshot `spec.template.spec` before
  attaching; after `detach`, assert the workload spec is unchanged (the
  sidecar legitimately stays — injection is a Pod-level webhook mutation,
  so the *workload* spec must never drift), and for `--replace`, that the
  app container is restored (state/intercept.go:828 restoreAppContainer).
  Second test: after `telepresence helm uninstall`, pods come back
  un-injected (no `traffic-agent` container, no init container, no agent
  volumes) and the workload spec differs from the original only by the
  documented leftovers — `telepresence.io/restartedAt` and any
  user-authored `telepresence.io/inject-traffic-agent`. This is net-new
  coverage: no existing test diffs a workload spec before/after.

Area wiring: orchestrator adds `TestSecurity` (+ blank import) to
main_test.go. It churns identities and manager specs, so it runs late —
after `state`, before `quic` — with an ordering doc comment. suite-catalog
gains the security rows plus the install/Restoration row.

Notes: assert on error *text* for conflicts (PrepareIntercept returns
`PreparedIntercept{Error, ErrorCategory}`, not a gRPC status —
trafficmgr/intercept.go:575) and on `codes.*` only where the manager truly
returns a status (authorization, mount collisions). Every suite needs a
positive control in the same test file so a silently-broken fixture can't
masquerade as a passing refusal.

## Verification plan (orchestrator)

- `go vet` + `golangci-lint` on touched packages; chart golden matrix green
  after the forwarder log-level value.
- Scoped live runs per suite, then `-run '^TestSecurity$'`, then a full
  `RTEST_COVER=1` run. Coverage is not the goal here — the refusal paths
  are small — so judge this wave on the refusals asserted, not on points.
- Each new test must be seen to FAIL against a deliberately broken
  premise (e.g. run the conflict test with the incumbent detached) before
  it is trusted: a "must be refused" test that passes for the wrong reason
  is worse than no test.
