# The regression test framework

`regression_test/` protects the code base from regressions. It drives the
real `telepresence` CLI against a real cluster, and is built around
declarative, memoized fixtures: a test declares the resources it needs,
the framework provisions each one on first use, reuses it for every later
test that declares the same spec, and (in dev mode) adopts what a previous
run left behind. A single scoped test on a warm cluster runs in seconds.

## Quick start

```bash
# Once: build the client (embeds the helm chart!) and the manager image,
# and make the image reachable (kind shown; minikube: minikube image load).
TELEPRESENCE_VERSION=v2.x.x-test.0 TELEPRESENCE_REGISTRY=local \
  make build tel2-image client-image
kind load docker-image local/tel2:2.x.x-test.0 --name <cluster>

# Everything:
RTEST_KUBECONFIG=~/.kube/my-test-cluster.yaml RTEST_REGISTRY=local \
  make check-regression

# A third of it: three area shards balanced by duration (mapping in
# build-aux/main.mk). One rtest run per cluster at a time, so parallel
# shards need a cluster (RTEST_CONTEXT/RTEST_KUBECONFIG) each; CI gives
# every shard its own runner.
make check-regression SHARD=2

# One area, one suite, or one test:
go test ./regression_test -run '^TestIntercept$'
go test ./regression_test -run 'TestIntercept/HeaderFilter'
go test ./regression_test -run 'TestIntercept/HeaderFilter/Test_PathPrefix'

# Remove everything the framework created (cluster + daemons):
make rtest-clean
```

The client binary must be rebuilt (`make build`) after changing client code
**or the chart** — the chart is embedded in the binary. The manager image
must be rebuilt and re-loaded after changing `cmd/traffic` code.

## Environment

The shell environment always wins; there is no config file.

| Variable | Effect | Default |
|---|---|---|
| `RTEST_KUBECONFIG` | kubeconfig for the run | standard resolution |
| `RTEST_CONTEXT` | kube context | kubeconfig current context |
| `RTEST_REGISTRY` | manager/agent image registry; `local` => pullPolicy Never | `$TELEPRESENCE_REGISTRY`, else ghcr.io/telepresenceio |
| `RTEST_EXECUTABLE` | client binary under test | `build-output/bin/telepresence` |
| `RTEST_CLIENT_VERSION` | compat: released client (downloaded + cached) | version under test |
| `RTEST_MANAGER_VERSION` | compat: released chart/manager from the public registry | version under test |
| `RTEST_MANAGER_REGISTRY` | registry for a pinned manager version | ghcr.io/telepresenceio |
| `RTEST_LABELS` / `RTEST_SKIP_LABELS` | comma lists, ANY-match select/deselect | unset |
| `RTEST_FRESH=1` | ignore adoptable resources | off |
| `RTEST_TEARDOWN=1` | destroy resources at run end even in dev mode | off |
| `RTEST_COVER=1` | coverage mode (see Coverage) | off |

CI (`GITHUB_ACTIONS=true`) implies fresh + teardown. The version under test
is read from the binary itself; `TELEPRESENCE_VERSION` overrides.

## The fixture engine

A fixture (`rt.Fixture[T]`) is a value describing one resource, identified
by a canonical spec hash. `rt.Get(t, f)` returns the memoized value or
provisions under the calling test's `t`; a provisioning failure fails that
test and turns every later `Get` of the same fixture into a skip.
`rt.Mutate(t, f)` is `Get` plus invalidation when the test ends: the next
`Get` re-provisions. Provisioning must converge from any prior state
(install-or-upgrade, apply + rollout, quit + connect).

Rules that hold the whole thing together:

- **Suites never provision in `SetupSuite`.** The `rt.Suite` accessors
  (`s.Manager()`, `s.Connect()`, `s.Workload(tpl)`, `s.LocalEcho()`) are
  lazy; a `-run`-filtered suite costs nothing.
- **Declare your manager spec.** `rt.NeedsManager(spec)` does two things:
  the runner groups suites by spec so switching (an in-place
  `helm upgrade --reset-values` of the ONE shared release) is minimized,
  and `SetupTest` guarantees the declared spec is installed before every
  test — an earlier suite may have left another spec active.
- **Mutation is explicit.** Anything that changes the shared release
  (`rt.Mutate(t, rt.ManagerFixture(otherSpec))`), the default connection,
  or a shared workload must go through `Mutate`, or later tests inherit
  your leftovers. `rt.Reconnect(t, ctx, ns)` covers the same-test
  reconnect that `Mutate`'s end-of-test invalidation cannot express.
- **Manager specs are mutually exclusive.** Provisioning any spec
  invalidates every other spec's memoized handle AND every connection
  (the rollout replaced the pod each session port-forwards to). The
  provision also waits until the old manager pod is fully gone: webhook
  admissions and fresh service-to-pod resolutions can otherwise land on
  the terminating pod with its pre-rollout state.
- **Connections.** The host daemon is a process singleton: provisioning a
  host connection always quits whatever ran before it; named connections
  (`rt.ConnNamed`, with or without `rt.ConnDocker`) quit only their own
  session (`quit -s` stops ALL daemons and ignores `--use` — never use it
  for one connection). Adoption of a running daemon happens only for the
  plain default connection, and never after the release has rolled within
  the run.
