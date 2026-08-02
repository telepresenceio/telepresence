# M7 parity ledger (pass 2)

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

Confidence: every row is now `read` — both sides were read. The first pass
marked some rows `grep` (the flag, verb, or field was located, or not, by
search alone); upgrading those changed verdicts, so search alone is not
enough to retire on. `docker_run_test.go` went from superseded to partial,
and the docker area from "near ready" to blocked.

Pass 2 (2026-08-03) rechecked the pass-1 gap list against the reviewed
milestone specs (m3 waves 1-4, m5, m7's own exit criteria). Three row
corrections came out of it — `pod_cidr_test.go` to superseded, one
`http_intercepts_test.go` gap removed, `uninstall_test.go` to partial —
and the gap list was rebuilt: a delta that a reviewed spec deliberately
scoped out is a decision already made, not pending work. The rebuilt list
separates mandated work (m7 exit criteria: two coverages, three verdicts)
from promised-but-missed items (six, all small) and from recorded drops.

## Summary

| | files | notes |
|---|---|---|
| superseded | 24 | ready to delete once their area's partials are closed (pass 2 moved pod_cidr in, uninstall out; net unchanged) |
| partial | 49 | see gap list; most partials resolve by recording a drop, not by writing a test |
| unclaimed | 5 | 2 are real coverage loss |
| dropped | 1 | otel_test.go; individual dropped tests inside kept files are listed separately |
| infrastructure | 2 | integration_test.go (entrypoint), single_service_test.go (scaffolding) |

81 total. `itest/` and `testdata/` are not test files and go with the package.

The headline: **`suite-catalog.md`'s supersession column is optimistic in
every area.** It was written before the suites existed and records intent.
Roughly half the claimed-superseded files have at least one assertion with no
home in the new suite. None of that is surprising for a port done in waves,
but it means no area can retire on the catalog's word alone.

The pass-2 counter-headline: **most of those deltas are not pending work.**
The reviewed wave specs narrowed the catalog deliberately, and the m7 parity
rule accepts a recorded drop as full parity. After sorting, the work that
remains is the two m7-mandated coverages, six small promised-but-missed
items (one of them install/Setup, the only multi-test miss), and four
decisions — everything else retires by recording the disposition.

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
| argo_rollouts_test.go | 2 | — | partial: intercepting a Rollout with workloads.argoRollouts.enabled=true and the Argo CRDs installed, plus listing the underlying ReplicaSet when rollout support is off. `argo` appears nowhere under regression_test; the catalog's "argo variant keeps its CRD-install fixture + label" was never built | read |
| headless_test.go | 1 | attach/Matrix headless cells | superseded | read |
| container_test.go | 2 | — | unclaimed: intercepting a named container in a multi-container pod (`--container`), and `--container` combined with replace. The flag appears nowhere under regression_test | read |

`attach` is the biggest collapse and the least ready. Its ledger needs the
most scrutiny; it retires last.

## intercept

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| http_intercepts_test.go | 10 | intercept/Filters (Header, PathPrefix, Combined, Coexistence, TCPPortConflict); Test_BackwardCompatibility is a plain unfiltered TCP intercept — overlap cluster A, any plain attach/Modes cell | partial: Test_HTTPManySimultaneous, Test_HTTPManyClientsSimultaneous (concurrency; see the Concurrent disposition) | read |
| intercept_flags_test.go | 1 | — | partial: Test_ContainerReplace is `--container` + replace; same gap as container_test.go | read |
| multiple_port_intercept_test.go | 3 | intercept/Routing Test_MultiPort (one named port intercepted, the other left on the cluster) | partial: MultiPortIntercept intercepts *two* ports on one workload with two --port flags and asserts the mapping in status -- the new test uses a single --port; MultiPortLocalConflict ("N is already in use by intercept X"); MultiPortRemoteConflict ("one intercept has no filters") | read |
| multi_replica_intercept_test.go | 2 | intercept/Routing Test_MultiReplica (global) | partial: the personal/filtered all-replicas variant | read |
| local_shortcut_test.go | 3 | intercept/Routing Test_LocalShortcut (shortcut on+global, and off) | partial: Test_ShortcutNotGlobal -- localShortcut=true with localShortcutIsGlobal=false, where a filtered intercept is exempted from the shortcut. The suite's two config deltas set both fields together, so the middle case cannot occur. Old also asserts the shortcut applies to the pod address as well as the service address | read |
| multiport_test.go | 4 | — | partial: four service-shape edge cases with no home -- multiple *unnamed* service ports, a workload with no container port, UDP and TCP services on the same port, and two services on one container port. workloads.Template only renders named ports (ExtraPorts []NamedPort), so each needs a new template, not just a new test | read |
| to_pod_test.go | 2 | — | partial: `--to-pod` TCP and UDP port forwarding; the flag appears nowhere under regression_test | read |
| bind_to_podip_test.go | 1 | — | partial: the intercept binding to the pod IP rather than loopback; no equivalent under regression_test | read |
| intercept_localhost_test.go | 1 | — | partial: a custom localhost address for the intercept handler, including that plain localhost does *not* answer; no equivalent under regression_test | read |
| multiple_intercepts_test.go | 2 | — | partial: **intercept/Concurrent was never built**. Test_Intercepts drives N simultaneous intercepts with concurrent traffic to each; Test_ReportsPortConflict asserts the *local* port clash ("port 127.0.0.1:N is already in use by intercept X"), which is a different path from intercept/Filters' Test_TCPPortConflict (a manager-side global-intercept conflict) | read |
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
| kubeauth_test.go | 1 | — | partial: an exec-credential kubeconfig, driven over both a host and a docker connection, asserting the authenticator ran (connector.log for host, kubeauth.log for docker, where it runs as its own host process). No exec-credential plumbing exists in regression_test | read |
| uhn_dns_test.go | 2 | dns/Excludes, dns/Mappings | **retired** | read |
| multi_connect_test.go | 3 | connect/ConnectMulti Test_TwoDockerConnections -- two *named* docker connections to different namespaces, list isolation both ways, concurrent intercepts, quit-one-keeps-the-other | partial: Test_MultipleConnect_sameNamespace, two connections into the *same* namespace | read |
| inactive_client_test.go | 2 | — | partial: taking over a connection whose owning client has gone inactive, and the sleeping-client variant; `inactiv` appears nowhere under regression_test | read |

## install

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| install_test.go | 8 | install/HelmLifecycle (uninstall, failed-install tolerance, absence), install/HelmValues Test_ReuseThenResetValues (UpgradeRetainsValues), golden/ (HelmTemplateInstall) | partial: Test_HelmSubChart, Test_No_Upgrade, Test_findTrafficManager_differentNamespace_present | read |
| helm_test.go | 7 | install/HelmLifecycle Test_CollidingInstall, connect/Test_UnmanagedNamespace, and the attach area for the managed-namespace intercept | partial: HelmWebhookInjectsInManagedNamespace and its doesn't-inject twin (injection scoped by managed namespace), Test_HelmMultipleInstalls, Test_HelmInstallReportsPodFailureReason (BrokenInstallThenCorrected asserts the failure, not the reported pod reason) | read |
| uninstall_test.go | 1 | install/Test_InstallUninstallReinstall (release install/uninstall/reinstall, presence/absence) | partial: the old test also asserts the webhook-injected agent is scrubbed from workload pods on `helm uninstall`; the new lifecycle test asserts only the manager Deployment. Folds into the uninstall-verb disposition | read |
| pod_cidr_test.go | 1 | install/Test_ExplicitCIDRsReachStatus | superseded — the old file's single test drives exactly one strategy (environment + explicit podCIDRs); its `tests` table has one entry, so no other strategy was ever asserted. Pass 1 read the catalog's "podCIDR strategies" plural as old coverage; it was aspiration | read |
| setup_test.go | 7 | — | **partial: install/Setup does not exist.** Apply idempotence, round-trip, streaming output, upgrade merge, non-admin handoff, client-RBAC input round-trip, and validation. Pass 1 called this "deferred with the `setup` verb to 2.32.0"; wrong — the verb landed 2026-07-19, before wave 2 was built (2026-07-30). A wave-2 promise that was not delivered and has no recorded reason | read |
| limitrange_test.go | 1 | injector/Test_AgentGetsDefaultedResources | superseded | read |

## injector

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| webhook_test.go | 2 | injector/AutoInject Test_AnnotationDrivesInjection | partial: Test_AgentImageFromConfig. The only `--agent-image` use in regression_test is manual_agent.go's genyaml invocation, which is the CLI flag, not the cluster config driving the injected image | read |
| inject_policy_test.go | 3 | injector/InjectPolicies Test_Policies (OnDemand and WhenEnabled semantics, annotated vs plain) | partial: Test_MultiOnDemandInjectOnInstall and Test_MultiOnDemandInjectOnApply -- several workloads under OnDemand, at install time and at apply time | read |
| injector_test.go | 3 | injector/Test_AccessMethods (both secret-upgrade tests) | partial: Test_InterceptOperationRestoredAfterFailingInject (failed-inject resync) | read |
| agent_injector_disabled_test.go | 3 | injector/Test_InterceptFailsVersionAndListStillWork, Test_HandBuiltAgentIntercepts | superseded | read |
| manual_agent_test.go | 1 | injector/Test_HandBuiltAgentIntercepts | superseded | read |
| env_interpolate_test.go | 1 | — | partial: prefixed env-var interpolation in the injected agent's environment; `interpolat` appears nowhere under regression_test | read |
| tls_test.go | 1 | — | partial: the workload's TLS annotations driving the agent's TLS config. The `tls`/`TLS` hits under regression_test are webhook cert-regen and h2c, a different surface | read |
| workload_configuration_test.go | 4 | — | unclaimed: `telepresence.io/enabled=false` across workload kinds | read |

## namespaces

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| namespaces_test.go | 5 | namespaces/StaticList (Static), SelectorSemantics Test_LabelToggle (Dynamic), MappedNamespaces | partial: Test_NamespacesClusterWide (a manager managing every namespace), Test_MultiNamespaceHTTPIntercepts and Test_MultiNamespaceIngests (attaching across two namespaces at once) | read |
| multiple_services_test.go | 5 | session/compat_sim Test_ListAndIntercept (the Test_List half) | partial: **session/Throughput was never built** -- LargeRequest, RepeatedConnect, ProxiesOutboundTraffic, and AllowsUnmanagedMappedNamespace (MappedNamespaces proves scoping, not that an *unmanaged* namespace may be mapped) | read |

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
| loglevel_test.go | 1 | — | partial: asserts the configured level actually reaches rootd's logger (` DEBUG +rootd/server` lines in daemon.log). Nothing in regression_test inspects daemon log content | read |
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
| cidr_conflict_test.go | 6 | routing/Test_SubnetConflict subtests (auto-resolve to the virtual subnet; --allow-conflicting-subnets) | partial: 4 of 6 have no home -- the cluster-served and client-side `autoResolveConflicts=false` refusals, the config-driven AllowConflicting route check (`ip route get` via brm), and local-DNS-stays-reachable inside a conflicting subnet | read |
| proxy_via_test.go | 5 | routing/Test_AllSubnetsRouteThroughWorkload, Test_SubCIDRExcludedFromRoutedSubnets | partial: Test_ProxyViaLoopBack, Test_ProxyViaAllAndMounts (the suite doc comment already says the mounts variant "is left to the mounts area", where it does not exist) | read |
| udp_test.go | 1 | — | unclaimed: plain UDP echo through the TUN. quic/Datagrams covers UDP over QUIC datagrams, a different path | read |

## mounts

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| mounts_test.go | 3 | mounts/Content (read side), intercept/Concurrent (colliding — never built) | partial: Test_MountWrite is an **explicit, documented skip** in `mounts/content.go` (needs a PVC-backed writable volume); Test_MountReadOnly and Test_CollidingMounts have no home | read |
| intercept_mount_test.go | 4 | intercept/Test_DetailedJSON, mounts/Content (which reads content under the mount root, subsuming Test_InterceptMount's "mount point is a directory") | partial: Test_InterceptMountRelative (a relative --mount path), Test_NoInterceptorResponse | read |
| podscaling_test.go | 2 | mounts/Podscaling Test_MountSurvivesPodScaling (scale to zero and back; the suite doc names Test_RestartInterceptedPod) | partial: Test_StopInterceptedPodOfMany -- killing one pod of several, where the attachment must move to a surviving pod | read |
| large_files_test.go | 2 | mounts/ftp_vs_fuse.go covers the two backends (intercept.useFtp true/false content check — pass 1 missed it) | partial: only the large-file transfer itself is unported; nothing under regression_test moves a large file | read |

## docker and state

| old file | tests | covered by | verdict | conf |
|---|---|---|---|---|
| docker_daemon_test.go | 8 | docker/Coexist (hostDaemonNoConflict, daemonHostNotConflict), docker/CacheFiles (cacheFiles) | partial: `status`'s daemon-name shape (`<ns>-cn`, Connected); `alsoProxy32` (`--docker --also-proxy` + STREAM_INFO in connector.log); `singleNameLookup` (`connect --docker -- <cmd>`, then no daemon left running); `GatherLogsTrafficManager` (gather-logs over a docker connection; session/GatherLogs is host-only); `networkNoSubnetConflict` (three teleroute networks vs cluster CIDRs -- ComposeLifecycle checks a compose *default* network, a different object) | read |
| docker_run_test.go | 5 | docker/DockerRun + docker/RunLifecycle (HostDaemon, incl. the four-way teardown), docker/DockerConnRun (DockerRunCommand, ExternalDNS, VolumePresent) | partial: Test_DockerRun_DockerDaemon -- the `--docker-run` traffic round-trip and its four-way teardown over a *docker* connection. DockerConnRun covers that connection's network, DNS, and mounts, never its traffic or teardown | read |
| restapi_test.go | 4 | docker/RestAPI Test_ConsumeHere (the filtered consume-here axis, plus an unintercepted baseline) | partial: the two global (unfiltered) cells, the whole `/intercept-info` endpoint, and the restapi.HeaderCallerInterceptID axis -- the new suite's own doc says its probes never send that header | read |
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
| container_test.go | 2 | **port** — `--container` is real, unported coverage (m7 exit criterion) |
| workload_configuration_test.go | 4 | **port** — `telepresence.io/enabled=false` is real, unported coverage (m7 exit criterion) |
| intercept_env_test.go | 1 | port: `--env-excludes` is a few lines inside intercept/Flags' existing env round-trip |
| udp_test.go | 1 | port: plain UDP through the TUN over grpc transport is otherwise uncovered (quic/Datagrams is a different path), and `workloads.UDPEcho` already exists (built for m5), so this is ~30 lines in routing |
| istio_test.go | 2 | drop: self-skips without an Istio install with DNS capture, so it has likely never run in CI; record that as the reason |

## Gap list (pass 2)

Pass 1 framed ~35 items as new tests. That framing was wrong in two ways.
First, m7-spec's exit criteria mandate exactly two new coverages
(`--container`, `telepresence.io/enabled=false`) plus recorded *verdicts*
for the three undecided unclaimed files — and its ledger rule accepts
"explicitly dropped with a recorded reason" as full parity. Second, the
reviewed wave specs deliberately narrowed the catalog (the catalog records
pre-implementation intent; the wave specs record what review actually
approved), so a delta the wave spec scoped out is a decision already made.
Pass 2 sorts every pass-1 item into the four buckets below.

### Mandated by m7-spec exit criteria

M1. `--container` attach (container_test.go + intercept_flags_test.go's
    ContainerReplace) — one test or axis; the flag appears nowhere under
    regression_test
M2. `telepresence.io/enabled=false` across workload kinds
    (workload_configuration_test.go) — natural home injector/
M3. Recorded verdicts for udp_test.go (recommend: port, ~30 lines,
    template exists), intercept_env_test.go (recommend: port, a few lines
    in intercept/Flags), istio_test.go (recommend: drop, never ran in CI)

### Promised by a reviewed spec, not delivered, no recorded skip

Each is one test or axis in an existing suite. Together they are small.

P1. `--to-pod` TCP — wave-1 promised it in intercept/Flags; the flag is
    absent from every suite (the framework's ToPod fields serve compose
    and state specs, not the intercept flag)
P2. exec-credential kubeauth (kubeauth_test.go) — wave-1 explicitly moved
    it to wave 4 ("moved to wave 4 docker area"); wave 4 never picked it
    up. The only coverage of the kubeauth daemon path
P3. node-agent HTTP-filtered wiretap — wave-3 promised "+ HTTP-filtered
    variants"; filtered intercept and plain wiretap exist, their
    combination does not
P4. REST API `/intercept-info` — wave-4 promised it alongside
    /consume-here; the suite delivered consume-here only. The global cells
    and header axis can ride along or be dropped (restapi.go's doc already
    explains why the header never drives the current probes)
P5. one bulk-transfer test — wave-4's trimmed Throughput promised "one
    bulk-transfer check and N=30 connect cycles". Recommend the bulk check
    only: connect-cycle stability is exercised by the framework's own
    fixture churn every run (record that as RepeatedConnect's verdict),
    and outbound proxying is asserted implicitly by every routing/dns test
P6. install/Setup (setup_test.go, 7 tests) — wave-2 promised it; the verb
    existed before wave 2 was built. The largest single miss. Alternative:
    retire setup_test.go last, the same holding pattern as quic #4227

### Scope decisions already made in reviewed specs — record as drops

No code; recording these dispositions here is what the m7 parity rule
requires. Spec citations inline.

- attach cell contract: wave-1 built {intercept, ingest, replace, wiretap}
  x {deployment, statefulset, headless, no-service, multiport}. ReplicaSet
  cells, the no-volumes variant, PodDisruptionBudget re-intercept, the
  replace both-ports assertion, multiple wiretaps on one port, and the
  agent-count==replicas assertion were not in the reviewed table. (M2's
  enabled=false work touches ReplicaSet handling anyway; fold if
  convenient)
- ingest extras (FTP, proxy-via, `--command`, container+command,
  detach-without-container, list format): wave-1 promised the ingest core
  plus the conflict matrix, nothing more
- intercept/Concurrent: in the catalog, absent from the reviewed wave-1
  spec; connect/ConnectMulti already runs concurrent intercepts (one per
  connection). Test_HTTPManySimultaneous / ManyClients and colliding
  mounts fold in here. The local-port-clash error message is the one
  piece with no cousin; cheap to add to Filters if wanted
- pod-IP bind: wave-1 pre-authorized the skip ("skip if too deep, note
  it"); this entry is the note
- custom localhost address, the four multiport service-shape edge cases
  (each needs a new workload template): not in the reviewed wave-1 spec
- connect area extras (empty config file tolerated,
  VersionWithInvalidKubeContext, `connect -- <cmd>`,
  CreateAndRunIndividualPod, APIServerIsProxied, ConflictingProxies,
  AlsoNeverProxyDocker, same-namespace double connect): none promised by
  wave-1's connect bullets
- helm extras (sub-chart, No_Upgrade, findTrafficManager variants,
  multiple installs, webhook-scoped-by-managed-namespace): wave-2's
  install bullets promise none of these. Pod-failure surfacing IS
  delivered — Test_BrokenInstallThenCorrected asserts "traffic-manager
  pod is not ready"; the old test's extra detail is the literal reason
  string
- injector extras (failed-inject resync, agent image from config, env
  prefix interpolation, TLS annotations, multi-workload OnDemand):
  catalog-only; wave-2's six injector bullets scope them all out
- namespaces cluster-wide manager + simultaneous multi-namespace attach:
  not in wave-2's three bullets (cluster-wide is the one worth a second
  look — see decisions). AllowsUnmanagedMappedNamespace likewise: wave-4's
  trimmed Throughput bullet did not carry it, and mapping-scope semantics
  live in namespaces/MappedNamespaces
- cloud agent-arrival, cloud log levels, rootd log level: wave-3 promised
  "cloud_config_test.go's core" only
- auth x509 (cert-only client, x509.enabled=false rejection): wave-3 said
  "port only what is assertable through the CLI + ManagerClient";
  enforcing.go's doc comment records the drop with that reason — already
  a recorded drop under the m7 rule
- enforcing-rejects-legacy-client: a version-compat concern; the m4
  compat job is its natural home, not regression
- nodeagent shared-Job semantics, ingest-without-injector, Job reaping on
  uninstall: wave-3's five nodeagent bullets scope them out
- quic VPN-only outbound + traffic-agent coexistence: wave-3 omitted them
  from the portable core; both are thin variants of asserted behaviour
  (VPN-only = outbound reachability with quic transport, coexistence = a
  sidecar attach under a quic manager)
- CIDR refusal variants (autoResolveConflicts=false both flavors),
  config-driven allow-conflict route check, local-DNS-inside-conflict:
  wave-4's Conflicts bullet promised auto-resolve + allow-conflicting
  exactly, and delivered exactly that
- proxy-via loopback and proxy-via + mounts: wave-4: "mirror
  proxy_via_test.go's core, skip the mounts variant"
- mounts write round-trip (documented in-suite as needing a PVC),
  read-only mount, relative --mount path, NoInterceptorResponse:
  not in wave-4's mounts bullets
- large-file transfer: catalog-only; wave-4's mounts bullets dropped it
  (the backend axis itself is covered by mounts/ftp_vs_fuse.go)
- docker extras (daemon status shape, alsoProxy32, singleNameLookup,
  teleroute-network subnet check, gather-logs over docker): wave-4
  promised the Coexist/CacheFiles "essentials"; m5's DockerConnRun listed
  exactly the three tests it ports
- `telepresence uninstall <wl>` / agent scrub on helm uninstall: one
  behaviour behind one helper, asserted 10x in workloads_test.go plus
  once in uninstall_test.go — overlap-collapse territory. The m4 compat
  manifest already records the exemption with a reason ("no compat-core
  test runs that command"). Accept that record, or close it with one
  ~20-line test (attach, uninstall, gone from `list --agents`) — the one
  drop here worth reconsidering, since it is the verb's only integration
  coverage

### Genuine open decisions (not promised anywhere; user call)

D1. Argo Rollout kind (argo_rollouts_test.go) — a real supported feature
    (workloads.argoRollouts.enabled) needing a CRD fixture; port (medium)
    or drop with the reason that it needs an Argo install
D2. cluster-wide manager (Test_NamespacesClusterWide) — the unrestricted
    config is common in production and no new suite runs one; medium
    (invasive on a shared cluster)
D3. inactive-client takeover (inactive_client_test.go, 2 tests) — real
    feature, catalog-claimed, silently dropped by wave-1; medium
D4. `--docker-run` traffic/teardown over a docker connection
    (Test_DockerRun_DockerDaemon) — sits between m5's explicit per-test
    list (excludes it) and its "supersedes the dockerDaemonSuite
    docker-run tests" summary (includes it); the one genuinely ambiguous
    m5 scope line

### Deferred by open issue (blocks deletion only, no work)

- quic Test_ZZDiscoveryNodePort + Test_NodeAgentTransport:
  telepresenceio/telepresence#4227; quic_test.go keeps these two until it
  closes

## Retirement readiness (pass 2)

"drops" means recording the dispositions above in the retirement commit —
no code. Small = one test or axis in an existing suite.

| area | ready? | remaining before retirement |
|---|---|---|
| dns | **retired** | — |
| state | **retired** | — |
| smoke | yes | drops only |
| mounts | yes | drops only |
| routing | yes | drops + the udp verdict (recommended small port) |
| auth | yes | drops only (x509 already recorded in-suite; legacy-client points at the m4 compat job) |
| session | near | P5 (one bulk test) + drops |
| nodeagent | near | P3 (filtered wiretap) + drops |
| quic | near | drops; 2 files' tests wait on #4227 |
| connect | near | P2 (kubeauth) + drops; D3 decision |
| docker | near | P4 (/intercept-info) + drops; D4 decision |
| namespaces | near | drops; D2 decision |
| injector | no | M2 (enabled=false, mandated) + drops |
| intercept | no | M1 (--container, mandated) + P1 (--to-pod) + M3 env-excludes + drops |
| install | no | P6 (Setup: build, or hold setup_test.go back like #4227) |
| attach | no | M1 + drops; D1 decision; retires last by design |

`dns` and `state` retired first, as the two areas where the parity argument
is not in dispute. That commit establishes the mechanics every later area
follows: delete the old files and any testdata only they used, move the
catalog's supersession rows here, repoint every provenance comment that
cited a deleted file, and confirm both packages still build.

Retiring those five files also removed
`integration_test/testdata/k8s/echo-w-subdomain.yaml`, which only
subdomain_test.go used.
