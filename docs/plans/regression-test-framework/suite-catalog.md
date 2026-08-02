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
| smoke/CLI | version/status/config text + JSON shape, no daemons | cli_test.go, parts of connected_test.go, config_test.go | Only place that asserts on text output shape |
| smoke/Connected | status/version/list once connected | connected_test.go | JSON asserts |
| connect/Lifecycle | connect, disconnect, quit, reconnect after API-server drop (requires sudo) | not_connected_test.go, reconnect_session_test.go | |
| connect/Errors | bad kubeconfig, bad context, unmanaged namespace refusal | not_connected_test.go, helm_test.go (partly) | |
| connect/Contexts | --context, kubeconfig extension (also/never-proxy, dns), exec-credential auth | kubeconfig_extension_test.go, kubeauth_test.go, uhn_dns_test.go (flags part) | |
| connect/Multi | two managers, named connections, --use, inactivity takeover | multi_connect_test.go, inactive_client_test.go | requires docker |
| attach/Modes | THE core table: {intercept, ingest, replace, wiretap} x {Deployment, ReplicaSet, StatefulSet, headless, no-service, no-volumes, Rollout} — attach, list, traffic round-trip, detach, uninstall | workloads_test.go (10 tests), replace_test.go, wiretap_test.go, ingest_test.go (core), argo_rollouts_test.go, headless_test.go | Collapses overlap clusters A and J; argo variant keeps its CRD-install fixture + label |
| attach/Conflicts | ingest/intercept conflict matrix, repeat idempotence | ingest_test.go | |
| intercept/Filters | header/path/combined filters **with traffic through the filter**, coexistence, TCP conflict | http_intercepts_test.go | Fixes the exit-0-only gap |
| intercept/Flags | pairwise matrix: port forms x mount x replace x docker-run x env-output | intercept_flags_test.go, multiple_port_intercept_test.go, ignored_mounts_test.go (flags part), intercept_localhost_test.go | rt.Matrix |
| intercept/Routing | multi-replica, multi-port services, --to-pod TCP/UDP, pod-IP bind, local shortcut | multi_replica_intercept_test.go, multiport_test.go, to_pod_test.go, bind_to_podip_test.go, local_shortcut_test.go | |
| intercept/Concurrent | 3 simultaneous intercepts, duplicate-port conflict, colliding mounts | multiple_intercepts_test.go, mounts_test.go (collision) | |

### Wave 2 — install, injector, namespaces

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| install/Helm | install/upgrade/uninstall semantics, reuse/reset-values, failed-install tolerance, multi-namespace installs, collision detection | install_test.go, helm_test.go, uninstall_test.go, pod_cidr_test.go | |
| install/Setup | setup verb: apply, round-trip, streaming, non-admin handoff, validation | setup_test.go | |
| install/Golden | helm-template golden matrix over chart values | (new capability) | No cluster |
| injector/Webhook | annotation auto-inject, inject policies, reinvocation + LimitRange, cert regenerate (watch/mount), failed-inject resync, disabled injector, manual agent via genyaml | webhook_test.go, inject_policy_test.go, limitrange_test.go, injector_test.go, agent_injector_disabled_test.go, manual_agent_test.go, env_interpolate_test.go, tls_test.go (annotations) | Private namespaces per policy |
| namespaces/Selector | static list vs label selector, cross-namespace attach, mapped-namespaces | namespaces_test.go, gather_logs (mapped part), multiple_services_test.go (mapped part) | |