- **Workloads are always destroyed at run end**, even in dev mode: they
  are cheap to recreate, and keeping dozens of them walks the node into
  kubelet's pod limit. A workload whose manifest changed is deleted and
  recreated — the manager preserves a workload's existing agent config
  across template edits (findings.md #3), so apply alone does not converge.
- **Dev keep + parking.** Without `RTEST_TEARDOWN`, namespaces, the
  release, and the connection stay for the next run's adoption, and the
  release is parked back on the Default spec (a release left on a spec
  that references destroyed namespaces crashloops). Private namespaces
  (`rt.PrivateNamespace` / `rt.PrivateUnmanagedNamespace`) and
  SecondaryManager releases are always destroyed; secondary teardown also
  removes the cluster-scoped webhook configuration, which namespace
  deletion alone leaves behind.

## Adding a suite

```go
package myarea

func init() {
    rt.Register(&MySuite{},
        rt.InArea("myarea"),
        rt.NeedsManager(managers.Default),
        rt.WithLabels(rt.CompatCore),          // selection labels
        rt.Requires(rt.Docker), rt.On("linux"), // platform constraints
    )
}

type MySuite struct{ rt.Suite }

func (s *MySuite) Test_Something() {
    t := s.T()
    conn := s.Connect()
    wl := s.Workload(workloads.Echo("my-echo"))
    ls := s.LocalEcho()
    a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
    defer a.Detach(t)
    rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}
```

A new area additionally needs a blank import and a `Test<Area>` entry in
`main_test.go` (area order matters: areas that churn connections or the
release spec run after the ones that depend on stability).

Labels (`compat-core`, `slow`, `stress`, `flaky-retry`) select; platform
gating is separate: GOOS sets (`rt.On`/`rt.NotOn`) and capabilities
(`rt.Requires(rt.Docker | rt.Sudo | rt.FUSE | rt.Veth)`) self-skip with the
unmet constraint named. Most tests must run on every platform in dev mode;
exemptions attach to the specific tests that need them. CI is Linux-only
for now.

## Catalogs

- `managers.Default` plus parameterized specs (`NodeAgent()`,
  `InjectorDisabled()`, `InjectPolicy(p)`, `CertRegen(m)`,
  `StaticNamespaces(ns...)`, `QuicNodePort()`, `AuthEnforcing()`,
  `UsageTo(addr)`, `ClientConfig(key, values)`, `Compat(version)`).
  Every spec is Baseline + overlay; Baseline pins debug logging, the image
  coordinates, test RBAC, and `usage.enabled=false`. Usage statistics are
  off by default on both sides; the only test that enables them points the
  collector at an in-process local server (`rt.NewUsageCollector`).
- `workloads.Echo`, `EchoStatefulSet`, `EchoHeadless` (numeric targetPort:
  the init-container interception path pod-IP traffic needs),
  `EchoNoService` (carries the inject-container-ports annotation),
  `EchoMultiPort`, `EchoReplicas`, `EchoWithConfigVolume`; templates take
  `Annotations`, `Resources`, `AppProtocol`.
- **No hard-coded ports.** Local listeners bind `:0` (`s.LocalEcho()`);
  the only fixed numbers are external facts (the echo image's 8080, a
  NodePort under test) and must say so.

## Compat

`RTEST_LABELS=compat-core` with `RTEST_MANAGER_VERSION` or
`RTEST_CLIENT_VERSION` runs the subset bidirectionally; CI's
`regression_compat` job does both against the latest release tag. The
labeled tests collectively cover the client-facing `manager.Manager`
surface — `regression_test/framework/compat/manifest_test.go` fails when
an RPC is neither claimed nor exempted-with-reason, so new RPCs cannot
ship without compat coverage. `compat.MinManager(t, ">=2.30.0")`-style
gates skip feature tests the counterpart lacks. The `CompatSim` test
installs the built manager with `compatibility.version=2.21.0`, proving
every `Unimplemented` fallback chain without real old images.

## Coverage

`TELEPRESENCE_COVER=1 make build tel2-image` instruments both binaries;
`RTEST_COVER=1` runs point `GOCOVERDIR` at
`build-output/rtest/coverage/client`, add a hostPath covdata volume to the
manager (whose injector propagates the same wiring into every generated
traffic-agent and init container), quit the daemons at run end (counters
flush on exit), scrape the cluster's covdata through a throwaway pod after
fixture teardown, and `make rtest-coverage` merges everything into one
report.

## Areas

smoke (output shapes), connect (lifecycle, errors, contexts, multi),
attach (verb x workload-kind matrix + conflicts), intercept (filters,
flags incl. the pairwise matrix, routing), install (helm semantics, setup
verb, podCIDRs), injector (policies, certs, LimitRange, manual agent),
namespaces (selector, static list, mapped), dns, routing (proxy-via,
conflicts, never-proxy), mounts (FUSE/FTP content, ignore annotation,
podscaling), docker (coexistence, cache files, docker-run, REST API;
compose deferred), session (served client config, gather-logs, usage,
workload watch, compat sim), nodeagent, auth, quic. Chart-value
combinations are additionally covered clusterless by
`go test ./regression_test/golden`.

## Debugging

Artifacts land in `build-output/rtest/logs/<runid>/`: `run.log`,
`manifest.json` (per-test outcome/duration/labels), the manager values
files, applied manifests, and on failure the abnormal events + daemon log
tails per test. Daemon state lives under `build-output/rtest/home/`; to
poke at a kept session manually:

```bash
export DEV_TELEPRESENCE_CONFIG_DIR=$PWD/build-output/rtest/home/config \
       DEV_TELEPRESENCE_LOG_DIR=$PWD/build-output/rtest/home/logs
build-output/bin/telepresence status
```

One rtest run per cluster at a time: resource names are stable by design
(that is what makes adoption work).
