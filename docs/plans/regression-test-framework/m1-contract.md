# M1 implementation contract

Binding API contract for milestone 1 of `plan.md`. Implementation agents must
follow the signatures and behaviors here; where a detail is unspecified,
follow the spirit of `plan.md` and the conventions of the existing
`integration_test/itest` package (but never import it).

All packages are part of the main module:
`github.com/telepresenceio/telepresence/v2/regression_test/...`

Import DAG (no cycles):

```
suites/*  ->  rt  ->  cli, managers, workloads
suites/*  ->  check -> (stdlib only)
managers, workloads, cli: leaf packages (import only stdlib + third-party +
    small main-module utility packages where clearly useful)
```

Note: concrete fixtures (namespace, manager, workload, connection,
localservice) live in package `rt` (files `fixture_*.go`), NOT a separate
`fixtures` package — a separate package would create an import cycle with the
`rt.Suite` accessors.

## Directory layout (M1)

```
regression_test/
  main_test.go
  framework/rt/        env.go runtime.go fixture.go registry.go suite.go
                       manifest.go report.go platform.go state.go
                       fixture_namespace.go fixture_manager.go
                       fixture_workload.go fixture_connection.go
                       fixture_localservice.go
  framework/cli/       tp.go opts.go types.go
  framework/managers/  spec.go values.go catalog.go
  framework/workloads/ template.go templates/  (go:embed)
  framework/check/     check.go
  suites/smoke/        cli.go connected.go
  suites/attach/       modes.go
  suites/intercept/    filters.go
```

## Environment (rt/env.go)

| Var | Effect | Default |
|---|---|---|
| RTEST_KUBECONFIG | kubeconfig for the run | standard clientcmd resolution |
| RTEST_CONTEXT | kube context; passed as `--context` to kubectl and to telepresence connect/helm | kubeconfig current context |
| RTEST_REGISTRY | image registry for manager/agent | $TELEPRESENCE_REGISTRY, else ghcr.io/telepresenceio |
| RTEST_EXECUTABLE | client binary under test | `<repo>/build-output/bin/telepresence[.exe]` |
| RTEST_CLIENT_VERSION, RTEST_MANAGER_VERSION, RTEST_AGENT_VERSION | compat overrides; parsed in M1, full old-version support in M4 | version under test |
| RTEST_LABELS / RTEST_SKIP_LABELS | comma lists; select/deselect by label (ANY-match) | unset |
| RTEST_FRESH=1 | ignore adoptable resources, provision fresh | off |
| RTEST_TEARDOWN=1 | destroy resources at end even in dev mode | off |
| RTEST_TAIL_LOGS=1 | continuous pod log capture | off (capture on failure only) |

CI mode = `GITHUB_ACTIONS=true`: implies fresh + teardown. The shell
environment always wins; there is no rtest.yml in M1. Version under test:
`$TELEPRESENCE_VERSION` if set, else parsed from `<exe> version --output json`
(client field only, daemons not required). Registry value `local` =>
pullPolicy Never; `localhost:*` => Always (same rule as the old harness).

## Runtime (rt/runtime.go)

```go
type Runtime struct{ /* opaque */ }
func (r *Runtime) Exe() string
func (r *Runtime) Version() semver.Version          // blang/semver/v4
func (r *Runtime) Registry() string
func (r *Runtime) IsCI() bool
func (r *Runtime) KeepResources() bool               // dev mode && !RTEST_TEARDOWN
func (r *Runtime) ArtifactDir(sub ...string) string  // build-output/rtest/logs/<runid>/...
func (r *Runtime) Kubectl(ctx context.Context, ns string, args ...string) (string, error)
func (r *Runtime) KubectlJSON(ctx context.Context, ns string, out any, args ...string) error
func (r *Runtime) CLI() *cli.TP                      // bound to run env+dirs
func (r *Runtime) Infof(format string, args ...any)  // progress line, also into run log
```

One process-global Runtime, created by `rt.Main`. `rt.R()` returns it
(panics before Main). Child-process env for CLI and kubectl is explicit and
documented in code: HOME, PATH, USER/LOGNAME, TMPDIR, KUBECONFIG,
DEV_TELEPRESENCE_CONFIG_DIR, DEV_TELEPRESENCE_LOG_DIR,
TELEPRESENCE_PROGRESS=plain, GOCOVERDIR (when set), plus documented extras on
Windows. Config dir and log dir live under the STABLE root
`build-output/rtest/home/` so daemons and caches survive between runs
(cross-run adoption). Baseline client config written once per run to the
config dir: log levels debug, `usage.enabled=false`, and the same timeout set
the old `withBasicConfig` pins (copy values from
`integration_test/itest/cluster.go:430` but do not import it).

