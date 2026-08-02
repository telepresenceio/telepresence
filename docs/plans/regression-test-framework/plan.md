# Regression test framework (`regression_test/`)

A new integration-test package, fully decoupled from `integration_test/`, whose
primary objective is to protect the code base from regressions. It keeps the
spirit of the current framework — suites are easy to add, tests drive the real
CLI against a real cluster — but replaces the position-in-a-tree resource model
with declarative, memoized fixtures so that individual tests start fast and
common resources are configured once, not over and over.

## Goals

1. A single scoped test runs fast: it provisions only the resources it
   declares, and on a warm cluster it adopts resources left by a previous run
   instead of re-installing them.
2. Shared resources (traffic-manager installs, namespaces, workloads,
   connections) are memoized by spec and reused across suites within a run and
   across runs in dev mode.
3. Code coverage for the client binary and the manager/agent images, merged
   into one profile.
4. First-class flag-combination testing for `helm install`/`setup`,
   `connect`, `intercept`, `ingest`, `replace`, and `wiretap`.
5. A labeled `compat-core` subset that exercises most of the
   `manager.Manager` RPC surface and runs bidirectionally (old client vs new
   manager, new client vs old manager).
6. Full-run wall clock well under the current 70 minutes (target: 25-30 min
   serial, less with CI sharding).

## Non-goals

- Modifying or migrating `integration_test/` — it stays untouched and keeps
  running in CI until the new package reaches parity, then suites are retired
  area by area.
- Multi-cluster testing (the old suite doesn't have it either; the fixture
  model leaves room for it later).
- Test parallelism in the first iteration. The fixture model is designed so
  read-only tests *can* later run in parallel, but the initial runner is
  serial (see Milestones).

## Why not evolve the current framework

Findings from a full audit of `integration_test/` (85 files, 51 suites, ~245
tests, ~16k lines):

- Resource configuration is tied to *position* in a fixed harness tree
  (`suffix -> namespace pair -> traffic-manager -> connected -> service`,
  `itest/runner.go`). The only way to get a differently-configured
  traffic-manager is a new namespace-pair suffix, so suites resort to
  in-test `helm upgrade` + rollback pairs. A full run performs **68 in-test
  helm install/upgrade calls, 37 uninstalls (each polling up to 60 s), and 14
  rollbacks (up to 5 min each)** on top of the 5 harness-level installs. This
  is the dominant cost of the 70 minutes.
- Cross-suite isolation is by convention (`defer` restore), not by
  construction: one `harness` value per namespace pair holds a mutable setup
  stack shared by every suite at that level.
- `*testing.T` travels inside `context.Context`, and lazy-setup failures are
  attributed to the level's test, not the suite that triggered the setup.
- `Cluster` is a ~40-method god interface that every suite sees.
- Scoped runs staying cheap depends on two subtle behaviors (a `TEST_SUITE`
  regexp check and testify returning before `SetupSuite` when all methods are
  filtered) that are easy to break unknowingly.
- No coverage support, no parallelism (a cross-process file mutex serializes
  whole binaries), nondeterministic suffix ordering, and huge functional
  overlap (the basic intercept happy path is re-validated in ~20 places, see
  `suite-catalog.md`).

These are structural properties; a rewrite alongside is cheaper and safer than
surgery on a framework that 51 live suites depend on.

## Package layout

```
regression_test/
  main_test.go            one go-test entrypoint per area (thin, see below)
  framework/
    rt/                 runtime, registry, suite base, fixture engine, labels
    fixtures/           concrete fixtures: Namespace, Manager, Workload,
                        Connection, LocalService, DockerDaemon
    managers/           named manager value-sets (the config catalog)
    workloads/          named workload templates (echo, hello, headless, ...)
    cli/                typed CLI invocation layer (JSON-first)
    check/              assertion helpers (EventuallyHTTP, PodHasAgent, ...)
    cover/              coverage orchestration (GOCOVERDIR, pod scraping, merge)
    compat/             version predicates, old-binary download, RPC manifest
  suites/
    smoke/              one Go package per area; normal (non-test) packages
    connect/            with suite structs + methods, registered via init()
    intercept/
    attach/             intercept/ingest/replace/wiretap as one axis
    install/
    injector/
    nodeagent/
    quic/
    auth/
    namespaces/
    dns/
    routing/
    mounts/
    docker/
    session/
  testdata/
```

