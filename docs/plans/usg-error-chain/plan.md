# Plan: richer error reporting in anonymous usage collection

## Goal

Improve the `error` information captured by the usage-reporting subsystem so that:

1. We capture the **full error chain**, not just the leaf-most (innermost) error type.
2. The report includes **gRPC status codes** and **HTTP status codes** (from Kubernetes API
   errors) when the failure carries one.

All of this must stay within the package's hard privacy rule: **no error message text is
ever sent** — only tool-intrinsic identifiers (Go types, errcat category, status codes).

## Current behavior

The only error-report site is `pkg/usg/cobra.go:83`, inside the `RunE` wrapper installed by
`AttachToRoot`:

```go
if err != nil {
    r.Add("error", formatError(err))
}
```

`formatError` (cobra.go:114) walks to the innermost unwrapped error and renders a single
string `"{errcat-category}:{innermost-go-type}"`, e.g. `user:*errors.errorString`. Two
limitations:

- It deliberately throws away every wrapper frame, so a connect failure that was
  `categorized -> fmt.wrapError -> StructuredError(Unavailable)` collapses to just the leaf
  type, losing both the wrapping structure and the gRPC code.
- It has no notion of gRPC or HTTP status codes.

The traffic-manager side does not report command errors at all; this plan does not change
that (out of scope — see below).

## Key facts discovered

- **errcat category** is already a whole-chain property: `errcat.GetCategory` unwraps until
  it finds a `Categorized`. A new `Category.String()` already exists on this branch.
- **gRPC codes, server side / direct calls:** standard `status.FromError(err)` (grpc v1.81.0,
  which unwraps via `errors.As`) finds a `*status.Status` anywhere in the chain.
- **gRPC codes, CLI side:** errors returned from the user daemon are reconstructed by
  `pkg/grpc.FromGRPC` into `*grpc.StructuredError`. That type carries the code via
  `GetCode() grpcCodes.Code` and the category via `GetCategory()`, but it does **not**
  implement `GRPCStatus()` and does **not** `Unwrap()`. So `status.FromError` will *not*
  see it — we must also probe for a `GetCode()`-bearing error.
- **HTTP codes:** the relevant source is Kubernetes API errors
  (`k8s.io/apimachinery/pkg/api/errors`). These implement the `apierrors.APIStatus`
  interface; `apiStatus.Status().Code` is the HTTP status (int32) and `.Reason` is a stable
  enum string. `errors.As(err, &apiStatus)` finds it anywhere in the chain. Raw `net/http`
  responses are not surfaced as errors anywhere relevant to tracked commands.

## Design

Replace the single `formatError` with a small analyzer that produces a structured result,
and emit it as **multiple report entries** instead of one packed string. Multiple keys are
easier for a collector to aggregate/group than a single delimited blob, and we are well
under `MaxEntryCount`.

### Entries produced (only when applicable)

| Key             | Example value                              | When                                  |
|-----------------|--------------------------------------------|---------------------------------------|
| `error.cat`     | `user`                                     | always, on failure                    |
| `error.chain`   | `*url.Error>*net.OpError>*os.SyscallError` | always, on failure                    |
| `error.grpc`    | `Unavailable`                              | a gRPC status code is present         |
| `error.http`    | `404`                                      | a k8s/HTTP status code is present     |

Notes:
- `error.cat` replaces the category half of the old `error` value. (This branch is
  unreleased, so there is no backward-compatibility constraint on the old `error` key; we
  drop it in favor of the split keys.)
- `error.grpc` uses the `codes.Code.String()` name (`Unavailable`, `NotFound`, …) rather
  than the numeric code — stable and human-readable. `OK`/`Unknown` are treated as
  "no meaningful code" and omitted.
- `error.http` uses the numeric status (`404`, `409`, …). We may optionally also record the
  k8s `Reason` as `error.k8s` (e.g. `NotFound`, `Conflict`); flagged as a decision below.

### Chain representation

Walk the chain outermost -> innermost via `errors.Unwrap`. For each frame, emit a token:

- **Skip pure structural wrappers** that carry no diagnostic signal: `*fmt.wrapError`,
  `*fmt.wrapErrors`, and errcat's `*errcat.categorized`. (Matched by `%T` string; these are
  the only wrappers we introduce ourselves.)
- **Sentinels** get a friendly stable name instead of their type:
  `context.Canceled`, `context.DeadlineExceeded`, `io.EOF`, `os.ErrNotExist`.
- **Everything else** contributes its `%T`.
- Collapse consecutive duplicate tokens.
- Join with `>`.