## Fixture engine (rt/fixture.go)

```go
type Env struct {
    Ctx context.Context
    T   testing.TB
    R   *Runtime
}

type Fixture[T any] struct {
    Name        string                       // "manager/default"
    Hash        string                       // canonical spec hash (identity)
    ProvisionFn func(Env) (T, error)
    AdoptFn     func(Env) (T, bool)          // optional; dev mode only
    DestroyFn   func(Env, T) error           // idempotent
}

func Get[T any](t testing.TB, f *Fixture[T]) T
func Mutate[T any](t testing.TB, f *Fixture[T]) T
```

Engine behavior (single global engine owned by Runtime; mutex-guarded):

- `Get`: memo hit by Hash returns stored value. Miss: in dev mode try
  `AdoptFn` first (unless RTEST_FRESH); otherwise `ProvisionFn` under the
  calling test's `t` (log line + duration via `Infof`). Error => record the
  error, `t.Fatalf`. A later `Get` of a failed fixture => `t.Skipf("fixture
  %s failed earlier: %v", ...)` — no retry storms.
- `Mutate`: like Get, plus registers `t.Cleanup` that removes the memo entry
  (the resource is in unknown state; the next Get re-provisions).
  ProvisionFn must therefore CONVERGE from any prior state (helm
  install-or-upgrade, kubectl apply + rollout wait, reconnect).
- Provision order is recorded; final teardown destroys in LIFO order.
  Teardown runs at `Main` end when IsCI() or RTEST_TEARDOWN; otherwise
  resources are kept and recorded in the state file. Destroy of a
  never-provisioned or already-destroyed fixture is a no-op.
- Nested fixtures: ProvisionFn may call `rt.Get(e.T, dep)`.

State file (rt/state.go): `build-output/rtest/state.yaml`, keyed by cluster
server URL + context. Records, per fixture hash: kind, names created (release,
namespaces, workloads), and the values hash for the manager release. AdoptFn
implementations validate against BOTH the state file and the cluster (release
secret exists, rollout ready, `telepresence status` for connections). `make
rtest-clean` support: a helper `rt.CleanAll(ctx)` invoked via
`go run ./regression_test/framework/rtclean` (tiny main) deletes everything
labeled `purpose=tp-rtest` and quits daemons.

## Registry and runner (rt/registry.go, rt/platform.go)

```go
func Register(s TestingSuite, opts ...RegOption)
func InArea(area string) RegOption
func NeedsManager(spec managers.Spec) RegOption   // declared primary spec
func WithLabels(labels ...Label) RegOption
func On(goos ...string) RegOption                 // platform allow-list
func NotOn(goos ...string) RegOption
func Requires(caps ...Capability) RegOption       // Docker, Sudo, FUSE, Veth
func RunArea(t *testing.T, area string)
func Main(m *testing.M)                           // called from TestMain
```

- `TestingSuite` = testify's `suite.TestingSuite`; suite display name is the
  struct type name via reflection.
- `RunArea` filters by area, applies label env filters, sorts by
  `(managerSpecHash, suiteName)` (deterministic!), then for each suite:
  `t.Run(name, ...)` -> constraint check (`On`/`NotOn`/`Requires` self-skip
  with the unmet constraint in the skip message; capabilities detected once,
  lazily: Docker=`docker info`, Sudo=`sudo -n true`, FUSE=fusermount/macfuse
  present, Veth=linux+Sudo) -> `suite.Run(t, s)`.
- Labels in M1: `CompatCore`, `Slow`, `Stress`, `FlakyRetry` (constants of
  type `Label`).

## Suite base (rt/suite.go)

```go
type Suite struct {
    suite.Suite
    // unexported: registration ref, per-suite lazy handles
}
func (s *Suite) R() *Runtime
func (s *Suite) Ctx() context.Context               // run ctx + per-test logger
func (s *Suite) Manager() *ManagerHandle             // Get(declared spec)
func (s *Suite) AppNamespace() string                // Get(shared app ns)
func (s *Suite) Connect(opts ...cli.ConnectOpt) *Conn // Get(Connection(spec, app ns, opts))
func (s *Suite) Workload(tpl workloads.Template) *Workload
func (s *Suite) LocalEcho() *LocalService            // in-process echo on :0
func (s *Suite) CLI() *cli.TP
```

