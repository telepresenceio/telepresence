# Seed suite catalog

Companion to `plan.md`. Lists the manager-config catalog, the initial areas
and suites with the `integration_test/` files they supersede, the overlap
clusters being collapsed, and the `compat-core` selection with its RPC
manifest.

## Manager config catalog (`framework/managers/`)

Named, typed value-sets. The runner groups suites by these specs and switches
between them with in-place `helm upgrade`, so the catalog size bounds the helm
cost of a full run.

| Spec | Values delta from Default | Used by areas |
|---|---|---|
| Default | baseline (debug logs, test RBAC, agentArrival 60s, usage off) | smoke, connect, intercept, attach, dns, routing, mounts, docker, session |
| NodeAgent | nodeAgent.enabled, h2c probing off | nodeagent |
| NodeAgentNoInjector | + agentInjector.enabled=false | nodeagent |
| NodeAgentClientDefault | + client.nodeAgent.enabled | nodeagent |
| InjectorDisabled | agentInjector.enabled=false | injector |
| InjectPolicy(p) | agentInjector.injectPolicy=p (parameterized) | injector |
| CertRegen(mode) | agentInjector.certificate.regenerate + accessMethod | injector |
| QuicNodePort | quicTunnel.* (NodePort) | quic |
| QuicRelay | quic + agentPortForward=false | quic |
| AuthEnforcing / AuthPermissive | security.authentication.* | auth |
| APIPort | telepresenceAPI.port | attach (restapi) |
| Usage | usage.{enabled,collectorAddress,insecure}, collectorAddress pointed at a local in-process fake collector | session |
| NamespaceSelector(sel) | namespaces / namespaceSelector (parameterized) | namespaces, install |
| WorkloadToggles(kind) | workloads.<kind>.enabled=false | install |
| ClientConfig(delta) | client.* served config (parameterized, small deltas) | routing, session |

Parameterized specs hash their parameters, so `InjectPolicy(OnDemand)` and
`InjectPolicy(WhenEnabled)` are distinct fixtures but still upgrades of the
same release.

## Areas and seed suites

Ordering below is the proposed porting order (milestone 3 waves). "Supersedes"
names files in `integration_test/`.

### Wave 1 — smoke, connect, intercept, attach

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| smoke/CLI | version/status/config text + JSON shape, no daemons | retired (M7) | Only place that asserts on text output shape |
| smoke/Connected | status/version/list once connected | retired (M7) | JSON asserts |
| connect/Lifecycle | connect, disconnect, quit, reconnect after API-server drop (requires sudo) | retired (M7) | |
| connect/Errors | bad kubeconfig, bad context, unmanaged namespace refusal | retired (M7) | |
| connect/Contexts | --context, kubeconfig extension (also/never-proxy, dns), exec-credential auth | retired (M7) | |
| connect/Multi | two managers, named connections, --use, inactivity takeover | retired (M7) | requires docker |
| attach/Modes | THE core table: {intercept, ingest, replace, wiretap} x {Deployment, ReplicaSet, StatefulSet, headless, no-service, no-volumes, Rollout} — attach, list, traffic round-trip, detach, uninstall | retired (M7) | Collapses overlap clusters A and J; argo variant keeps its CRD-install fixture + label |
| attach/Conflicts | ingest/intercept conflict matrix, repeat idempotence | retired (M7) | |
| intercept/Filters | header/path/combined filters **with traffic through the filter**, coexistence, TCP conflict | retired (M7) | Fixes the exit-0-only gap |
| intercept/Flags | pairwise matrix: port forms x mount x replace x docker-run x env-output | retired (M7) | rt.Matrix |
| intercept/Routing | multi-replica, multi-port services, --to-pod TCP/UDP, pod-IP bind, local shortcut | retired (M7) | |
| intercept/Concurrent | 3 simultaneous intercepts, duplicate-port conflict, colliding mounts | retired (M7) | |

