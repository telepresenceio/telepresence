---
name: integration-tests
description: Run, scope, or debug telepresence integration tests under integration_test/. Use when the user wants to run a suite or single test, debug a failing integration test, or says "/integration-tests". Runs `make check-integration` scoped with TEST_SUITE/TEST_NAME, in the background, writing to a log file so heavy output stays out of context.
---

# integration-tests

Runs telepresence integration tests from the main conversation. (Replaces the
former `integration-test-runner` subagent: kept in the main thread so the env
vars and scoping are set here, where this harness's shell-env quirks are known.)

## Background to assume
- Tests live under `integration_test/` (testify suites) and need a working k8s
  cluster (kind / minikube / Docker Desktop) plus images it can reach. For a
  local cluster, set `TELEPRESENCE_REGISTRY=local` and LOAD images into it
  (`make load-tel2-image`, plus `make client-image` for `--docker`) rather than
  pushing to a registry — see Workflow step 2.
- Harness is in `integration_test/itest/`. Env vars are documented in CLAUDE.md
  under "Integration Test Environment Variables".
- `itest.yml` (next to the telepresence `config.yml`) pins
  `TELEPRESENCE_VERSION` / `TELEPRESENCE_REGISTRY` and wins over shell env for
  the test harness.

## Always run via `make check-integration`

It builds prerequisites and runs `go test -json ./integration_test/... |
test-report`, which renders a live, readable log. Do not hand-roll raw `go test`.

## CRITICAL: pass scoping/config as MAKE ARGUMENTS, not shell env

In this harness, shell `export`s do NOT persist across separate Bash calls, and
inline `VAR=value` shell prefixes are disallowed. Pass everything as make
command-line arguments instead — make exports command-line variables into the
recipe's environment, so they reach the `go test` / `test-report` process:

```
make check-integration \
  TEST_SUITE='^MySuite$$' \
  TEST_LOG_OUTPUT=/tmp/itest-mysuite.log
```

- `TEST_SUITE` / `TEST_NAME` are regexps. **Double the trailing `$` anchor** —
  make eats a single `$`, so `'^MySuite$'` becomes `^MySuite` (anchor lost); use
  `'^MySuite$$'`. `TEST_SUITE` filters suite names; `TEST_NAME` becomes
  `-testify.m`. Combine them to pin one test in one suite.
- `TEST_LOG_OUTPUT` → a FRESH, run-specific path; `rm -f` it first (it is opened
  append-mode). `test-report` writes the rendered log here, NOT to stdout.
- `check-integration` builds **no image** (the `client-image` prereq was removed),
  so it does NOT need `TELEPRESENCE_VERSION`/`TELEPRESENCE_REGISTRY` — the test
  harness reads version/registry from `itest.yml`. Pass those vars only to the
  build/load commands (step 2), matching `itest.yml`.

## Keep heavy output out of context

This runs in the main thread, so do NOT Read or `tail` the whole log. Instead:

1. Launch the `make` command with `run_in_background: true`.
2. On completion (or to peek), Read ONLY summary lines from `$TEST_LOG_OUTPUT`:
   `grep -E '^(---|    ---) (PASS|FAIL|SKIP):' /tmp/itest-mysuite.log`
   (the bare word `FAIL` also appears in DEBUG lines — match the `--- FAIL:`
   form). The top-level result is `--- PASS:` / `--- FAIL: Test_Integration`.

## Reading the log

Output streams within seconds once the test phase starts. A persistently empty
`$TEST_LOG_OUTPUT` after the test phase has begun is a problem (stale lock, wrong
path, or stuck run) — investigate, don't wait it out.

## Stale lock / leftover state

If a prior run was killed, before re-running:
- `telepresence quit -s`
- `rm -f /tmp/telepresence-itest.lock /tmp/datawire-machine-scoped-default.lock`
- A leftover traffic-manager ("telepresence-oss ... is already installed in
  namespace <ns>") → `helm uninstall traffic-manager -n <ns>` and delete the ns.

## Workflow

1. **Identify** the suite/test: Grep `integration_test/` to map a feature to a
   suite (`<feature>_test.go`, `SuiteName()`) and/or a `Test_` method.
2. **Decide rebuild.** The harness runs the prebuilt host binary plus the
   cluster-side images, so rebuild whatever changed. These build/load commands
   need `TELEPRESENCE_VERSION` matching `itest.yml` (the image tag is
   `registry/name:version`) and, for a local cluster (kind / minikube / Docker Desktop),
   `TELEPRESENCE_REGISTRY=local` — then LOAD images into the cluster instead of
   pushing to a registry:
   - client-side Go (`pkg/`, `cmd/cli`):
     `make build TELEPRESENCE_VERSION=<itest.yml>` — else the harness runs a
     stale binary (e.g. missing a newly added flag).
   - manager/agent (`cmd/traffic`, `charts/`):
     `make load-tel2-image TELEPRESENCE_VERSION=<itest.yml> TELEPRESENCE_REGISTRY=local`.
   - `--docker` tests only:
     `make client-image TELEPRESENCE_VERSION=<itest.yml> TELEPRESENCE_REGISTRY=local`
     (the daemon container runs on the workstation via docker, so it only needs
     to exist locally — no cluster load).
   `local` makes the harness use `pullPolicy=Never`. Reserve `make push-images`
   (and a real `TELEPRESENCE_REGISTRY`) for a remote cluster that cannot load
   local images.
3. **Run** scoped (background, fresh `TEST_LOG_OUTPUT`).
4. **Summarize:** command run, pass/fail/skip counts, failing test names, the
   smallest log excerpt explaining each failure, and the next concrete action.

## Don't

- Don't run the full suite unscoped without explicit user instruction — it is
  slow and `-failfast` aborts everything on the first failure.
- Don't `make clobber` or destroy local images without asking.
- Don't edit generated files: `docs/reference/cli/**`, `DEPENDENCIES.md`,
  `DEPENDENCY_LICENSES.md`, `docs/release-notes*`.