Edge case — multi-wrap (`Unwrap() []error`, e.g. `errors.Join`): recurse into each branch
and join branches with `|`. Kept minimal; rare in tracked command paths.

Code extraction is independent of the chain walk (codes can sit on any frame):

```go
// gRPC: standard status first, then the telepresence StructuredError shape.
if s, ok := status.FromError(err); ok && s.Code() != codes.OK && s.Code() != codes.Unknown {
    grpc = s.Code().String()
} else {
    var coder interface{ GetCode() codes.Code }   // matches *grpc.StructuredError
    if errors.As(err, &coder) {
        if c := coder.GetCode(); c != codes.OK && c != codes.Unknown {
            grpc = c.String()
        }
    }
}

// HTTP via k8s API errors.
var apiStatus apierrors.APIStatus
if errors.As(err, &apiStatus) {
    http = strconv.Itoa(int(apiStatus.Status().Code))
}
```

Using a locally-declared `interface{ GetCode() codes.Code }` (rather than importing
`pkg/grpc`) keeps `pkg/usg` from depending on `pkg/grpc`. Decision flagged below.

## Files to change

- `pkg/usg/errinfo.go` (new) — `errInfo` struct + `analyzeError(err) errInfo` and an
  `(errInfo).addTo(*Report)` method. Move/replace the logic currently in `formatError`.
- `pkg/usg/cobra.go` — replace the `r.Add("error", formatError(err))` call with
  `analyzeError(err).addTo(r)`; delete `formatError`; drop the now-unused `fmt` import if
  applicable; update the `AttachToRoot` doc comment (lines 37-38) describing what is
  recorded on failure.
- `pkg/usg/errinfo_test.go` (new) — table tests mirroring `usg_test.go` style, covering:
  plain error, single/multiple `%w` wrapping, errcat-categorized, a `status.Error`,
  a synthetic `GetCode()` error (StructuredError shape), a k8s `NewNotFound`, sentinels,
  and a `errors.Join` branch.
- `pkg/usg/report.go` — no code change; if we add per-error keys, confirm `MaxEntryCount`
  comfortably accommodates them alongside the existing `flags`/`flag.*` entries (it does).

No proto/RPC changes: entries already travel as the free-form `map<string,string> entries`
field, so the collector schema is unaffected.

## Out of scope (call out, do not implement unless requested)

- Sending any error message text or stack traces.
- Changing the collector / proto schema.

## Follow-up: traffic-manager side (implemented)

The manager had no per-operation error reporting (only `manager.boot`). Its analog to the
client's per-command failure is a failed gRPC call, so reporting hangs off the gRPC server's
error interceptor — the single chokepoint that still sees the **raw** error chain before
`jsonError` flattens it for the wire.

- `pkg/usg/server.go` (new) — `ReportServerError(ctx, method, err)` plus `isBenignCode`.
  Sends a report with topic `manager.rpc`, a `method` entry (the code-defined
  `info.FullMethod`, safe), and the `analyzeError` entries. No-op unless a producer is
  installed **and** its `Source == manager` — the user daemon shares the gRPC server code,
  and its failures are already reported per-command by the CLI, so gating on source prevents
  double-counting. `InstallManager` is only called by the traffic-manager, confirming the
  gate is sufficient.
- `pkg/grpc/server/server.go` — the unary and stream error interceptors call
  `usg.ReportServerError` with the raw error before `jsonError`. No import cycle:
  `pkg/usg`'s dependency tree does not include `pkg/grpc/server`.
- **Benign-code filter** (confirmed with user): skip `OK`, `Canceled`, `DeadlineExceeded`,
  `NotFound`, `AlreadyExists`, `Unimplemented`. `status.Code` maps nil→OK and the context
  errors to Canceled/DeadlineExceeded, so raw client disconnects are filtered too.
- `pkg/usg/server_test.go` (new) — manager-reports / benign-skipped / client-no-report /
  nil-noop / no-producer-noop, plus an `isBenignCode` table.

## Decisions (confirmed)

1. **Split keys** — `error.cat` / `error.chain` / `error.grpc` / `error.http`. The old
   single `error` key is dropped (branch unreleased).
2. **k8s: HTTP code + Reason** — emit both `error.http` (numeric) and `error.k8s`
   (`apiStatus.Status().Reason`, e.g. `NotFound`).
3. **Coupling** — use a local `interface{ GetCode() codes.Code }` to read the telepresence
   gRPC code; no `pkg/usg -> pkg/grpc` import.
4. **Chain: signal-only** — drop our structural wrappers (`fmt.wrapError`/`wrapErrors`,
   `errcat.categorized`); keep informative frames and sentinel names.
```