Suites live in ordinary packages (testify suite methods don't need to be in
`_test.go` files); `main_test.go` blank-imports every suite package and exposes
one top-level `func Test<Area>(t *testing.T)` per area. Everything compiles
into a single test binary, so in-process fixture sharing is trivial, while the
directory tree stays organized instead of 85 files in one folder.

```go
// regression_test/main_test.go
package regression_test

import (
    "testing"

    "github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
    _ "github.com/telepresenceio/telepresence/v2/regression_test/suites/intercept"
    // ... one blank import per suite package
)

func TestMain(m *testing.M)      { rt.Main(m) }
func TestSmoke(t *testing.T)     { rt.RunArea(t, "smoke") }
func TestIntercept(t *testing.T) { rt.RunArea(t, "intercept") }
// ... one per area
```

Selection is plain `go test`:

```
go test ./regression_test -run 'TestIntercept/HeaderFilter/Test_PathPrefix'
```

No `TEST_SUITE`/`-testify.m` indirection. Labels add a second dimension
(`RTEST_LABELS=compat-core`, `RTEST_SKIP_LABELS=slow,sudo`); label filters are
applied by the registry before `suite.Run`, so filtered suites cost nothing.

## The fixture engine (core of the design)

A fixture is a value describing a resource, identified by a canonical spec
hash. The engine provisions on first use, memoizes by hash, and tears down at
end of run (CI) or leaves resources for adoption (dev).

```go
// framework/rt/fixture.go (sketch)
type Fixture[T any] struct {
    Name      string                                  // "manager/node-agent"
    Deps      []AnyFixture
    Hash      func() string                           // identity
    Provision func(ctx context.Context, r *Runtime) (T, error)
    Adopt     func(ctx context.Context, r *Runtime) (T, bool, error) // cross-run
    Destroy   func(ctx context.Context, r *Runtime, v T) error
}

func Get[T any](t testing.TB, f *Fixture[T]) T     // shared use
func Mutate[T any](t testing.TB, f *Fixture[T]) T  // exclusive; invalidates at test end
```

Rules that fix the old framework's problems by construction:

- **Memoization**: two suites declaring `managers.NodeAgent` share one helm
  release. The first `Get` pays; the rest are free.
- **Mutation is explicit**: a test that upgrades the manager, scales a
  workload, or uninstalls an agent calls `Mutate`. The engine invalidates the
  memo entry when the test ends; the next `Get` re-provisions. No
  restore-on-defer conventions, no suite poisoning its successors.
- **Reconfiguration is an upgrade, not a reinstall**: two manager fixtures
  that differ only in values share the helm release name; switching between
  them is one `helm upgrade` in place. Uninstall happens only at final
  teardown.
- **Declared-fixture ordering**: each suite registration declares its primary
  manager spec. The runner sorts suites within an area (and areas within the
  run) to group identical specs, minimizing value-set switches. With the ~15
  distinct manager configs in the catalog this bounds the whole run at roughly
  15-20 helm upgrades, replacing today's 68 + 37 + 14 helm operations.
- **Failure attribution**: `Get` provisions under the calling test's `t`; a
  provisioning failure fails (and skips) exactly the tests that need that
  fixture, marked "fixture <name> failed", instead of failing an unrelated
  harness-level test. Subsequent `Get`s of a failed fixture skip immediately
  (no retry storms).
- No `*testing.T` in contexts. `Runtime` carries config, cluster handle, and
  logging; `t` is always an explicit parameter.

