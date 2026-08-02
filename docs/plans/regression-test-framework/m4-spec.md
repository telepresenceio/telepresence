# M4 spec (compat-core)

## 1. Version plumbing (framework)

- `RTEST_CLIENT_VERSION`: when set to a released version, the framework
  downloads `telepresence-<os>-<arch>` from the GitHub release (mirror
  integration_test/itest/cluster.go:327's downloadBinary: URL shape, zip on
  Windows, cache under build-output/rtest/downloads) and uses it as Exe()
  for every CLI invocation. The version under test (images, chart) stays the
  built one unless RTEST_MANAGER_VERSION also says otherwise.
- `RTEST_MANAGER_VERSION`: when set, ManagerFixture installs the RELEASED
  chart at that version: `telepresence helm install --version <v>` pulls
  from oci://ghcr.io/telepresenceio/telepresence-oss (that path only
  activates when the requested version differs from the client's, see
  pkg/client/cli/helm/chart.go:101); image registry/tag come from
  RTEST_MANAGER_REGISTRY (default ghcr.io/telepresenceio) at that version;
  pullPolicy never forced to Never. The BUILT client binary drives helm in
  this mode (whatever RTEST_CLIENT_VERSION says — helm install always uses
  the runtime Exe()... verify against the old harness: itest used the BUILT
  binary for helm when versions differ, integration_test/itest/helm.go:296;
  mirror that: keep a separate helmExe that is always the built binary).
- `compat` package (framework/compat/): `ClientVersion()`,
  `ManagerVersion()` (semver of the effective versions),
  `MinManager(t, "2.30.0")`-style gates that Skipf when the manager is
  older, mirroring itest's ClientIsVersion/ManagerIsVersion FinalizeVersion
  semantics (blang semver ranges over finalized versions).
- Baseline-values compatibility: when RTEST_MANAGER_VERSION is older, the
  merged values must not include chart keys the old chart's schema rejects
  (values.schema additionalProperties). Strategy: managers.Baseline gains a
  version guard — drop keys newer than the target manager version (only
  the ones the catalog actually uses; maintain a small key->introduced-in
  map, populated by consulting the old chart when a compat run first
  fails; start with extraEnv/extraVolumes (2.32) and nodeAgent (2.30),
  quicTunnel (2.31?) — verify from git history of values.schema.yaml).

## 2. compat-core labeling

Ensure rt.CompatCore is on tests that collectively cover the RPC manifest
below (some already are): smoke/SmokeConnected (Version, Arrive/Remain/
Depart, GetClientConfig), attach/AttachModes intercept/deployment
(Prepare/Create/Get/Remove-Intercept, EnsureAgent, agent+intercept
watches, GetAgentConfig, GetKnownWorkloadKinds), attach ingest cell
(EnsureAgent/ReleaseAgent), intercept/HeaderFilter Test_Header
(ReviewIntercept via agent, Tunnel), session/WorkloadWatch
(WatchWorkloads, session events), session/GatherLogs default case
(GetLogs), session/ManagerInfo (GetClusterInfo), connect/Lifecycle
(ReconnectClient, WatchClusterInfo), dns single resolution test once the
dns area exists (LookupDNS) — until then label an intercept-area test that
resolves the service by name (routing/MultiReplica resolves via DNS).
Compat-core tests must consult compat gates for features the old side
lacks (node-agent, quic, setup) — in practice the list above avoids
version-gated features entirely except watches, which degrade via
Unimplemented probing inside the client.

## 3. RPC manifest guard

framework/compat/manifest.go: a checked-in map testName ->
[]manager RPC method names; framework/compat/manifest_test.go (a REAL
_test.go unit test, no cluster): walks manager.Manager's grpc service
descriptor (manager.Manager_ServiceDesc from rpc/v2/manager) and fails
when a method is neither claimed by the manifest nor in the exemption
list: agent-only (ReviewIntercept, ReportMetrics, WatchLogLevel,
GetQuicAgentCert, plus Mechanism* if present), quicforwarder-only
(WatchQuicBackends), legacy/dead (GetTelepresenceAPI, deprecated watch
variants WatchAgents/WatchIntercepts/WatchAgentPods/
WatchAgentPodsDelta/WatchSessionEvents-legacy fallbacks — exempt the
LEGACY names only where the delta variant is claimed; document each
exemption inline), tunnel-internal (Tunnel is claimed by HeaderFilter;
GetQuicTunnelEndpoint exempt-with-note until the quic compat cell lands),
LookupHost legacy (Lookup) etc. Derive the real method list from the
ServiceDesc at runtime, not a hand-copied list.

## 4. checkCompat widening (production change, cmd/traffic)

cmd/traffic/cmd/manager/service.go's checkCompat currently gates only
GetKnownWorkloadKinds (2.20.0) and WatchWorkloads (2.21.0-alpha.4).
Widen: add checkCompat calls with the version each RPC appeared in for
the newer surface the client probes with Unimplemented fallbacks:
WatchSessionEvents, WatchAgentPodsInNamespacesDelta, WatchAgentPodsDelta,
WatchAgentsDelta, WatchInterceptsDelta, GetQuicTunnelEndpoint,
GetQuicAgentCert, WatchQuicBackends, LookupDNS's simple variant if
gated... derive each introduced-in version from git log of
rpc/manager/manager.proto (git log --follow -p or git log -S'rpc Watch
AgentsDelta'). Keep it mechanical: a table method->minVersion consulted
by the interceptor-style helper if one exists, else per-method calls like
the existing two. Also add `compatibility.version` to
charts/telepresence-oss/values.schema.yaml (it is wired in
deployment.yaml:366 but absent from the schema, so --set fails
validation — verify then fix). Add managers.Compat(version string) spec
in the catalog setting it, plus ONE regression test (area "session" or a
new tiny "compat" suite) that installs Compat("2.19.0")-ish and asserts
the client's fallback paths still produce a working list/intercept
(proves the Unimplemented chains end-to-end without old images).

## 5. CI

.github/workflows/dev.yaml: a `compat` job (label-gated like the old
compatibility steps, or on the existing 'compatibility test' label):
two sequential steps on the regression job's setup: (a) old manager:
RTEST_MANAGER_VERSION=<derived>, RTEST_LABELS=compat-core; (b) old
client: RTEST_CLIENT_VERSION=<derived>, RTEST_LABELS=compat-core. Derive
<derived> = the latest release tag reachable from the repo (git tag
--sort=-v:refname | grep -E '^v2\.[0-9]+\.[0-9]+$' | head -1) computed in
a step, not hard-coded. Keep the regression job untouched.

## Verification plan (orchestrator)

- Unit: the manifest guard test passes.
- Live: RTEST_MANAGER_VERSION=2.31.1 RTEST_LABELS=compat-core run against
  kind dev (chart pulled from ghcr; images public). Then
  RTEST_CLIENT_VERSION=2.31.1 RTEST_LABELS=compat-core run.
- The Compat("...") simulation test runs in the normal full suite.