### Wave 3 — nodeagent, quic, auth, session

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| nodeagent/Matrix | node-agent x {injector on/off, cluster-default, filters, multi-replica, replace-refusal, job reaping} | node_agent_test.go, node_agent_multi_test.go, node_agent_no_injector_test.go, node_agent_cluster_default_test.go | One config family, 4 installs collapse to upgrades |
| quic/Transport | discovery, fallback, datagrams, outage/recovery, relay, coexistence | quic_test.go, quic_disabled_test.go | slow label for outage tests |
| auth/Modes | enforcing/permissive, x509 toggles, identity-bound sessions, legacy-client compat | manager_auth_test.go, compat_auth_test.go | |
| session/Lifecycle | cluster-served client config, log levels, gather-logs (deduped matrix), usage reporting, workload watch stream | cloud_config_test.go, loglevel_test.go, gather_logs_test.go, usage_reporting_test.go, workload_watch_test.go, list_watch_test.go, manager_grpc_test.go | Drops the two byte-identical gather-logs tests |

### Wave 4 — dns, routing, mounts, docker

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| dns/Resolution | subdomain, svc domain, unqualified names, excludes/mappings, WPAD suppression | subdomain_test.go, svcdomain_test.go, uhn_dns_test.go, wpad_test.go | |
| routing/Subnets | also/never-proxy, conflicting proxies, CIDR conflicts via veth (sudo), allow-conflicting, proxy-via, podCIDR strategies | also_proxy_test.go, cidr_conflict_test.go, proxy_via_test.go, pod_cidr_test.go (values part) | Drops the also-proxy duplicate |
| mounts/FUSE | read/write, read-only, ftp vs sshfs, large files (slow label), agent-content match, scale-to-zero survival | mounts_test.go, intercept_mount_test.go, podscaling_test.go, large_files_test.go | |
| docker/Daemon | containerized daemon lifecycle, host+docker coexistence, cache ownership, subnet non-conflict, gather-logs from container | docker_daemon_test.go | requires docker |
| docker/Run | --docker-run handler matrix, REST API suite | docker_run_test.go, restapi_test.go | handler matrix landed in wave 5 (RunLifecycle, DockerConnRun) |
| docker/Compose | compose extension verbs | compose_test.go | landed in wave 5 (Compose, ComposeLifecycle) |
| session/Throughput | TUN throughput, repeated-connect stress | multiple_services_test.go | stress label for the 90-subtest loop |

### Wave 5 — docker-run lifecycle, compose, state manifests, quic depth (m5)

| Area/Suite | Validates | Supersedes | Notes |
|---|---|---|---|
| docker/RunLifecycle | --docker-run handler + four-way teardown (SIGINT, detach, disconnect, quit) | docker_run_test.go (host-daemon matrix) | requires docker |
| docker/DockerConnRun | bare docker-run network/DNS join, external DNS, telemount volume | docker_run_test.go (dockerDaemonSuite tests) | requires docker |
| docker/Compose | one test per x-tele verb (connect, proxy, ingest, intercept, replace, wiretap, dns) | compose_test.go | requires docker |
| docker/ComposeLifecycle | named-volume down/-v semantics, default-network subnet non-conflict | compose_test.go | slow label |
| state/Apply | dry-run, create/reuse/unchanged, attachment drift, connection drift | state_manifest_test.go | new area; owns its connection lifecycle |
| state/Delete | reverse-order teardown, absent, no-connection variants | state_manifest_test.go | |
| state/Handler | handler command pid lifecycle across apply/delete | state_manifest_test.go | not on windows |
| quic/Datagrams | RFC 9221 datagram carriage over UDP echo | quic_test.go (Test_AUDPEchoDatagrams) | slow label; inline ExtraEnv spec |
| quic/ForwarderRestart | transport rides out forwarder pod deletion without fallback | quic_test.go (Test_ForwarderRestartSurvival) | |
| quic/ManagerOutage | attachment survives manager scale-to-zero | quic_test.go (Test_ZManagerOutageAttachmentSurvival) | slow label |
| quic NodePort endpoint discovery | discovered `<nodeIP>:<nodePort>` endpoints (no externalHost) | quic_test.go (Test_ZZDiscoveryNodePort) | deferred: telepresenceio/telepresence#4227 |
| nodeagent quic transport | node-hosted agent transport is quic | quic_test.go (Test_NodeAgentTransport) | deferred: telepresenceio/telepresence#4227 |

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
