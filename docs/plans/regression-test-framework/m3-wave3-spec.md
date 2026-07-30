# M3 wave 3 spec (nodeagent, quic, auth, session)

## Framework extensions (first, one agent)

1. **managers.Values additions** (verify all keys against
   charts/telepresence-oss/values.schema.yaml, with Merge support):
   - `NodeAgent` (`nodeAgent`): `Enabled bool`.
   - `Client` (`client`): typed passthrough for the cluster-served client
     config keys wave 3 uses: `nodeAgent.enabled`, `logLevels.{rootDaemon,
     userDaemon}`, `routing.{neverProxySubnets,allowConflictingSubnets,
     autoResolveConflicts}`, `dns.includeSuffixes`. Model as a nested typed
     struct; a `map[string]any` escape hatch field is acceptable for rare
     keys if schema-safe.
   - `QuicTunnel` (`quicTunnel`): `Enabled`, `Service{Type, NodePort}`,
     plus whatever quic_test.go's values used (read it).
   - `Security` (`security`): `Authentication{Mode string, X509{Enabled
     *bool}}`.
   - `Agent`: add `EnableH2cProbing *bool` (`enableH2cProbing`).
   - `Usage`: add `CollectorAddress string`, `Insecure bool`.
   - `TelepresenceAPI` (`telepresenceAPI`): `Port int`.
2. **Catalog entries**: `NodeAgent()`, `NodeAgentNoInjector()`,
   `NodeAgentClientDefault()`, `QuicNodePort()`, `QuicRelay()`,
   `AuthEnforcing()`, `AuthPermissive()`, `UsageTo(addr string)`,
   `ClientConfig(key string, v Values)` or equivalent parameterized specs.
   Derive the exact value combinations from the superseded suites
   (node_agent*, quic_test, manager_auth_test, usage_reporting_test,
   cloud_config_test) — read them for the values, never import them.
3. **Manager gRPC access** (`rt/managergrpc.go`): `ManagerClient(e Env, ns
   string) (manager.ManagerClient, func())` — port-forward dial to the
   traffic-manager service like integration_test/itest/traffic_manager.go:80
   does (k8spf scheme + portforward.ResolveSvcToPod), returning the client
   and a close func. Needed by session/WorkloadWatch now and compat-core
   (M4) later.
4. **Local usage collector** (`rt/fixture_usagecollector.go`): in-process
   gRPC server implementing the usg service (rpc/usg), listening on a host
   port; expose received reports. The manager reaches the host via the
   node's view of the host — on kind/minikube use the address
   usage_reporting_test.go used (host.docker.internal) and SKIP the suite
   when the cluster cannot resolve it (mirror the old skip probe).

## Suites

### nodeagent area (suites/nodeagent/, area "nodeagent")
Consolidate node_agent_test.go, node_agent_multi_test.go,
node_agent_no_injector_test.go, node_agent_cluster_default_test.go into one
matrix-style area sharing the NodeAgent spec family:
- Attach modes: node-agent intercept, ingest, wiretap (+ HTTP-filtered
  variants) on a deployment; assert the workload pod is untouched (no
  sidecar container) and a Job-based agent appears and is reaped on detach.
- Replace refusal: `replace` with node-agent must fail with the CLI's
  refusal message.
- Multi-replica: 4-replica workload, global + filtered intercepts reach all
  replicas (N sequential requests all local).
- NoInjector: with NodeAgentNoInjector(), intercepts still work;
  `--node-agent=false` is rejected.
- ClientDefault: with NodeAgentClientDefault(), a plain intercept uses the
  node agent (pod untouched); `--node-agent=false` overrides and injects
  the sidecar.
Labels: none special; Requires nothing beyond the shared cluster (the kind
dev cluster runs containerd — the node-agent path must work there; CI runs
a dedicated docker-runtime job for the CRI-socket variant, out of scope
here).

### quic area (suites/quic/, area "quic")
From quic_test.go + quic_disabled_test.go, the portable core:
- Disabled default: with the Default spec, `status --format json` reports
  tunnel transport grpc (not fallback) and traffic works.
- Enabled: QuicNodePort() spec: status reports quic transport; intercept
  round-trip works over it.
- Fallback: QuicNodePort with an unreachable advertised endpoint (read how
  quic_test.go forced this — externalHost override) silently falls back to
  grpc and traffic still works.
- Relay: QuicRelay() (agentPortForward=false): intercept traffic works.
- Outage/recovery (Slow label): scale the quic forwarder (or manager,
  matching the old test) down and up; attachment survives.
Skip-with-reason anything that depends on infrastructure the kind cluster
lacks; each skip names the missing piece.

### auth area (suites/auth/, area "auth")
From manager_auth_test.go (read it carefully; it uses extra
ServiceAccounts and raw gRPC metadata):
- Permissive: AuthPermissive() spec: plain connection works.
- Enforcing: AuthEnforcing(): the test-developer identity connects and
  intercepts; an unauthorized identity (second SA without grants) is
  denied; with x509 disabled the cert-only client is rejected.
Keep the identity plumbing minimal: create the extra SA + (lack of)
bindings with kubectl in-test, in the manager namespace, cleaned up after.
Port only what is assertable through the CLI + ManagerClient; raw
gRPC-metadata assertions may use rt.ManagerClient.

### session area (suites/session/, area "session")
- ClientConfig: cluster-served client config (cloud_config_test.go's core):
  switch the shared release to a ClientConfig spec serving a distinctive
  dns.includeSuffixes + routing value; a fresh connection reflects it in
  `config view` / status; switch back via Mutate discipline.
- GatherLogs: deduped matrix from gather_logs_test.go: all logs / manager
  only / agents only / one agent / --get-pod-yaml only with logs. Assert on
  the zip's file set (unzip -l). One live intercept beforehand so agent
  logs exist.
- UsageReporting: with UsageTo(collector addr) spec and a client config
  pointing at the local collector: connect + intercept, assert the
  collector received reports from both client and manager. Self-skip when
  host.docker.internal is unresolvable from the cluster.
- WorkloadWatch: via rt.ManagerClient: WatchWorkloads emits added/
  modified events for a workload as it gains/loses an agent (port the
  essential assertions of workload_watch_test.go, tolerating event
  batching); also `list --output json-stream` shows a watch event when a
  workload appears.
- ManagerInfo: GetClusterInfo via rt.ManagerClient returns the service
  subnet consistent with `status` output.

Area wiring: orchestrator adds TestNodeAgent/TestQuic/TestAuth/TestSession
entries + blank imports (never the suite agents).

Ordering: nodeagent and quic churn the shared release heavily; both areas
run after the wave-1/2 areas in main_test.go order, quic last before
compat-sensitive suites since transport experiments are the most invasive.
Every non-Default spec acquisition uses Mutate.
