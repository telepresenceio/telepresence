# Plan: deprecate `--output`, introduce `--format` for consistent structured output

## Problem

The global `--output` flag is used inconsistently:

- Some commands (`status`, `config`, `ingest`, `intercept`) emit a clean
  structured object.
- Others (`version`, `list`, `list_contexts`, `list_namespaces`) emit a
  wrapper object `{cmd, stdout, stderr, err}` where the real payload is either
  a JSON-stringified blob of human text (`version`) or an object nested under
  `stdout` (`list*`).

We want every command to be able to produce clean structured output, but we
must not change what `--output` already returns — external tooling may depend
on the current `{cmd, stdout}` shape. So we keep `--output` exactly as-is
(deprecated) and add a new `--format` flag that always produces clean
structured output.

## Current architecture (as-is)

All in `pkg/client/cli/output/output.go`:

- `--output` is a global persistent flag (`global/flags.go:41`), values
  `default | json | yaml | json-stream`.
- `SetFormat` (PersistentPreRunE, wired at `cmd/telepresence.go:46`) replaces
  the command's stdout with a buffering `*output` when a non-default format is
  requested.
- `output.Object(ctx, obj, override)` records the object to marshal.
- `output.Execute` (`main.go:67/74`) marshals after the run:
  - `override == true` → marshal `obj` alone (clean).
  - otherwise → wrap in `object{cmd, stdout, stderr, err}` (`output.go:242`);
    buffered `Printf` text becomes the `stdout` string.

The clean/wrapped distinction is therefore already a single bit: the `override`
argument. `--format` is conceptually "force `override = true` for every
command, and never emit the wrapper."

### Command buckets

| Bucket | Mechanism today | Commands | Work under `--format` |
|--------|-----------------|----------|-----------------------|
| Clean | `output.Object(…, true)` + human text via KeyValueFormatter | `status`, `config`, `ingest`, `intercept` | none (identical) |
| Wrapped object | `output.Object(…, false)` | `list`, `list_contexts`, `list_namespaces` | trivial — emit the existing object cleanly |
| Text-only | `KeyValueFormatter`/`Printf`, captured as a `stdout` string | `version` | moderate — design a structured object |

Everything else (`connect`, `quit`, `leave`, `uninstall`, …) emits status
messages, not data; see "Status-message commands" below.

## Goals / non-goals

Goals:
- Add `--format=json|yaml|json-stream` producing clean structured output for
  every command, with no `cmd`/`stdout`/`stderr`/`err` envelope.
- Keep `--output` behavior byte-for-byte identical (back-compat), but mark it
  deprecated and point users to `--format`.

Non-goals:
- Changing `--output` output shapes.
- Reworking the human-readable (default) output of any command.

## Design

### 1. New global flag

In `pkg/client/cli/global/flags.go`:
- Add `FlagFormat = "format"`, register it (same value set as `--output`,
  default `default`), add to `FlagNames`.

### 2. Output package: track the format source

`output.go` currently keys solely off `FlagOutput`. Change it to resolve the
effective format from **either** flag and remember which one was used:

- `validateFlag` returns `(format, source, error)` where `source ∈ {output,
  format}` (or fold into the `output` struct as a `cleanEnvelope bool`).
