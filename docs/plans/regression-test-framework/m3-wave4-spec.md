# M3 wave 4 spec (dns, routing, mounts, docker + session extras)

## Framework extensions (first, one agent)

1. **Generic connect args**: `rt.ConnExtraArgs(args ...string) ConnOpt` —
   verbatim extra arguments for the connect invocation, folded into the
   fixture hash. Retires the mapped-namespaces gap noted in
   suites/namespaces/mapped_namespaces.go (update it to use this) and
   serves --proxy-via / --allow-conflicting-subnets in this wave.
2. **Volume-bearing workload**: `workloads.EchoWithConfigVolume(name)` — the
   echo deployment plus a ConfigMap (fixed, distinctive key/content rendered
   into the manifest) mounted at a well-known path. The mounts suites assert
   the intercept's local mount exposes that content. Thread through the
   fixture hash.
3. **Mount helpers** (`rt` or `check`): resolve the local mount root from
   the intercept's env (`TELEPRESENCE_ROOT`, `TELEPRESENCE_MOUNTS` from
   `--env-json` output or cli.InterceptInfo's Environment if exposed —
   verify what the JSON carries and extend cli.InterceptInfo with
   `Environment map[string]string` if needed); helpers to read a file under
   the mount with a timeout (FUSE mounts appear asynchronously).
4. **veth conflict scripts**: read integration_test/testdata/scripts/
   veth-up.sh and veth-down.sh; embed equivalent scripts (or inline exec
   calls) in the routing suite package — never reference the old testdata
   paths at runtime.

## Suites

### dns area (suites/dns/, area "dns", NeedsManager(Default))
From subdomain_test.go, svcdomain_test.go, uhn_dns_test.go, wpad_test.go:
- Resolution: while connected, `<svc>.<ns>`, `<svc>.<ns>.svc`, and
  single-label `<svc>` all resolve and serve; a headless pod's
  `<pod>.<svc>.<ns>` resolves (subdomain form).
- ExcludesMappings: kubeconfig extension dns.excludes hides a name that
  would otherwise resolve; dns.mappings adds an alias that resolves to the
  echo service (mirror uhn_dns_test.go semantics; derived kubeconfig +
  ConnWithKubeconfig; Requires nothing special).
- WPAD: `wpad.<anything>` lookups are suppressed (NXDOMAIN) while
  connected (wpad_test.go semantics).

### routing area (suites/routing/, area "routing")
- ProxyVia: connect with `--proxy-via all=<wl>`: workload subnets are
  reached through the workload's agent (assert echo reachable and status
  shows the virtual subnet — mirror proxy_via_test.go's core, skip the
  mounts variant). Requires(Docker)? No — host connection.
- Conflicts (Requires(Sudo), On("linux"), label Slow): veth interface
  colliding with the cluster's service subnet: default connect
  auto-resolves (status shows a translated/virtual subnet), and
  `--allow-conflicting-subnets <cidr>` keeps the raw subnet. Always tear
  the veth down (defer + unique names).
- NeverProxyOmitted: a never-proxy for a sub-CIDR of the cluster's
  service subnet is absent from the root daemon's routed subnets
  (proxy_via_test.go's Test_NeverProxySubnetIsOmitted).

### mounts area (suites/mounts/, area "mounts", Requires(rt.FUSE), NotOn("windows"))
From mounts_test.go, intercept_mount_test.go, ignored_mounts_test.go:
- Content: intercept EchoWithConfigVolume with mounts enabled: the local
  mount exposes the ConfigMap content; a write to a writable volume path
  round-trips (only if the agent mounts it writable — verify from the old
  test which paths are writable; serviceaccount tokens are read-only).
- FTPvsFUSE: run the content check under both intercept.useFtp true and
  false via rt.ConnWithConfig variants.
- Ignored: telepresence.io/inject-ignore-volume-mounts annotation excludes
  the named volume from TELEPRESENCE_MOUNTS (ignored_mounts_test.go's
  4-case table, trimmed to 2: ignored and not-ignored).
- Podscaling (label Slow): with a suite-long intercept+mount active, scale
  the workload to 0 and back; the intercept survives and the mount serves
  again (podscaling_test.go essentials, generous Eventually).

### docker area (suites/docker/, area "docker", Requires(rt.Docker), On("linux"))
- Coexist: host connection and a named docker connection at once, both
  orders (two tests), each lists its namespace (docker_daemon_test.go's
  hostDaemonNoConflict/daemonHostNotConflict essentials).
- CacheFiles: after a docker connect, the host cache files created by the
  containerized daemon are owned by the invoking user (cache dir under the
  run's home; read docker_daemon_test.go::Test_DockerDaemon_cacheFiles for
  which files).
- DockerRun: intercept --docker-run with the echo-test image (build once
  via a fixture from a small embedded Dockerfile like the old
  cli-container testdata, or reuse ghcr echo-server with a command):
  routed-to-local through the handler container; volume env propagation
  skipped with a note if too deep.
- RestAPI: managers spec with TelepresenceAPI.Port set: an intercepted
  workload's sidecar answers /consume-here and /intercept-info from inside
  the cluster (query via kubectl exec curl from the echo pod or the
  echo-server's /forward like restapi_test.go — pick the simpler; if both
  are heavy, assert the API port serves on localhost from the agent pod via
  kubectl exec wget).
- Compose: SKIP for now — register a suite with a single test that
  t.Skip("compose area deferred; see suite-catalog.md") so the gap is
  visible in reports.

### session area additions (suites/session/)
- Throughput (labels Slow, Stress): one bulk-transfer check (a few MiB
  through an intercepted echo round-trip) and N=30 sequential
  connect/disconnect cycles asserting stability. Keep well under the old
  suite's 90-subtest scale.

Area wiring: orchestrator adds TestDns/TestRouting/TestMounts/TestDocker
entries + blank imports before dispatching suite agents.

Notes: every raw connect variant must go through ConnOpts so fixture
hashing and daemon hygiene hold; suites that disturb the default
connection use rt.Mutate; spec switches always via declared NeedsManager or
switch helpers with Reconnect.
