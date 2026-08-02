# M7 parity ledger (pass 1)

Per-file verdicts for the 81 `integration_test/*_test.go` files, against the
suites in `regression_test/suites/`. Companion to `m7-spec.md`.

## Method

For each old file: read its test list, read what each test actually asserts,
then look for those assertions in the new suites (by suite doc comment, test
body, and grep for the flag/verb/field under test). The measure is
behavioural, not test counts — the port deliberately collapses twelve overlap
clusters, so a count comparison would argue for porting duplication back in.

Verdicts:

| Verdict | Meaning |
|---|---|
| superseded | every assertion has a named home in `regression_test/` |
| partial | the area is ported but named assertions have no home; listed in the gap list |
| dropped | deliberately not ported, reason recorded |
| unclaimed | no new suite names it; needs a decision |

Confidence: rows marked `read` were verified by reading both sides. Rows
marked `grep` were verified by locating (or failing to locate) the flag,
verb, or field under test in `regression_test/`. A `grep` row that says
"absent" is a strong negative — the token appears nowhere under
`regression_test/` — but a `grep` row that says "present" is weaker and
should be upgraded to `read` before its area retires.

## Summary

| | files | notes |
|---|---|---|
| superseded | 24 | ready to delete once their area's partials are closed |
| partial | 49 | see gap list |
| unclaimed | 5 | 2 are real coverage loss |
| dropped | 1 | otel_test.go; individual dropped tests inside kept files are listed separately |
| infrastructure | 2 | integration_test.go (entrypoint), single_service_test.go (scaffolding) |

81 total. `itest/` and `testdata/` are not test files and go with the package.

The headline: **`suite-catalog.md`'s supersession column is optimistic in
every area.** It was written before the suites existed and records intent.
Roughly half the claimed-superseded files have at least one assertion with no
home in the new suite. None of that is surprising for a port done in waves,
but it means no area can retire on the catalog's word alone.