### Concrete fixtures

- `fixtures.Namespace(role)` — app namespaces from a small labeled pool
  (`purpose=tp-rtest`), plus `Private()` variants for suites that need a
  throwaway namespace (namespace-selector semantics, injector policies).
- `fixtures.Manager(spec managers.Spec)` — helm install through the CLI under
  test, values marshaled from a typed struct, keyed by canonicalized values
  hash + managed-namespace set.
- `fixtures.Workload(tpl workloads.Template, ns)` — rendered manifest +
  rollout wait; keyed by (namespace, template, params).
- `fixtures.Connection(mgr, opts)` — a live `telepresence connect` (host or
  `--docker --name <n>`), keyed by manager + flags. Owns quit-on-teardown.
- `fixtures.LocalService(kind, port)` — local echo/h2c/UDP servers.

### Baseline configuration

The framework writes a per-run client config (debug logs, pinned test
timeouts) much like today's `withBasicConfig`. Usage statistics are **off by
default on both sides**: the baseline client config sets
`usage.enabled=false`, and every spec in the manager catalog — including
Default — installs with `usage.enabled=false`. The only tests that enable
usage reporting point `usage.collectorAddress` at a local in-process fake
collector; no test ever reports to a real endpoint.

### Cross-run adoption (dev mode)

State file `build-output/rtest-state.yaml` keyed by cluster server URL +
context, recording release names and spec hashes. On startup in dev mode
(default when not CI), `Adopt` validates cheaply — `helm get values` hash
compare, `kubectl rollout status` — and reuses. `RTEST_FRESH=1` forces
re-provisioning; `make rtest-clean` (or `go test -run TestClean` helper)
removes everything labeled `purpose=tp-rtest`. CI mode always provisions fresh
and always tears down. This is what makes "run one test, tweak code, run it
again" a seconds-to-a-minute loop instead of a full install cycle.

## Suite model

```go
// suites/intercept/headers.go
package intercept

func init() {
    rt.Register(&HeaderFilter{},
        rt.InArea("intercept"),
        rt.NeedsManager(managers.Default),
        rt.Labels(rt.CompatCore))
}

type HeaderFilter struct{ rt.Suite }

func (s *HeaderFilter) Test_PathPrefix() {
    conn := s.Connect(rt.DefaultConn)              // Get(Connection(...))
    wl := s.Workload(workloads.Echo("echo"))       // Get(Workload(...))
    lsvc := s.LocalService(check.EchoServer)       // binds :0, port discovered
    ic := conn.Intercept(wl, cli.ToLocal(lsvc), cli.HTTPPathPrefix("/api"))
    defer ic.Detach()
    s.Check.RoutedToLocal(ic, check.WithPath("/api/x"))
    s.Check.RoutedToCluster(ic, check.WithPath("/other"))
}
```

- `rt.Suite` embeds testify's `suite.Suite` (kept for team familiarity and the
  `Assert`/`Require`/`Eventually` idiom) plus lazy accessors (`s.Connect`,
  `s.Workload`, `s.Manager`) that call `Get` on first use. The rule is:
  **`SetupSuite` must not provision** — accessors are lazy precisely so a
  `-run`-filtered suite costs nothing.
- Registration is one `rt.Register` call with area, declared manager spec, and
  labels. Adding a suite = one file in an area package; adding an area = one
  package + one line in `main_test.go`.
- Labels select *what to run*: `compat-core`, `slow`, `stress`,
  `flaky-retry`. Platform gating is separate and per-test, because most
  tests must be runnable on every platform in dev mode: a suite or test
  declares GOOS sets (`rt.On(...)`/`rt.NotOn(...)`) and capability
  requirements (`rt.Requires(rt.Docker, rt.Sudo, rt.FUSE, rt.Veth)`), and
  the runner self-skips with the unmet constraint as the reason — replacing
  today's ad-hoc `if s.IsCI() && GOOS != ...` checks. CI is Linux-only for
  now, so platform exemptions mainly serve macOS/Windows dev runs.