- Accessors are LAZY (each calls `rt.Get` on first use). `SetupSuite` in
  suites must not provision; the base enforces nothing but the rule is
  documented on the type.
- `TearDownTest` on the base: when the test failed, dump `kubectl get events
  --field-selector type!=Normal` for the involved namespaces and copy the
  daemon log tails into `ArtifactDir(<test>)`.
- The base records per-test outcome+duration for the manifest
  (SetupTest/TearDownTest hooks).

## Manifest and report (rt/manifest.go, rt/report.go)

`ArtifactDir()/manifest.json`:

```json
{ "run": "<runid>", "start": "...", "end": "...",
  "tests": [ {"name": "TestIntercept/HeaderFilter/Test_PathPrefix",
              "outcome": "pass|fail|skip", "duration_ms": 1234,
              "labels": ["compat-core"], "artifacts": "logs/<runid>/<test>"} ],
  "fixtures": [ {"name": "manager/default", "hash": "...",
                 "action": "provisioned|adopted|mutated", "duration_ms": 5678} ] }
```

Progress: one line per fixture action and per suite start via `Infof`
(`[rtest] fixture manager/default: helm install 12.3s`). No dependency on
tools/test-report.

## Concrete fixtures (rt/fixture_*.go)

Naming: manager namespace `rtest-manager`; shared app namespace `rtest-app`;
private namespaces `rtest-<prefix>-<4hex>`. All created namespaces get labels
`purpose=tp-cli-testing` is NOT used; use `purpose=tp-rtest` and
`rtest.telepresence.io/managed="true"` (the manager's namespaceSelector
matches the latter). Stable names are what make adoption work; document "one
rtest run per cluster at a time".

- **Namespace**: create ns with labels; Destroy deletes (private namespaces
  are always destroyed at run end, even in dev mode).
- **Manager** (`fixture_manager.go`): ensure manager ns + test ServiceAccount
  `telepresence-test-developer` (+ minimal RBAC, modeled on
  `integration_test/testdata/k8s/client_sa.yaml`, embedded not referenced);
  merge `managers.Baseline(runtime info)` with the spec's values; write
  `values.yaml` under ArtifactDir; run `telepresence helm install|upgrade -n
  rtest-manager -f values.yaml` (upgrade when the helm release secret already
  exists: `kubectl get secret -n rtest-manager -l owner=helm` name match);
  then `kubectl rollout status deploy/traffic-manager`. Adopt: state-file
  values-hash match + release secret exists + rollout ready. Destroy:
  `telepresence helm uninstall` tolerating absence. All manager specs share
  ONE release; switching specs = in-place upgrade (the engine's memo makes
  consecutive same-spec suites free).
- **Workload**: render template, `kubectl apply -f -`, rollout wait. Value:
  `type Workload struct { Name, Namespace, Kind string; Port int; SvcName string }`.
  Destroy: delete manifest objects.
- **Connection** (`fixture_connection.go`): `telepresence connect --namespace
  <ns> --manager-namespace rtest-manager --as
  system:serviceaccount:rtest-manager:telepresence-test-developer` plus
  opts. Adopt: `telepresence status --output json` reports a connection to
  the same namespace+manager. Destroy: `telepresence quit -s`.
  `Conn` methods (each takes `t testing.TB` first):
  `Status`, `List`, `Intercept(t, wl, opts...) *Attach`,
  `Ingest(t, wl, opts...) *Attach`, `Disconnect`.
  `Attach.Detach(t)`; Attach also captures the `--format json` output struct.
- **LocalService**: in-process HTTP server bound to `127.0.0.1:0`, responds
  with body `rtest-local:<id>` — the marker `check` asserts on. Not persisted
  or adopted; always destroyed.

## cli package

```go
type TP struct { Exe string; Env []string; Dir string; Logf func(string, ...any) }
func (tp *TP) Run(ctx context.Context, args ...string) (stdout, stderr string, err error)
func (tp *TP) OK(t testing.TB, args ...string) string  // require no error; tolerate single-line warnings on stderr like the old TelepresenceOk
func (tp *TP) JSON(ctx context.Context, out any, args ...string) error
```