Two areas were claimed and never built at all: `install/Setup` (7 tests) and
`session/Throughput` (5 tests). Two more are deferred by an open issue
(telepresenceio/telepresence#4227).

## attach

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| workloads_test.go | 10 | attach/Modes + attach/Matrix (deployment, statefulset, headless, no-service cells) | partial: ReplicaSet kind, no-volumes variant, PodDisruptionBudget re-intercept, `telepresence uninstall <wl>`, agent-count==replicas | read |
| replace_test.go | 1 | attach/Matrix replace/multiport | partial: old asserts BOTH service ports route local under replace; `runCell` checks the primary port only, and its extra-port check is gated to `verb == "intercept"` | read |
| wiretap_test.go | 2 | attach/Matrix wiretap cells | partial: Test_MultipleTapsOnOnePort (two wiretaps on one port) has no home | read |
| ingest_test.go | 11 | attach/Matrix ingest cells + attach/Conflicts | partial: IngestFTP, IngestProxyVia, IngestWithCommand, IngestWithContainerAndCommand, LeaveIngestWithoutContainer, IngestListFormat, IngestIngestConflict | read |
| argo_rollouts_test.go | 2 | — | partial: no Rollout kind exists. `argo` appears nowhere under `regression_test/`; the catalog's "argo variant keeps its CRD-install fixture + label" was never built | grep |
| headless_test.go | 1 | attach/Matrix headless cells | superseded | read |
| container_test.go | 2 | — | unclaimed: `--container` appears nowhere under `regression_test/` | grep |

`attach` is the biggest collapse and the least ready. Its ledger needs the
most scrutiny; it retires last.

## intercept

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| http_intercepts_test.go | 10 | intercept/Filters (Header, PathPrefix, Combined, Coexistence, TCPPortConflict) | partial: Test_BackwardCompatibility, Test_HTTPManySimultaneous, Test_HTTPManyClientsSimultaneous | read |
| intercept_flags_test.go | 1 | — | partial: Test_ContainerReplace is `--container` + replace; same gap as container_test.go | read |
| multiple_port_intercept_test.go | 3 | intercept/Routing Test_MultiPort | partial: MultiPortLocalConflict, MultiPortRemoteConflict | grep |
| multi_replica_intercept_test.go | 2 | intercept/Routing Test_MultiReplica (global) | partial: the personal/filtered all-replicas variant | read |
| local_shortcut_test.go | 3 | intercept/Routing Test_LocalShortcut | partial: 3 old variants (active traffic, disabled, not-global) collapse to 1 new test; confirm all three semantics are asserted | grep |
| multiport_test.go | 4 | — | partial: unnamed service ports, no container port, unnamed UDP+TCP, same container port — four service-shape edge cases with no home | grep |
| to_pod_test.go | 2 | — | partial: `--to-pod` appears nowhere under `regression_test/` | grep |
| bind_to_podip_test.go | 1 | — | partial: pod-IP bind appears nowhere | grep |
| intercept_localhost_test.go | 1 | — | partial: custom localhost address has no home | grep |
| multiple_intercepts_test.go | 2 | intercept/Filters Test_TCPPortConflict (conflict half only) | partial: **intercept/Concurrent was never built** — no file, no 3-simultaneous-intercept test | grep |
| h2c_intercept_test.go | 3 | intercept/Routing Test_H2C | superseded | read |
| ignored_mounts_test.go | 1 | mounts/Ignored, mounts/NotIgnored | superseded | read |
| intercept_env_test.go | 1 | — | unclaimed: `--env-excludes` filtering. intercept/Flags does `--env-file`/`--env-json` round-trip but not exclusion | read |

## smoke and connect

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| cli_test.go | 6 | smoke/CLI (Version, StatusNotRunning, ConfigView) | partial: Test_VersionWithInvalidKubeContext. Test_Help already skipped upstream — drop | read |
| connected_test.go | 4 | smoke/Connected | superseded | read |
| config_test.go | 1 | smoke/Test_ConfigView | partial: old asserts an *empty* config file is tolerated; new only asserts non-empty output | read |
| not_connected_test.go | 6 | connect/Errors, connect/Lifecycle, smoke/StatusNotRunning | partial: Test_ConnectWithCommand (`connect -- <cmd>`), Test_CreateAndRunIndividualPod | read |
| reconnect_session_test.go | 1 | connect/Test_ReconnectAfterApiServerDrop | superseded | read |
| kubeconfig_extension_test.go | 5 | connect/Contexts (AlsoNeverProxy, DNSIncludeSuffixes) | partial: Test_APIServerIsProxied, Test_ConflictingProxies, Test_AlsoNeverProxyDocker | read |
| kubeauth_test.go | 1 | — | partial: exec-credential kubeconfig auth has no home; the catalog claimed connect/Contexts | grep |
| uhn_dns_test.go | 2 | dns/Excludes, dns/Mappings | **retired** | read |
| multi_connect_test.go | 3 | connect/Test_TwoDockerConnections | partial: named connections and same-namespace multi-connect | grep |
| inactive_client_test.go | 2 | — | partial: inactivity takeover has no home; `inactiv` appears nowhere under `regression_test/` | grep |

## install

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| install_test.go | 8 | install/Lifecycle, install/Values, golden/ | partial: Test_HelmSubChart, Test_No_Upgrade, Test_findTrafficManager_differentNamespace_present | grep |
| helm_test.go | 7 | install/Lifecycle Test_CollidingInstall, connect/Test_UnmanagedNamespace | partial: webhook injects/doesn't-inject per managed namespace, Test_HelmMultipleInstalls, Test_HelmInstallReportsPodFailureReason | grep |
| uninstall_test.go | 1 | install/Test_InstallUninstallReinstall | superseded | read |
| pod_cidr_test.go | 1 | install/Test_ExplicitCIDRsReachStatus | partial: only the explicit-CIDR strategy; the other podCIDR strategies are unasserted | read |
| setup_test.go | 7 | — | **partial: `install/Setup` does not exist.** Deferred with the `setup` verb to 2.32.0 | grep |
| limitrange_test.go | 1 | injector/Test_AgentGetsDefaultedResources | superseded | read |

## injector

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| webhook_test.go | 2 | injector/Test_AnnotationDrivesInjection | partial: Test_AgentImageFromConfig | grep |
| inject_policy_test.go | 3 | injector/Test_Policies | partial: Test_MultiOnDemandInjectOnInstall, Test_MultiOnDemandInjectOnApply | grep |
| injector_test.go | 3 | injector/Test_AccessMethods (both secret-upgrade tests) | partial: Test_InterceptOperationRestoredAfterFailingInject (failed-inject resync) | read |
| agent_injector_disabled_test.go | 3 | injector/Test_InterceptFailsVersionAndListStillWork, Test_HandBuiltAgentIntercepts | superseded | read |
| manual_agent_test.go | 1 | injector/Test_HandBuiltAgentIntercepts | superseded | read |
| env_interpolate_test.go | 1 | — | partial: `interpolat` appears nowhere under `regression_test/` | grep |
| tls_test.go | 1 | — | partial: TLS *annotations* on a workload; the `tls`/`TLS` hits under `regression_test/` are cert-regen and h2c, not annotations | grep |
| workload_configuration_test.go | 4 | — | unclaimed: `telepresence.io/enabled=false` across workload kinds | read |

## namespaces

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| namespaces_test.go | 5 | namespaces/StaticList, MappedNamespaces, SelectorSemantics | partial: Test_NamespacesClusterWide, Test_MultiNamespaceHTTPIntercepts, Test_MultiNamespaceIngests | grep |
| multiple_services_test.go | 5 | session/compat_sim Test_ListAndIntercept (the `Test_List` half) | partial: **`session/Throughput` was never built** — LargeRequest, RepeatedConnect, ProxiesOutboundTraffic, AllowsUnmanagedMappedNamespace | grep |

## nodeagent

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| node_agent_test.go | 7 | nodeagent/Modes (Intercept, HTTPFilteredIntercept, Ingest, Wiretap), nodeagent/Test_ReplaceRefused | partial: Test_NodeAgentHTTPFilteredWiretap. Test_NodeAgentConfigDefault dropped per catalog (unit-covered) | read |
| node_agent_multi_test.go | 4 | nodeagent/Multi (GlobalIntercept, FilteredIntercept) | partial: Test_NodeAgentSharedJobHTTPFiltered, Test_NodeAgentSharedJobGlobal (shared-Job semantics) | read |
| node_agent_no_injector_test.go | 5 | nodeagent/NoInjector (Intercept, SidecarFlagRejected), nodeagent/ClientDefault | partial: the ingest variant, and Test_ZUninstallReapsJobs (Job count reaches 0 on *detach* is asserted; on *uninstall* is not) | read |
| node_agent_cluster_default_test.go | 2 | nodeagent/ClientDefault (PlainInterceptUsesNodeAgent, FlagOverridesClusterDefault) | superseded | read |

## quic

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| quic_test.go | 10 | quic/Enabled, Fallback, Relay, Datagrams, Outage, ForwarderRestart, ManagerOutage | partial: Test_VPNOnlyTransport, Test_TrafficAgentCoexistence. Test_ZZDiscoveryNodePort and Test_NodeAgentTransport are deferred by telepresenceio/telepresence#4227 and must not be deleted until it closes | read |
| quic_disabled_test.go | 2 | quic/Disabled (GRPCTransport, InterceptRoundTrips) | superseded | read |

## auth

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| manager_auth_test.go | 5 | auth/Enforcing (GrantedIdentity, UnauthorizedIdentityDeniedAtConnect, SessionRequiresAndBindsVerifiedIdentity) | partial: **the suite's own doc comment declares the gap** — "a cert-only client authenticating over the manager's dedicated x509 auth port, and the security.authentication.x509.enabled=false rejection … which this framework has not ported". Also: old denies an *intercept*, new denies at *connect* | read |
| compat_auth_test.go | 2 | auth/Permissive Test_ConnectAndInterceptWork | partial: Test_EnforcingRejectsLegacyClient | read |

## session

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| cloud_config_test.go | 5 | session/ClientConfig Test_ServedConfigReflectsOnConnect | partial: five old tests collapse to one. CloudAgentArrival, and both cloud log-level tests, have no home; the new test's own comment says it ports "cloud_config_test.go's core" | read |
| loglevel_test.go | 1 | — | partial: root-daemon log level has no home | grep |
| gather_logs_test.go | 7 | session/GatherLogs Test_Matrix | superseded (catalog's dedup of the two byte-identical tests is legitimate) | read |
| usage_reporting_test.go | 1 | session/Test_ReportedFromClientAndManager | superseded | read |
| workload_watch_test.go | 1 | session/Test_ManagerWatchSeesLifecycle | superseded | read |
| list_watch_test.go | 1 | session/Test_ListJSONStreamSeesNewWorkload | superseded | read |
| manager_grpc_test.go | 1 | session/Test_ServiceSubnetMatchesStatus | superseded | read |

## dns and routing

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| subdomain_test.go | 1 | dns/Test_HeadlessPodSubdomain | **retired** | read |
| svcdomain_test.go | 1 | dns/Test_ServiceNameForms | **retired** | read |
| wpad_test.go | 1 | dns/Test_NotForwarded | **retired** | read |
| also_proxy_test.go | 1 | connect/Test_AlsoNeverProxy, routing/Test_SubCIDRExcludedFromRoutedSubnets | superseded (the catalog's "drops the also-proxy duplicate" holds) | read |
| cidr_conflict_test.go | 6 | routing/Test_SubnetConflict | partial: auto-resolution, auto-avoidance, the cloud- and client-side disables, allow-conflict, and local-DNS-stays-reachable — five of six have no home | grep |
| proxy_via_test.go | 5 | routing/Test_AllSubnetsRouteThroughWorkload, Test_SubCIDRExcludedFromRoutedSubnets | partial: Test_ProxyViaLoopBack, Test_ProxyViaAllAndMounts (the suite doc comment already says the mounts variant "is left to the mounts area", where it does not exist) | read |
| udp_test.go | 1 | — | unclaimed: plain UDP echo through the TUN. quic/Datagrams covers UDP over QUIC datagrams, a different path | read |

## mounts

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| mounts_test.go | 3 | mounts/Content (read side), intercept/Concurrent (colliding — never built) | partial: Test_MountWrite is an **explicit, documented skip** in `mounts/content.go` (needs a PVC-backed writable volume); Test_MountReadOnly and Test_CollidingMounts have no home | read |
| intercept_mount_test.go | 4 | intercept/Test_DetailedJSON | partial: Test_InterceptMount, Test_InterceptMountRelative, Test_NoInterceptorResponse | grep |
| podscaling_test.go | 2 | mounts/Test_MountSurvivesPodScaling | partial: Test_StopInterceptedPodOfMany | grep |
| large_files_test.go | 2 | — | partial: `LargeFile` appears nowhere under `regression_test/`; the catalog claimed mounts/FUSE with a `slow` label | grep |

## docker and state

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| docker_daemon_test.go | 8 | docker/Coexist, CacheFiles, ConnRun, docker/Test_DefaultNetworkNoSubnetConflict | partial: confirm the `alsoProxy32`, `singleNameLookup`, and `GatherLogsTrafficManager` cells landed | grep |
| docker_run_test.go | 5 | docker/RunLifecycle, docker/ConnRun | superseded | grep |
| restapi_test.go | 4 | docker/RestAPI (Test_ConsumeHere and its neighbours) | partial: the four global/filtered x consume/info cells collapse to fewer new tests; confirm the filtered-info path is asserted | grep |
| compose_test.go | 9 | docker/Compose (7 verbs), docker/ComposeLifecycle (2) | superseded | read |
| state_manifest_test.go | 9 | state/Apply, state/Delete, state/Handler | **retired** — the only 1:1 area in the ledger | read |

## dropped

| old file | tests | reason |
|---|---|---|
| otel_test.go | 3 | env-gated stress, never run in CI (catalog) |
| cli_test.go::Test_Help | 1 | already skipped upstream |
| gather_logs (2 of 7) | 2 | byte-identical duplicates |
| also_proxy duplicate | 1 | duplicate of the kubeconfig-extension assertion |
| node_agent_test.go::Test_NodeAgentConfigDefault | 1 | unit-covered, per its own comment |

## unclaimed

| old file | tests | decision needed |
|---|---|---|
| integration_test.go | 1 | entrypoint; disappears with the package |
| single_service_test.go | 0 | scaffolding only |
| container_test.go | 2 | **port** — `--container` is real, unported coverage |
| workload_configuration_test.go | 4 | **port** — `telepresence.io/enabled=false` is real, unported coverage |
| intercept_env_test.go | 1 | port `--env-excludes` into intercept/Flags |
| udp_test.go | 1 | port into routing, or record why quic/Datagrams suffices |
| istio_test.go | 2 | port behind `rt.Requires`, or drop. Self-skips without an Istio install, so it has probably never run in CI |

## Gap list

Ordered by what blocks the most retirement. Each item is a new test (or
matrix axis) in `regression_test/`, not a change to the old suite.

**Blocks `attach` (the largest area):**

1. ReplicaSet workload template + matrix kind (2 old cells)
2. Rollout kind behind an Argo CRD fixture (2 old tests) — or a recorded drop
3. `--container` attach, covering container_test.go and intercept_flags_test.go
4. `telepresence uninstall <workload>` after detach — asserted by all 10 workloads_test.go tests, absent everywhere
5. PodDisruptionBudget re-intercept
6. replace on a multiport workload asserts *both* ports route local
7. multiple wiretaps on one port
8. the ingest extras: FTP, proxy-via, `--command`, container+command, detach-without-container, list format

**Blocks `intercept`:**

9. intercept/Concurrent (never built): 3 simultaneous intercepts + colliding mounts
10. `--to-pod` TCP and UDP
11. pod-IP bind
12. custom localhost address
13. the four multiport service-shape edge cases
14. `--env-excludes`

**Blocks `install` / `injector`:**

15. install/Setup (never built, 7 tests) — gated on the `setup` verb shipping in 2.32.0
16. webhook injection scoped by managed namespace
17. failed-inject resync
18. agent image from config, env prefix interpolation, TLS annotations
19. `telepresence.io/enabled=false` across workload kinds

**Blocks `routing` / `mounts` / `session`:**

20. CIDR auto-conflict resolution and its two disables, allow-conflict, local-DNS reachability
21. proxy-via loopback, proxy-via + mounts
22. mount write round-trip (needs a PVC fixture), read-only mount, colliding mounts
23. large-file transfer under both FUSE backends (`slow`)
24. session/Throughput (never built): large request, repeated connect, outbound proxying
25. cloud-served agent arrival and log levels; root-daemon log level

**Blocks `auth` / `nodeagent` / `quic`:**

26. x509 cert-only client and the `x509.enabled=false` rejection (declared unported in the suite's own doc)
27. enforcing rejects a legacy client
28. node-agent filtered wiretap, shared-Job semantics, ingest without injector, Job reaping on uninstall
29. quic VPN-only transport, traffic-agent coexistence
30. quic NodePort discovery + node-agent transport — blocked on telepresenceio/telepresence#4227

## Retirement readiness

| area | ready? | blocking |
|---|---|---|
| dns | **retired** | — |
| state | **retired** | — |
| docker | near | 3 grep-confidence rows to upgrade to read |
| quic | near | 2 tests + issue #4227 |
| session | no | Throughput never built; cloud-config 5:1 collapse |
| nodeagent | no | 4 assertions |
| auth | no | x509 path declared unported |
| namespaces | no | cluster-wide + multi-namespace |
| injector | no | 5 assertions |
| install | no | Setup never built |
| intercept | no | 6 assertions incl. a whole unbuilt suite |
| attach | no | 8 assertions; retires last by design |

`dns` and `state` retired first, as the two areas where the parity argument
is not in dispute. That commit establishes the mechanics every later area
follows: delete the old files and any testdata only they used, move the
catalog's supersession rows here, repoint every provenance comment that
cited a deleted file, and confirm both packages still build.

Retiring those five files also removed
`integration_test/testdata/k8s/echo-w-subdomain.yaml`, which only
subdomain_test.go used.