## CLI invocation layer

- `cli.TP` wraps the binary under test: env-scrubbed subprocess, stdout/stderr
  capture, secret masking — same duties as today's `itest.TelepresenceOk`
  plumbing, minus the context-carried `T`.
- **JSON-first**: functional tests use `--format json` / `--output json` and
  assert on typed structs (`cli.InterceptInfo`, `cli.Status`, `cli.List`).
  Text-output shape is validated once, in dedicated `smoke` suites — not
  re-grepped in every functional test (today `status` output is parsed in 12+
  places, `Using Deployment X` in ~20).
- Raw escape hatch `cli.Run(args...)` remains for error-path and text tests.
- `check/` centralizes the polling assertions (`EventuallyHTTP`,
  `RoutedToLocal`, `PodHasAgent`, `AgentGone`) so timeout policy lives in one
  place.
- **No hard-coded ports**: local listeners (echo/h2c/UDP servers, handler
  processes, in-process daemons) bind port 0 and expose the port the OS
  chose; intercept/ingest specs reference the fixture's actual port. Fixed
  port numbers are reserved for tests whose subject is a specific port (e.g.
  NodePort discovery) and must say so.

## Diagnostics

- Per-test artifact directory `build-output/rtest-logs/<run>/<test>/` with
  client logs, `kubectl get events` (abnormal only), and pod logs captured
  **on failure** (plus opt-in continuous `-f` capture via `RTEST_TAIL_LOGS=1`)
  instead of today's unconditional log-follower goroutines with 2-second
  tickers.
- Deterministic ordering (sorted registries, no map iteration), so a failure
  sequence is reproducible.
- Reporting is a framework concern, not a byproduct of piping `go test -json`
  through `tools/test-report`: the runner emits its own progress lines (per
  fixture and per test) and writes a machine-readable run manifest
  (`build-output/rtest-logs/<run>/manifest.json`) with each test's outcome,
  duration, labels, and artifact paths. `tools/test-report` does not dictate
  the output format — it can be rewritten on top of the manifest, or
  discarded.

## Code coverage

Client side (trivial):

- `make rtest-client` builds `build-output/bin/telepresence` with
  `go build -cover` when `TELEPRESENCE_COVER=1`.
- The framework sets `GOCOVERDIR=build-output/coverage/client` in the child
  environment (the old framework's env whitelist would have silently dropped
  it; the new one passes an explicit, documented env set).

Cluster side (manager + agent images):

- Images built with `-cover` via a Dockerfile build arg.
- In coverage mode the manager fixture adds a `GOCOVERDIR` env and a
  hostPath volume (test clusters are single-node kind/minikube) to the
  deployment via helm values; the agent-injector propagates the same to
  agents. Go's runtime writes counter data on clean exit, and both manager
  and agent shut down gracefully on SIGTERM, so data lands on the node when
  pods are deleted at teardown.
- `cover/` then runs a scraper pod mounting the same hostPath, tars the
  covdata out, and `go tool covdata merge`s client + manager + agent into one
  profile. `make rtest-coverage` prints/exports it; CI uploads it as an
  artifact so regressions in coverage are visible per PR.
- The chart gains a small additive value (e.g. `manager.extraEnv`/
  `extraVolumes`) for this; post-install patching was rejected at review.

## Flag-combination testing

Three tiers, cheapest first:

1. **Golden chart rendering (no cluster)**: `helm template` over a value
   matrix (injector policies x mutation-aware, auth mode x x509, nodeAgent x
   injector, quic service types, workload toggles), asserted against golden
   files / structural invariants. Hundreds of combinations in seconds; this is
   where install-value coverage becomes combinatorial instead of the current
   one-value-per-key testing.
