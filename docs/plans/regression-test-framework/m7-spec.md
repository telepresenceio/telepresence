# M7 spec (parity audit + retirement)

Goal: establish, per area, that `regression_test/` actually covers what
`integration_test/` covers; retire the old suites area by area as each is
proven; and leave CI running one integration-level suite instead of two.

The milestone is finished when `integration_test/` is gone, the
`build_and_test` job runs `regression` alone, and the framework's own
`README.md` is the only test-authoring documentation.

## Why now

Two facts, both measured on 2026-08-02:

| | `integration_test/` | `regression_test/` |
|---|---|---|
| CI wall clock | 69 min test phase (74m48s / 74m58s whole job) | 40.6 min (2434s) |
| Timeout budget | `-timeout=80m`, ~15% headroom | `-timeout=60m`, ~33% headroom |
| Result that day | timed out, 13 failures, retried unscoped | 116 passed, 0 failed, 2 skipped |

The old suite ran out of its 80-minute budget on a normal PR, and because a
timeout writes an incomplete failures document (`complete: false`), the retry
wrapper could not scope the retry and re-ran everything — a job that would
have taken over four hours had it not been cancelled. That is not a flake to
be tuned away: a 69-minute suite under an 80-minute cap has no room left, and
every suite added makes it worse. Retirement removes the problem rather than
raising the number, and `check-integration-retry.sh` retires with it.

Keeping both suites also means every behavioural change is asserted twice, in
two idioms, with the old one being the slower and less diagnosable of the two.

## The parity ledger

Raw test counts must not be used as the parity measure. The old suite has 240
test methods against the new suite's 118, but the catalog deliberately
collapses twelve documented overlap clusters — the basic intercept happy path
alone is re-validated in roughly 20 old suites and once in `attach/Modes`. A
count comparison would argue for porting duplication back in.

The measure is **behavioural**: for each old file, every distinct assertion it
makes is either (a) asserted by a named new test, (b) asserted by a unit test,
or (c) explicitly dropped with a recorded reason. The ledger is a checked-in
table, one row per old file:

```
| old file | old tests | covered by | verdict |
|---|---|---|---|
| workloads_test.go | 10 | attach/Modes matrix cells … | superseded |
| otel_test.go | 3 | — | dropped: env-gated stress, never run in CI |
```

`suite-catalog.md` already carries the *claims* (a "Supersedes" column naming
74 of the 81 old files). M7 is where each claim is **verified** rather than
asserted — the claims were written before the suites existed, so several will
turn out to be partial.

Work per area: read the old file, list its assertions, find them in the new
suite, and record the verdict. Where a claim proves partial, either extend the
new suite in the same change or move the row to a gap list. An area is
retirable when every one of its old files has verdict `superseded` or
`dropped`, and the ledger says why.

Suggested split, following the wave order the port itself used, so each agent
reads one coherent area: (1) smoke, connect, intercept, attach; (2) install,
injector, namespaces; (3) nodeagent, quic, auth, session; (4) dns, routing,
mounts, docker, state.

## Unclaimed files

Seven old files are named by no new suite. Each needs a verdict before its
area can retire:

| file | tests | assessment |
|---|---|---|
| `integration_test.go` | 1 | the `Test_Integration` entrypoint; disappears with the package |
| `single_service_test.go` | 0 | suite scaffolding only, no tests of its own |
| `container_test.go` | 2 | `--container` intercept of a specific container in a multi-container pod. **Not covered** — the new suites use `--container` nowhere. Port into `intercept/Flags` as a matrix axis, or as one test |
| `workload_configuration_test.go` | 4 | `telepresence.io/enabled=false` on Deployment vs ReplicaSet vs StatefulSet, and intercepting the enabled one of a disabled pair. **Not covered.** Natural home is `injector/` |
| `intercept_env_test.go` | 1 | `--env-excludes` variable filtering. Likely folds into `intercept/Flags`; confirm the flag is exercised there |
| `udp_test.go` | 1 | general UDP echo through the TUN. `quic/Datagrams` covers UDP over QUIC datagrams, which is a different path — confirm whether the non-QUIC case is covered by `routing/` or needs porting |
| `istio_test.go` | 2 | ServiceEntry resolution against a meshed workload (issue #2717). Requires an Istio install with DNS capture and self-skips when absent, so it has probably never run in CI. Decide: port with a capability gate (`rt.Requires`), or drop with that reason recorded |

The `container_test.go` and `workload_configuration_test.go` gaps are real
coverage that would be lost on retirement; they are the only two that must be
written before their areas can go.

## Retirement mechanics

Per area, in one commit:

1. Delete the superseded `integration_test/*_test.go` files and any testdata
   only they used.
2. Remove the area's rows from `suite-catalog.md`'s supersession tables and
   add them to the ledger with their verdicts.
3. Run both suites once more before deleting, so the ledger's claims are
   backed by a green run of the new one.

Ordering is by confidence, not by wave: areas whose new suites have the
longest green history retire first, and `attach` — the biggest single
collapse, roughly 20 old suites into one matrix — retires last so its ledger
gets the most scrutiny.

Two directories are shared infrastructure rather than tests and go only when
the package does: `integration_test/itest/` (the old harness) and
`integration_test/testdata/`.

`regression_test/` has **no code dependency** on either — there is not one Go
import of `integration_test/...` anywhere under `regression_test/` — so
deletion is not blocked. What it does have is 35 provenance comments across
34 files, citing old files by path and line to explain where a behaviour or
a timeout value came from (`framework/rt/download.go`'s
"integration_test/itest/cluster.go:327's downloadBinary", `suites/attach/cell.go`'s
citation of the wiretap tests, and so on). Every one dangles the moment the
package is deleted.

Repointing them is part of retirement, not a follow-up, and per AGENTS.md a
comment should describe the code as it is rather than what it was ported
from: most of these should simply lose the citation, keeping whatever
constraint they were explaining. The two `README.md` mentions of the legacy
suite go at the same time. Grep `integration_test/` under `regression_test/`
before declaring an area done.

## CI transition

Ordered so CI is never without an integration-level gate:

1. While retiring, `build_and_test` keeps running whatever remains of
   `integration_test/`. Its wall clock falls with each area removed, which
   also relieves the timeout pressure that caused tonight's failure.
2. When the package is empty, delete the `build_and_test` job, its
   `check-integration*` make targets, `build-aux/check-integration-retry.sh`,
   and the `ok to test` gating that exists to make the expensive job
   opt-in for forks.
3. Promote `regression` to a required status check in branch protection, and
   drop `build_and_test (ubuntu-latest)` from the required contexts. **This is
   a repo-settings change, not a code change** — it needs an admin and must
   happen in the same window as step 2, or PRs will block on a context that
   can never report.
4. `regression_compat` (the bidirectional compat job) is unaffected.

Note for step 2: the required-context list is also spelled out inside
`dev.yaml`'s `preflight` job, which waits on it. That list must be updated in
the same commit — and while it is being touched, the docs-only defect found
on 2026-08-02 should be fixed with it: a skipped matrix job reports one bare
context (`unit`), not the per-matrix names `preflight` waits for, so every
docs-only PR times out `preflight` after 35 minutes and cannot satisfy its
required contexts without an admin override.

## Parallelism

Explicitly optional, and last. The plan's target is 25-30 minutes serial; the
suite is at 40.6 minutes, so parallelism is worth having but is not what makes
this milestone succeed. Retirement alone removes 69 minutes of CI.

If attempted, the constraint is the fixture engine: `engine` is mutex-guarded
and safe for concurrent `Get`, but the resources are not. A suite that
`Mutate`s the shared manager, or that reconfigures the release in place, must
not run beside anything else — and the manager release is shared by
construction (that is what makes the run fast). The tractable subset is
suites that only `Get` fixtures and only read: `smoke`, most of `dns`, parts
of `routing`. That is a small share of 40 minutes, so measure the ceiling
before building the machinery.

The M1 contract states the run is serial and several framework comments rely
on it (`ensureHostDaemon`'s "single, serial test run; see M1 contract",
`activeHostConfigDir`'s missing lock). Any parallelism work must revisit
those, not just add `t.Parallel()`.

## Exit criteria

1. A checked-in ledger accounting for all 81 old files, each `superseded`
   (naming the new tests) or `dropped` (with a reason).
2. `--container` and `telepresence.io/enabled=false` coverage exists in
   `regression_test/`, and the `udp`/`intercept_env`/`istio` verdicts are
   recorded.
3. `integration_test/` is deleted, along with its harness, testdata, make
   targets, and retry wrapper.
4. `build_and_test` is gone; `regression` is a required check; `preflight`'s
   required list matches branch protection.
5. `regression_test/README.md` documents no legacy suite, `AGENTS.md`'s
   Testing section references only the new guide, and no comment under
   `regression_test/` cites an `integration_test/` path.
6. A full CI run is green with the old job absent.

## Not in this milestone

- Sharding `regression` by area group. The plan mentions it; it only pays off
  once the suite is the sole integration gate and its duration is the whole
  critical path.
- Rewriting `tools/test-report`. The regression runner emits its own progress
  and manifest, so the tool retires with `check-integration*` rather than
  being ported.
