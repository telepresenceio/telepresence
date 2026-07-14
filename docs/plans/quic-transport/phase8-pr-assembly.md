# Phase 8: final verification and PR assembly

Read `README.md` in this directory first. **Depends on every other plan in
this directory being complete** — the observable precondition is that this
directory contains only `design.md`, `README.md`, and this file (each work
item's concluding commit deleted its own plan).

## Preconditions checklist

* [ ] Phase 7 (zero-config discovery) landed; `quicTunnel.enabled=true`
      alone works on kind with NodePort.
* [ ] UDP datagrams landed (client↔manager hybrid carriage).
* [ ] Pipelined stream setup: either landed or its negative result is
      recorded in `design.md` (both are valid completions).
* [ ] Connection migration: NAT-rebind test in
      `cmd/traffic/cmd/quicforwarder` passing; docs claim matches evidence.
* [ ] Relay hardening: re-probe landed and
      `Test_ZManagerOutageAttachmentSurvival` asserts QUIC recovery; GRO
      measured (kept or dropped, either with numbers).
* [ ] Session resumption landed (after re-probe).
* [ ] `git log thallgren/v2.30.0..HEAD` reads as a coherent narrative of
      logical, individually buildable commits. Reorganize only if something
      is actually broken (a fixup commit that belongs in its parent);
      otherwise leave history alone.

## Full verification (in this order, all green before touching the PR)

1. `go build ./...` and `make check-unit`.
2. `golangci-lint run ./...` **plus** `golangci-lint run --build-tags perf
   perf/` (the plain run silently skips the harness), `gofumpt -l` clean.
3. Proto hygiene: `make protoc` produces no diff; `protolint` green.
4. Integration, scoped first:
   `make check-integration TEST_SUITE='^QuicTunnel$$'
   TEST_LOG_OUTPUT=/tmp/itest-quic-final.log` (both QUIC suites).
5. Integration, full: this phase is the explicit instruction the workflow
   docs require before running the whole suite un-scoped. Expect it to be
   slow and `-failfast`; run it in the background and read only the
   PASS/FAIL summary lines. `pkg/tunnel` changes touch every transport, so
   a scoped-only run is not sufficient here.
6. Perf confirmation: one experiment-1 run per `perf/README.md`
   (`PERF_KUBE_CONTEXT`, `PERF_QUIC_EXTERNAL_HOST`, `PERF_IMPAIR_NODE`
   against a kind cluster). The head-of-line assertion must pass; record
   the numbers — they go in the PR description.
7. Dangling-reference sweep: `rg -n 'plans/quic-transport' --glob
   '!docs/plans/**'` finds every code/chart/proto comment that cites
   `design.md` (there are 35+, spread across phases 1-6). The final commit
   deletes that file, so each reference must be rewritten first: retarget it
   to the section of `docs/reference/quic-transport.md` that carries the
   surviving explanation, or make the comment self-contained. Move any
   design.md content a comment depends on into the reference doc rather than
   losing it. Proto comment changes need `make protoc` to sync `.pb.go`.
8. Docs build/consistency pass: `docs/howtos/quic-transport.md` and
   `docs/reference/quic-transport.md` must describe the *final* behavior
   (zero-config happy path, datagram carriage and its agent-path exception,
   migration, node tuning). Do not edit generated files
   (`docs/reference/cli/**`, `docs/release-notes*`, `DEPENDENCIES.md`,
   `DEPENDENCY_LICENSES.md`).
9. Changelog: add entries following the repository's existing convention
   (see how recent features are recorded at the repo root / release-notes
   pipeline) — one entry for the QUIC transport feature, plus entries for
   any user-visible sub-features (datagrams, zero-config). Follow existing
   entry style exactly.

## Write the PR description BEFORE deleting the plans

`design.md` is deleted in the final commit (AGENTS.md rule), and the PR
description becomes its heir. Draft the description first, harvesting from
`design.md` while it still exists:

* **Motivation and architecture**: the Background/Proposal essence — why
  QUIC, the forwarder-first architecture, trust bootstrap, silent fallback.
* **Measured results, stated honestly** (from "What measurement taught us"):
  head-of-line p95 ratios with the conditions attached; the idle-restart
  +1 RTT finding; the explicit statement that bulk WAN throughput modestly
  favors the port-forwarded transport and why (`rmem_max`); node-tuning
  pointer.
* **Compatibility**: additive protos, old-client/old-manager behavior,
  fallback semantics.
* Formatting requirements: **no "Test plan" section**, **no "Generated
  with" attribution lines** (repository owner's standing preferences).

## Final commit and PR

1. Final commit: delete `docs/plans/quic-transport/` entirely (`design.md`,
   `README.md`, this file — the earlier plans are already gone). Commit
   message notes that the PR description and the reference docs carry the
   surviving content.
2. Push `thallgren/quic` — **this is the point where the branch's no-push
   policy ends, and it still requires the repository owner's explicit
   go-ahead**. Do not push without it.
3. Draft PR #4204 already exists for this branch (created when only the
   design doc existed). Update its title to describe the feature (not the
   design doc), replace its body with the prepared description, and mark it
   ready for review. Base branch: `release/v2`.
4. Announcing (CNCF Slack #telepresence-oss) is the repository owner's
   call; if asked to prepare one, use the draft-message mechanism rather
   than posting directly.

## Acceptance criteria

* All checklist items ticked; verification steps 1-8 green with logs/numbers
  retained.
* PR #4204 ready for review with the prepared description; branch pushed
  with explicit approval.
* `docs/plans/quic-transport/` no longer exists on the branch tip.