### Wave 2 — install, injector, namespaces

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| install/Helm | install/upgrade/uninstall semantics, reuse/reset-values, failed-install tolerance, multi-namespace installs, collision detection | retired (M7) | |
| install/Setup | setup verb: apply, round-trip, streaming, non-admin handoff, validation | retired (M7) | |
| install/Golden | helm-template golden matrix over chart values | (new capability) | No cluster |
| injector/Webhook | annotation auto-inject, inject policies, reinvocation + LimitRange, cert regenerate (watch/mount), failed-inject resync, disabled injector, manual agent via genyaml | retired (M7) | Private namespaces per policy |
| namespaces/Selector | static list vs label selector, cross-namespace attach, mapped-namespaces | retired (M7) | |

### Wave 3 — nodeagent, quic, auth, session

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| nodeagent/Matrix | node-agent x {injector on/off, cluster-default, filters, multi-replica, replace-refusal, job reaping} | retired (M7) | One config family, 4 installs collapse to upgrades |
| quic/Transport | discovery, fallback, datagrams, outage/recovery, relay, coexistence | retired (M7) | slow label for outage tests |
| auth/Modes | enforcing/permissive, x509 toggles, identity-bound sessions, legacy-client compat | retired (M7) | |
| session/Lifecycle | cluster-served client config, log levels, gather-logs (deduped matrix), usage reporting, workload watch stream | retired (M7) | Drops the two byte-identical gather-logs tests |

### Wave 4 — dns, routing, mounts, docker

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| dns/Resolution | subdomain, svc domain, unqualified names, excludes/mappings, WPAD suppression | retired (M7) | |
| routing/Subnets | also/never-proxy, conflicting proxies, CIDR conflicts via veth (sudo), allow-conflicting, proxy-via, podCIDR strategies | retired (M7) | Drops the also-proxy duplicate |
| mounts/FUSE | read/write, read-only, ftp vs sshfs, large files (slow label), agent-content match, scale-to-zero survival | retired (M7) | |
| docker/Daemon | containerized daemon lifecycle, host+docker coexistence, cache ownership, subnet non-conflict, gather-logs from container | retired (M7) | requires docker |
| docker/Run | --docker-run handler matrix, REST API suite | retired (M7) | handler matrix landed in wave 5 (RunLifecycle, DockerConnRun) |
| docker/Compose | compose extension verbs | retired (M7) | landed in wave 5 (Compose, ComposeLifecycle) |
| session/Throughput | TUN throughput, repeated-connect stress | retired (M7) | stress label for the 90-subtest loop |

### Wave 5 — docker-run lifecycle, compose, state manifests, quic depth (m5)

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| docker/RunLifecycle | --docker-run handler + four-way teardown (SIGINT, detach, disconnect, quit) | retired (M7) | requires docker |
| docker/DockerConnRun | bare docker-run network/DNS join, external DNS, telemount volume | retired (M7) | requires docker |
| docker/Compose | one test per x-tele verb (connect, proxy, ingest, intercept, replace, wiretap, dns) | retired (M7) | requires docker |
| docker/ComposeLifecycle | named-volume down/-v semantics, default-network subnet non-conflict | retired (M7) | slow label |
| state/Apply | dry-run, create/reuse/unchanged, attachment drift, connection drift | retired (M7) | new area; owns its connection lifecycle |
| state/Delete | reverse-order teardown, absent, no-connection variants | retired (M7) | |
| state/Handler | handler command pid lifecycle across apply/delete | retired (M7) | not on windows |
| quic/Datagrams | RFC 9221 datagram carriage over UDP echo | retired (M7) | slow label; inline ExtraEnv spec |
| quic/ForwarderRestart | transport rides out forwarder pod deletion without fallback | retired (M7) | |
| quic/ManagerOutage | attachment survives manager scale-to-zero | retired (M7) | slow label |
| quic NodePort endpoint discovery | discovered `<nodeIP>:<nodePort>` endpoints (no externalHost) | retired (M7) | deferred: telepresenceio/telepresence#4227 |
| nodeagent quic transport | node-hosted agent transport is quic | retired (M7) | deferred: telepresenceio/telepresence#4227 |

Deliberately dropped (with reasons recorded here rather than silently):
`otel_test.go` (dead: env-gated stress never run in CI), `cli_test.go::Test_Help`
(already skipped), the duplicated gather-logs and also-proxy tests, and
`node_agent_test.go::Test_NodeAgentConfigDefault` (covered by unit tests per
its own comment). `h2c_intercept_test.go` folds into intercept/Routing with a
shared LocalService fixture.