Typed outputs in `types.go`: `Status`, `Version`, `InterceptInfo`,
`IngestInfo`, `ListEntry` — define ONLY the fields the tests assert, matching
the actual JSON emitted by the CLI (verify against the CLI source under
`pkg/client/cli`, do not guess). Option builders in `opts.go`:
`ConnectOpt` and `InterceptOpt` funcs producing args — `Port(local int,
remote string)`, `ToLocal(*LocalService)` equivalent lives in rt (needs the
handle; rt composes `cli.Port`), `MountFalse()`, `HTTPHeader(k, v)`,
`HTTPPathPrefix(p)`, `Replace()`, `WorkloadFlag(name)`, `EnvFile(path)`.
No hard-coded ports anywhere: local ports always come from `LocalService`
(bound to :0) or from an ephemeral `net.Listen` probe.

## managers package

```go
type Spec struct { Key string; Values Values }
func (s Spec) Hash() string                    // sha256 of canonical JSON of Values + Key
type Values struct { /* typed subset of chart values used by the catalog */ }
func Baseline(reg, tag, pullPolicy string, selectorLabel string) Values
func Merge(base, over Values) Values
var Default Spec                                // baseline only
```

M1 ships `Default` plus stubs used by later waves compile-ready:
`NodeAgent()`, `InjectorDisabled()`, `AuthEnforcing()` etc. may be added in
M3; do NOT pre-create them now. Baseline values: `logLevel: debug`,
`image.registry/tag/pullPolicy`, `agent.image.*`, `usage.enabled=false`,
`timeouts.agentArrival=60s`, `namespaceSelector` matching
`rtest.telepresence.io/managed="true"`, `clientRbac`/`managerRbac` with the
test ServiceAccount as subject (mirror `integration_test/itest/helm.go:57`
semantics without importing).

## workloads package

`Template` renders a Deployment/StatefulSet + Service from embedded
templates (`go:embed templates/*.yaml`), parameterized by name, kind,
replicas, image, port. Use the same echo-server image the old testdata uses
(look it up in `integration_test/testdata/k8s`, copy the reference).
`Echo(name string)` => Deployment+Service, one HTTP port.
`EchoStatefulSet(name string)` likewise.

## check package

```go
func EventuallyHTTP(t testing.TB, url string, want func(status int, body string) bool, timeout time.Duration)
func BodyContains(s string) func(int, string) bool
```

`rt` composes these into `Conn`-level helpers:
`rt.RoutedToLocal(t, url, ls *LocalService)` asserts the response carries the
local marker; `rt.RoutedToCluster(t, url)` asserts it does not.

## M1 suites (proof content)

- `suites/smoke/cli.go` — `SmokeCLI` (area "smoke", no manager): version
  (`--output json`: client version equals Runtime.Version), status while
  nothing runs (run `quit -s` first — this suite runs before any connection
  exists because main_test.go runs areas in order and smoke is first; add a
  comment), `config view --client-only`.
- `suites/smoke/connected.go` — `SmokeConnected` (area "smoke",
  NeedsManager(Default)): status JSON (root+user daemon populated), version
  (client+daemons+manager present), list excludes the traffic-manager.
- `suites/attach/modes.go` — `AttachModes` (area "attach",
  NeedsManager(Default), Labels(CompatCore)): table-driven
  {intercept, ingest} x {Echo Deployment, Echo StatefulSet}:
  attach -> list shows it -> for intercept: cluster URL serves the LOCAL
  marker (RoutedToLocal) -> detach -> cluster URL serves the cluster echo
  again (RoutedToCluster). For ingest: attach -> list -> env contains
  workload env -> detach. The workload-kind axis is extended in wave 1.
- `suites/intercept/filters.go` — `HeaderFilter` (area "intercept",
  NeedsManager(Default), Labels(CompatCore)): header-filtered intercept:
  request WITH header hits local, request WITHOUT header hits cluster (this
  sends real traffic through the filter, unlike the old suite).

`main_test.go`: TestMain -> rt.Main; `TestSmoke`, `TestAttach`,
`TestIntercept` call `rt.RunArea`.

## Makefile (build-aux/main.mk additions, M1 portion)

```
check-regression: build-deps  ## go test ./regression_test/... (plain output)
rtest-clean:                  ## go run ./regression_test/framework/rtclean
```

(`rtest-client`, coverage targets and CI wiring are M2.)

## Quality bar

- `go vet ./regression_test/...` and `make lint` clean (lll: keep lines
  short; no long inline strings).
- No imports of `integration_test/...` anywhere.
- Comments follow repo convention: describe code as-is, short, no
  transition/why-it-moved commentary.
- Deterministic: no `map` iteration affecting order; sort everything
  user-visible.
