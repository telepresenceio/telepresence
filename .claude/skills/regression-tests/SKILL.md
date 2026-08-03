---
name: regression-tests
description: Run, scope, or debug telepresence regression tests under regression_test/ — the integration-level suite. Use when the user wants to run an area, suite, or single test, debug a failure, or says "/regression-tests". Runs `go test ./regression_test` scoped with -run, in the background, writing to a log file so heavy output stays out of context.
---

# regression-tests

Runs the telepresence regression suite from the main conversation, where this
harness's shell-env quirks are known.

## Background to assume

- Tests live under `regression_test/` and need a working k8s cluster (kind /
  minikube / Docker Desktop) plus images it can reach. For a local cluster set
  `RTEST_REGISTRY=local` and LOAD the images into it rather than pushing.
- `regression_test/README.md` is the reference: fixture-engine rules, the
  RTEST_* table, catalogs, labels, coverage. Read it before debugging a
  fixture problem.
- **The shell environment always wins; there is no config file.** That means a
  stale `export` in the user's shell silently changes a run.

## Scoping: plain `go test -run`

Areas are ordinary Go tests, suites and methods are subtests, so one `-run`
expression selects at any depth:

```
go test ./regression_test -run '^TestIntercept$'
go test ./regression_test -run '^TestIntercept$/^HeaderFilter$'
go test ./regression_test -run '^TestIntercept$/^HeaderFilter$/^Test_PathPrefix$'
```

There is no `TEST_SUITE`/`TEST_NAME` indirection and no make-argument dance:
inline `VAR=value` prefixes work in this harness, so run `go test` directly
and keep `make check-regression` for the full unscoped suite.

Always pass `-count=1` (results must never come from the test cache) and a
`-timeout` that fits the scope: minutes for one suite, `-timeout=100m` for a
full run.

## The run command

```
TELEPRESENCE_REGISTRY=local TELEPRESENCE_VERSION=<version> \
  RTEST_CONTEXT=<context> RTEST_TEARDOWN=1 \
  go test -count=1 -timeout=30m -run '^TestArea$/^Suite$' ./regression_test \
  > /tmp/rtest-suite.log 2>&1
```

- `RTEST_CONTEXT` pins the kube context. Pin it explicitly whenever the
  machine has more than one cluster — the default is the kubeconfig's current
  context, which is one `kubectx` away from being the wrong cluster.
- `RTEST_TEARDOWN=1` destroys the run's resources at the end. Without it, dev
  mode keeps namespaces, the release, and the connection for the next run to
  adopt, which is what makes a scoped rerun take seconds.
- `RTEST_FRESH=1` ignores adoptable resources (use when a previous run left
  something suspect). CI implies fresh + teardown.
- `RTEST_LABELS` / `RTEST_SKIP_LABELS` select on `compat-core`, `slow`,
  `stress`, `flaky-retry`.

## CRITICAL: a stale TELEPRESENCE_VERSION silently poisons the build

The version under test is read from the binary itself, and `make build` stamps
it from `TELEPRESENCE_VERSION`. If the user's shell exports an old value, the
binary gets that version, the manager image tag no longer matches, and the run
fails at image pull with a version that appears nowhere in your command.

Always pass `TELEPRESENCE_VERSION` explicitly to BOTH `make build` and the
`go test` invocation, with the same value.

## Rebuild before running

The suite runs the prebuilt binary plus the cluster-side images, so rebuild
whatever changed:

- client-side Go (`pkg/`, `cmd/telepresence`) **and any `charts/` change**:
  `make build` — the chart is go:embedded in the client binary, so a
  chart-only edit without a rebuild silently installs the OLD chart.
- manager / agent (`cmd/traffic`, `charts/`): `make load-images` (or
  `make load-tel2-image`), so the cluster gets the new image.
- `--docker` tests: `make client-image` — the daemon container runs on the
  workstation, so it only needs to exist locally.

`RTEST_REGISTRY=local` makes the manager use `pullPolicy=Never`. Reserve
`make push-images` and a real registry for a remote cluster.

## Keep heavy output out of context

This runs in the main thread, so do NOT Read or `tail` the whole log.

1. Launch with `run_in_background: true`, redirecting to a fresh path.
2. On completion, read only the summary:
   `grep -E 'passed,|^ok|^--- FAIL|^FAIL' /tmp/rtest-suite.log | tail -5`
   The runner's own last line is
   `[rtest] run <stamp>: N passed, N failed, N skipped (N fixture actions)`.
3. For a failure, the per-test artifacts are under
   `build-output/rtest/logs/<stamp>/<TestPath>/` (cli.log, daemon logs), and
   `manifest.json` records every test's outcome and labels.

## Leftover state

- `make rtest-clean` removes everything the framework created, in the cluster
  and locally.
- A daemon from a killed run: `telepresence quit -s`.
- One rtest run per cluster at a time — resource names are stable by design,
  which is what makes adoption work.
- A leftover manager from unrelated work ("traffic-manager in namespace X
  already manages namespace Y") blocks an unrestricted install: find it with
  `kubectl get secret -A -l owner=helm`, and remember the chart also leaves
  cluster-scoped resources (webhook config, ClusterRole/Binding) that
  namespace deletion does not remove.

## Workflow

1. **Identify** the area/suite: areas are directories under
   `regression_test/suites/`, and each suite is an `rt.Suite` type registered
   in its `init()`.
2. **Decide the rebuild** (see above) and run it with an explicit
   `TELEPRESENCE_VERSION`.
3. **Run scoped**, in the background, to a fresh log path.
4. **Summarize:** the command, pass/fail/skip counts, failing test names, the
   smallest excerpt explaining each failure, and the next concrete action.

## Don't

- Don't run the full suite unscoped without explicit user instruction — it is
  roughly an hour.
- Don't run `go test -list` or a deliberately non-matching `-run` to "check
  what exists": the harness provisions real cluster resources before selection,
  so it costs a full setup cycle. Grep the suite files instead.
- Don't `make clobber` or destroy local images without asking.
- Don't edit generated files: `docs/reference/cli/**`, `DEPENDENCIES.md`,
  `DEPENDENCY_LICENSES.md`, `docs/release-notes*`.