## Overlap clusters collapsed

| Cluster (from audit) | Old occurrences | New home |
|---|---|---|
| A: basic intercept happy path | ~20 suites | attach/Modes table + `check.RoutedToLocal` helper; everywhere else attaches via helper without re-asserting basics |
| B: status output parsing | 12+ suites | smoke + typed `cli.Status` accessor |
| C: version reporting | 4 suites | smoke/CLI |
| D: connect/quit cycles | every suite + extras | Connection fixture (one per config, reused) |
| E: gather-logs | 8 tests, 3 suites | session/Lifecycle matrix, deduped |
| F: also/never-proxy + conflicts | 4 suites | routing/Subnets |
| G: node-agent | 4 suites + quic | nodeagent/Matrix |
| H: header filtering | 6+ suites | intercept/Filters owns it; others use one canonical filtered attach |
| I: mounts | 5 suites | mounts/FUSE |
| J: ingest mirror of intercept | 4 suites | attach/Modes axis |
| K: cluster-served config | 3 suites | session/Lifecycle |
| L: helm value handling | 4 suites + 68 installs | install/* + fixture engine |

## compat-core selection and RPC manifest

Labeled tests (drawn from the areas above):

| Test (new) | manager.Manager RPCs exercised |
|---|---|
| smoke/Connected status+version | Version, ArriveAsClient, Remain, Depart, GetClientConfig |
| connect/Lifecycle reconnect | ReconnectClient, WatchClusterInfo |
| attach/Modes (intercept x Deployment) | PrepareIntercept, CreateIntercept, GetIntercept, RemoveIntercept, WatchIntercepts(Delta), EnsureAgent, WatchAgents(Delta), WatchAgentPods*(Delta), GetAgentConfig, GetKnownWorkloadKinds |
| attach/Modes (ingest) | EnsureAgent, ReleaseAgent |
| intercept/Filters one header test | ReviewIntercept path via agent, Tunnel |
| session/Lifecycle watch + logs | WatchWorkloads, WatchSessionEvents, GetLogs, SetLogLevel, WatchLogLevel (agent side) |
| dns/Resolution one lookup test | LookupDNS (+ legacy Lookup fallback) |
| routing subnet smoke | WatchClusterInfo |
| quic/Transport fallback test | GetQuicTunnelEndpoint (+ fallback to Tunnel) |
| session/manager info | GetClusterInfo |
| install/Helm uninstall agents | UninstallAgents |

Exemption list for the manifest guard (proto methods not required from
compat-core): agent-only (ReviewIntercept*, ReportMetrics, WatchLogLevel,
GetQuicAgentCert), quicforwarder-only (WatchQuicBackends), dead surface
(GetTelepresenceAPI), enterprise/unused entries discovered during
implementation. (*ReviewIntercept is exercised indirectly whenever an agent
reviews an intercept; the guard counts indirect coverage when the manifest
says so.)

Version gates: compat-core tests may declare `compat.MinManager`/`MinClient`
so a direction against N-1 skips features the old side lacks (node-agent,
quic, setup) — mirroring today's `ClientIsVersion`/`ManagerIsVersion` usage
but centralized in `framework/compat`.

## Labels and platform constraints

Labels select what to run:

| Label | Meaning | Default in CI |
|---|---|---|
| compat-core | member of the bidirectional compatibility subset | on in compat job |
| slow | multi-minute by nature (large files, outage recovery) | on in nightly, off in PR shard 1 |
| stress | load/scale tests | nightly only |
| flaky-retry | opt-in single retry at runner level | on |

Platform gating is not label-based. A suite or test declares GOOS sets
(`rt.On`/`rt.NotOn`) and capability requirements (`rt.Requires(rt.Docker,
rt.Sudo, rt.FUSE, rt.Veth)`), and the runner self-skips with the unmet
constraint as the reason. Most tests must be runnable on all platforms in
dev mode; exemptions are the exception and are attached to the specific
tests that need them. CI runs on Linux only for now.