2. **Pairwise live matrices**: `rt.Matrix` generates all-pairs subtests over
   declared axes with constraints, deterministic order:

   ```go
   rt.Matrix(s, "intercept",
       rt.Axis("port", "8080", "local:8080", "local:name"),
       rt.Axis("mount", "false", "auto", "rel-dir"),
       rt.Axis("attach", "intercept", "replace"),
       rt.Axis("handler", "process", "docker-run"),
       rt.Exclude("attach=replace", "handler=docker-run"), // example constraint
   )(func(s *Suite, c rt.Combo) { ... })
   ```

   Pairwise keeps live combinations at O(largest-axis product), not the full
   cross product. Targeted matrices: intercept flags, connect flags, node-agent
   x injector x cluster-default, TLS annotations, auth modes.
3. **Single-shot** tests for flags with one meaningful behavior.

## Bidirectional compatibility subset

- `compat-core` is a **label**, not a directory: the curated tests live in
  their natural suites and are tagged. The catalog (see `suite-catalog.md`)
  covers session lifecycle, all watch streams and their fallback chains,
  intercept/ingest lifecycle, tunnel + DNS, logs, and cluster info — i.e. most
  of the 40 `manager.Manager` RPCs, which is where client/manager skew lives.
- Version selection mirrors the proven `DEV_*` machinery, decoupled:
  `RTEST_CLIENT_VERSION` (released binary downloaded from GitHub releases,
  cached) and `RTEST_MANAGER_VERSION` (released chart pulled from
  `oci://ghcr.io/telepresenceio/telepresence-oss`, image from
  `ghcr.io/telepresenceio`). Unset means "version under test" for both.
- `compat/` provides `ClientVersion()`, `ManagerVersion()`, and
  `MinManager("2.30.0")`-style gates so compat-core tests degrade assertions
  the way `ClientIsVersion`/`AttachVerb` do today, in one place.
- **RPC manifest guard**: a checked-in manifest maps each compat-core test to
  the manager RPCs it exercises. A unit-style test walks the
  `manager.Manager` proto descriptor and fails when a method is neither
  claimed by the manifest nor on the explicit exemption list (agent-only
  RPCs, quicforwarder-only RPCs, dead surface like `GetTelepresenceAPI`). New
  RPCs therefore cannot silently ship without compat coverage.
- CI: a `compat` job runs only `RTEST_LABELS=compat-core` in both directions
  against N-1 (and optionally N-2), with the version derived from the latest
  release tag instead of today's hand-pinned `2.29.3` literal. Because
  compat-core is a subset with high fixture reuse, both directions should fit
  in ~10 minutes each.
- Part of milestone 4 (the one production-code change in this initiative
  besides the coverage chart value): widen the manager's existing
  `COMPATIBILITY_VERSION` / `checkCompat` hook
  (`cmd/traffic/cmd/manager/service.go`) beyond the 2 RPCs it gates today,
  so `Unimplemented` fallback chains can be exercised without installing
  real old images.

## Execution, make targets, CI

- `make check-regression` — full run (`go test ./regression_test/...`) with
  the framework's own progress and summary output; no dependency on
  `tools/test-report`.
- `make rtest-client` / `rtest-images` — build binary/images, optionally with
  cover; `make rtest-clean`; `make rtest-coverage`.
- Env: `RTEST_KUBECONFIG` (default: normal kubeconfig resolution),
  `RTEST_REGISTRY`, `RTEST_{CLIENT,MANAGER,AGENT}_VERSION`, `RTEST_LABELS` /
  `RTEST_SKIP_LABELS`, `RTEST_FRESH`, `RTEST_TAIL_LOGS`. The version under
  test is read from the built binary, removing the mandatory
  `TELEPRESENCE_VERSION` footgun. An optional `rtest.yml` may supply defaults
  but **the shell environment always wins** (the old `itest.yml` precedence
  inversion is a recurring source of confusion).