- `SetFormat` sets `o.cleanEnvelope = (source == format)`.
- `Execute`:
  - If `cleanEnvelope` (i.e. `--format`): marshal the command's object directly,
    never the `object{…}` wrapper.
    - `o.obj != nil` → marshal `o.obj`.
    - else if buffer has text → fallback: marshal the captured text as a bare
      JSON string (valid JSON, no envelope) and record the command for
      migration. (Migrated commands won't hit this.)
  - Else (`--output`): unchanged.

### 3. Precedence & validation

- If both `--output` and `--format` are set: **hard error** (`errcat.User`)
  telling the user to pick one (DECIDED). Only the *global* flags count (guarded
  by `DefValue == "default"`), so a local `--format`/`--output` on
  compose/genyaml never triggers it.
- Reuse the existing format parsing so `--format` accepts exactly the same
  values, including `json-stream`, and `DefaultYAML` (config command,
  `cmd/config.go:33`) honors `--format` too.

### 3a. Flag-name collisions (compose `--format`)

Several generated `compose` subcommands (`config`, `images`, `ls`, `ps`,
`stats`, `version`, `volumes`, `bridge transformations list`) already define a
local `--format` flag (from `dc-cli.json`, `default=None` → `DefValue == ""`)
that is passed through to docker compose.

This is the **same collision** that already exists between the global
`--output` and `genyaml`'s local `--output` (DefValue `"-"`), and the existing
guard handles it: `validateFlag` only activates the global machinery when the
looked-up flag has `DefValue == "default"` (`output.go:214`). In cobra/pflag a
subcommand's local flag shadows the inherited persistent flag of the same name
(the inherited one is skipped during merge), so `Flags().Lookup("format")` on
`compose stats` returns the *local* flag.

Therefore the global `--format` (DefValue `"default"`) must reuse this pattern:
its format resolution checks `DefValue == "default"` so compose's local
`--format` (DefValue `""`) is never hijacked and continues to reach docker
compose unchanged.

Implementation constraints so the passthrough cannot break:
- Keep the global `--format` a **plain string** validated only inside the
  guarded resolution (like `--output`), *not* a custom enum `pflag.Value` that
  rejects values at parse time. compose's `--format` is `type: string` and must
  accept arbitrary docker-compose values (`table`, `json`, a Go template).
- `config.go:appendFlags` forwards every `Changed` local flag to docker compose
  (`--format <value>`), so the local flag must remain the one that gets set.

Regression tests:
- `compose stats --format json` forwards to docker compose, does **not** trigger
  global structured output.
- `compose stats --format table` is accepted (not rejected as an invalid global
  format) and forwarded to docker compose verbatim.

### 4. Deprecate `--output`

- Use the soft helper `flags/deprecation.go:DeprecationIfChanged` so `--output`
  keeps working but prints `Flag --output has been deprecated, use --format`
  only when it is actually set. Do **not** use pflag's built-in `Deprecated`
  (it hides the flag and we want it discoverable + unchanged).
- **Warning routing**: the deprecation message must go to real stderr, never
  into stdout JSON. Today stderr is captured into `object.Stderr` under
  `--output`; for `--format`'s clean mode it must bypass the buffer. Verify the
  warning is emitted via a writer that is not the buffering `*output`.

### 5. Error representation under `--format` (open decision)

Under `--output`, errors are *not* clean even for `override=true` commands: the
`override` fast-path requires `err == nil`, so on failure `Execute` falls back
to the `{cmd, …, err}` envelope on stdout (and `main.go` suppresses the stderr
print because formatted output was produced). There is no existing clean-error
convention.

DECIDED: under `--format`, on error emit a clean **`{"error": "<message>"}`**
object on **stdout** and exit non-zero. This mirrors the `.err` semantics that
`--output=json` consumers rely on (errors as JSON on stdout) without the
`cmd`/`stdout` envelope. `Execute` produces this when `cleanEnvelope` is set and
`err != nil`.

## Per-command migration

Pattern to follow (already used by `status`): compute a structured object,
call `output.Object(ctx, obj, …)` for the formatted path, and keep the
`KeyValueFormatter`/`Printf` path for human output.

**Crucial back-compat rule for text-only commands.** A command that historically
emitted only text (e.g. `version`) had its `--output` shape be the
`{cmd, stdout: "<text>"}` envelope. It must *not* start emitting a structured
object under `--output`, or that shape changes. Such commands gate the `Object`
call on **`output.WantsClean(cmd)`** (true only for `--format`), keeping the text
path for `--output`:

```go
if output.WantsClean(cmd) {         // --format only
    output.Object(ctx, obj, true)   // clean structured object
} else {
    kvf.Println(cmd.OutOrStdout())  // text; --output wraps it as {cmd, stdout}
}
```

Commands already passing a structured object to `Object` (the `list*` family,
`override=false`) need **no change**: the machinery emits their object cleanly
under `--format` and keeps the `{cmd, stdout: obj}` envelope under `--output`.
Commands already using `override=true` (`status`, `config`, `ingest`,
`intercept`) were clean under `--output` before and are unchanged.

1. **`list`, `list_contexts`, `list_namespaces`** — already build objects
   (`workloads`, `cm`, `nss`); they pass `override=false`. Under `--format`
   they emit cleanly with no code change beyond the envelope logic in `Execute`.
   Verify shapes are script-friendly.
2. **`version`** — replace the `KeyValueFormatter`-only path with a structured
   object for the formatted path, e.g.
   ```
   {
     "client": "v2.29.0",
     "root_daemon": "v2.29.0",
     "user_daemon": "v2.29.0",
     "traffic_manager": "v2.29.0",
     "traffic_agent": "ghcr.io/.../tel2:2.29.0",
     "connections": { "<name>": { … } }   // multi-daemon case
   }
   ```
   The multi-daemon "Connection X" case currently embeds a sub-formatter's text
   (`version.go:115`); it must become a nested object.
3. **Status-message commands** (`connect`, `quit`, `leave`, `uninstall`, …):
   define a default minimal envelope (e.g. `{"command": "...", "message": "..."}`
   or just the success object they already produce) so `--format` output is
   structured rather than a JSON-stringified blob. Can be migrated incrementally
   after the data commands.

### Migration audit (deliverable of phase 2)

Enumerate every command's formatted output and assign a target shape. Known
data commands: `version`, `list*`. Confirm there are no other
`KeyValueFormatter`/`Printf`-only data commands beyond `version`.

## Backward compatibility

- `--output` unchanged in values, semantics, and output shapes.
- `--format` is additive.
- Deprecation is a stderr warning only; scripts using `--output` keep working.

## Testing

- Extend `output/output_test.go`: matrix of `{--output, --format} ×
  {json, yaml, json-stream}` for a clean command, a wrapped command, and a
  text-only command, asserting `--output` shapes are unchanged and `--format`
  shapes have no envelope.
- Integration: a focused test asserting `version --format json` and
  `list --format json` produce envelope-free JSON, and that `--output json`
  still produces the legacy shape.

## Risks / open questions

- Error representation under `--format` (see §5) — needs a decision.
- Precedence when both flags set (§3) — needs a decision.
- Deprecation warning must not corrupt clean JSON (§4).
- `json-stream` semantics under `--format` (streamed objects already bypass the
  envelope via `Object`'s stream branch — confirm parity).
- Generated docs/markdown for the new flag (`make docs-files`,
  `docs/reference/cli/**` are generated).

## Phased implementation

1. **Machinery**: add `--format`, format-source tracking, envelope-free
   `Execute`, deprecation warning, precedence/validation. Tests for the
   machinery using existing clean/wrapped commands.
2. **Data commands**: migrate `list*` (verify) and `version` (structured
   object). Tests.
3. **Audit + status-message policy**: enumerate remaining commands, apply the
   default envelope, migrate incrementally.
4. **Tests**: migrate the integration tests and the `itest` harness off the
   deprecated `--output` to `--format` (done). Flag-only for commands already
   clean under `--output` (`status` harness, `intercept --detailed-output`,
   `ingest`, `config view`, `list --format json-stream`); `wiretap`'s
   `list --format json` parsing switched from the `{cmd, stdout}` envelope to
   the bare array, and the harness `StatusResponse` error tag from `err` to
   `error`. Left untouched: kubectl `--output`/`-o`, `helm --output`, and
   `genyaml --output -` (a local file flag).
5. **Docs**: regenerate CLI docs; add a changelog entry.

This plan file should be deleted in the change that completes the work.
