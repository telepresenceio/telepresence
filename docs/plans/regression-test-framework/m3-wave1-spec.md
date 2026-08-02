# M3 wave 1 spec (connect, intercept, attach full catalog)

Framework extensions needed before the wave-1 suites can be written, then the
suite content. Suites live in `regression_test/suites/<area>/`; framework
changes in `regression_test/framework/`.

## Framework extensions (one agent, first)

1. **Kubeconfig derivation** (`rt/kubeconfig.go`): helpers that write derived
   kubeconfigs under ArtifactDir and return their paths:
   - `KubeConfigCopy(e Env, mutate func(*api.Config))` — load the run's
     kubeconfig (clientcmd), apply mutate, write, return path.
   - `WithKubeConfigExtension(e Env, ext map[string]any)` — adds the
     `telepresence.io` extension object to the cluster entry (mirror the old
     itest WithKubeConfigExtension semantics, see integration_test/itest/
     cluster.go:1207 for the shape; do not import it).
   Connection options must be able to point at a derived kubeconfig:
   `cli.KubeConfig(path)` ConnectOpt is NOT possible (connect has no
   --kubeconfig flag; KUBECONFIG env governs) — instead add a
   `rt.ConnWithKubeconfig(path string)` option consumed by the connection
   fixture: it sets KUBECONFIG in that connect invocation's env and becomes
   part of the fixture hash.
2. **Secondary manager** (`rt/fixture_manager2.go`): `SecondaryManager(spec
   managers.Spec, ns string)` — a full manager release in its own namespace
   (created + destroyed like private namespaces; NOT the shared release).
   Used by connect/Multi and (wave 2) install/Helm. Keep it simple: always
   provisioned fresh, never adopted, destroyed at run end unconditionally.
3. **Private namespaces** (`rt/fixture_namespace.go`): `PrivateNamespace(e
   Env, prefix string)` fixture — `rtest-<prefix>-<4hex>` with the managed
   label, always destroyed at run end (even in dev keep mode).
4. **Workload template variants** (`framework/workloads`): add
   `EchoHeadless(name)` (headless StatefulSet service), `EchoNoService(name)`
   (deployment without a service), `EchoMultiPort(name)` (two named http
   ports), `EchoReplicas(name, n)`. Templates stay embedded.
5. **Connection options**: named connections (`--name`), docker mode
   (`--docker`), `--use` selection for CLI calls made against a specific
   connection. `Conn` must carry its connection name and pass
   `--use <name>` on every per-connection CLI call when set. Add
   `rt.ConnNamed(name)`, `rt.ConnDocker()` options; hash includes them.
6. **Attach verbs**: `Conn.Replace(t, wl, opts...)` and
   `Conn.Wiretap(t, wl, opts...)` mirroring Intercept/Ingest (verbs
   `replace`, `wiretap`).
7. **LocalService variants**: keep HTTP echo; add h2c-capable variant only
   when the routing suite lands (wave 1 intercept/Routing includes the h2c
   check formerly in h2c_intercept_test.go).
8. **Config mutation** (`rt/fixture_connection.go` or suite-level): the
   localShortcut suite needs a connection whose client config enables
   intercept.localShortcut. Model as a distinct baseline-config variant:
   `rt.ConnWithConfig(delta func(client.Config))` — writes a variant config
   dir, hash includes a delta fingerprint, daemon for that connection runs
   with DEV_TELEPRESENCE_CONFIG_DIR pointing at the variant dir. Only one
   live daemon at a time on the host: acquiring a connection with a different
   config dir must quit the previous host daemon first (the engine's Mutate
   is not enough — implement as part of the connection fixture's provision:
   `quit -s` when the running daemon's config dir differs).

## Wave 1 suites (parallel agents after the extensions land)

### connect area (suites/connect/)
- Lifecycle: connect->status->disconnect->connect->quit; reconnect after
  API-server drop via `sudo iptables` DROP/undo (Requires(Sudo), NotOn
  windows/darwin); mirrors reconnect_session_test.go semantics.
- Errors: invalid kubeconfig file, nonexistent context, connect to an
  unmanaged namespace fails with a clear error (namespace without the
  managed label).
- Contexts: kubeconfig `telepresence.io` extension: also-proxy + never-proxy
  entries appear in `status` output subnets; dns include-suffixes; manager
  discovery via extension `manager` namespace field if present. (exec-
  credential kubeauth moved to wave 4 docker area, where the old test sat.)
- Multi: Requires(Docker). Two named docker connections: first to
  rtest-app via the shared manager, second to a private namespace via a
  SecondaryManager; `telepresence list --use <name>` shows the right
  namespace's workloads; concurrent intercepts one per connection;
  quitting one leaves the other connected.

### attach area (suites/attach/)
- Modes: extend the M1 table to {intercept, ingest, replace, wiretap} x
  {deployment, statefulset, headless, no-service, multiport}. Skip cells
  that don't apply (wiretap/replace on no-service? verify per CLI behavior:
  no-service workloads are attachable since 2.20 via container ports —
  check and encode reality; a skipped cell must carry the reason).
  Intercept cells assert routed-to-local + detach restores; replace cells
  assert the app container is replaced (list shows replace attachment) and
  traffic reaches local handler even without filter; wiretap cells assert
  local receives a COPY while the cluster still serves (both markers
  observable).
- Conflicts: same-workload conflicts: intercept-then-intercept (same port)
  fails; ingest-then-intercept and intercept-then-ingest behavior; repeat
  ingest is idempotent. Mirror ingest_test.go's conflict semantics, judged
  from the CLI's actual errors.

### intercept area (suites/intercept/)
- Filters: extend to path-prefix filter, header+path combined, two
  coexisting filtered intercepts on one workload routing to two different
  local services, unfiltered TCP port conflict detection (second intercept
  without filter on same port fails).
- Flags: explicit table (rt.Matrix arrives in M5): port forms
  (local:svcPort, local:portName, bare), --to-pod TCP, env output flags
  (--env-file/--env-json round trip: intercepted env contains the
  workload's env), --detailed-output --format json shape.
- Routing: multi-replica (EchoReplicas(4): every replica routes to local —
  loop over pod deletion? keep simple: N sequential requests all local),
  multiport service (intercept port A while port B still serves cluster),
  pod-IP bind (EchoNoService + --address? skip if too deep, note it),
  localShortcut on/off behavioral pair, h2c: intercepted h2c service keeps
  HTTP/2 prior-knowledge working via local h2c server.

Suites must self-skip with reasons for genuinely-unportable cells rather
than silently dropping them. Every suite registers with NeedsManager(
managers.Default) unless stated; labels: CompatCore on Modes' intercept/
deployment cell path, Filters header test (already), Lifecycle reconnect
NO (sudo). Deterministic subtest names.