- CI: Linux runners only for now. One `regression` job initially (side by
  side with the existing integration job); once areas reach parity, shard by area groups (fixture
  declarations make shard planning static). Retries are driven by the run
  manifest: the CI wrapper re-invokes `go test` with `-run` patterns
  generated from the manifest's failures (replacing `test-report -scope`),
  plus an opt-in per-test `flaky-retry` label.

## Deliberately not inherited

- `*testing.T` in `context.Context`; god `Cluster` interface; harness stack
  shared across suites; suffix-keyed grouping; cross-process file mutex;
  `itest.yml`-overrides-shell precedence; unconditional log followers;
  map-order nondeterminism; `TimedRun`'s non-binding timeout; the
  `hello-%d` teardown hardcode; the stray `package integration` file.

## Milestones

1. **Framework core + proof suites.** `rt` runtime, fixture engine (memoize,
   mutate/invalidate, in-place manager upgrade, ordering), `cli`, `check`,
   `managers`/`workloads` catalogs, `smoke` + `intercept` core (the canonical
   attach-mode x workload-kind table). Exit criteria: one scoped test on a
   warm cluster in under a minute; full mini-run green on kind.
2. **Coverage + CI side-by-side.** Cover builds, pod scraping, merged
   profile; `regression` CI job on the existing minikube setup.
3. **Consolidation waves.** Port areas in the order given in
   `suite-catalog.md`, collapsing the documented overlap clusters as we go
   (each wave lists the old files it supersedes).
4. **Compat-core.** Labels, version plumbing, RPC manifest guard, the
   widened `checkCompat` hook in the manager, and the `compat` CI job in
   both directions.
5. **Matrices.** Golden chart rendering + pairwise live matrices.
6. **Test-author guide.** Distill the durable, author-facing content of this
   plan and `suite-catalog.md` into `regression_test/README.md`: the fixture
   engine rules (`Get`/`Mutate`, lazy accessors, no provisioning in
   `SetupSuite`), how to add a suite or area, the manager and workload
   catalogs, labels and platform constraints, env vars and make targets, the
   port and usage-statistics policies, and the compat version gates. Replace
   the body of the "Testing" section in `AGENTS.md` (the file `CLAUDE-md`
   links to) with a short summary that references this guide; while
   `integration_test/` still exists, the guide keeps a brief legacy section
   on running it. The guide — not this plan — is the lasting documentation:
   seed it in milestone 1 and keep it current through every wave.
7. **Parity audit + retirement.** Coverage-diff against the old suite per
   area; retire old suites area by area; optional parallelism for read-only
   suites.

## Decisions from review (2026-07-30)

- Names accepted (`regression_test/`, framework package `rt`); the test
  entrypoint file is `main_test.go`.
- testify suites are kept.
- The coverage chart addition (`extraEnv`/`extraVolumes`) is approved.
- Dev mode defaults to adopt-and-keep resources.
- The `checkCompat` widening is folded into milestone 4.
- No hard dependencies on port numbers: bind port 0 and discover; fixed
  ports only where the port itself is the test subject.
- Platform gating is per-test exemption (GOOS sets + capability
  requirements), not a blanket `linux-only` label; most tests must run on
  all platforms in dev mode. CI is Linux-only for now.
- Usage statistics are off by default in both the client and the cluster;
  the tests that cover usage reporting use a local collector server.
- The author-facing content of this plan is captured in
  `regression_test/README.md` (milestone 6), and the "Testing" section body
  in `AGENTS.md` is replaced by a reference to it.

## Framework note: Mutate is single-shot per test

`rt.Mutate` only re-provisions a fixture once the test that Mutated it ends
(by design: it marks the memo entry for invalidation in `t.Cleanup`). A
second `rt.Mutate` on the same fixture hash within the same test is a cache
hit, not a fresh provision. Code that must reconnect mid-test (after
quitting a connection, or after a manager release change whose cluster-info
a stale session won't reflect) uses `rt.Reconnect`, which bypasses the memo
and always issues a fresh `connect`. See also findings.md's product-gap
notes, which `rt.RestartManager` works around similarly.
