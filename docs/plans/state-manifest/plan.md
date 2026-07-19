# Workstation state manifest: `telepresence apply` / `telepresence delete`

## Objective

A declarative YAML manifest that describes the Telepresence state on a
workstation: an optional connection (with its `telepresence connect` flags) and
the ongoing attachments (intercept, replace, ingest, wiretap — each with their
flags). Two new commands consume it:

- `telepresence apply -f <manifest>` brings the workstation to the described
  state.
- `telepresence delete -f <manifest>` tears that state down.

A manifest without a `connection` depends on an already established connection,
and `delete` then leaves that connection intact. Only a manifest that declares
the connection disconnects during delete.

Docker mode is out of scope; that form of state is already captured by the
annotated Docker Compose spec (`telepresence compose`).

## Schema

Already authored (reviewable now): `pkg/client/cli/manifest/state.schema.yaml`,
JSON Schema draft 2020-12 expressed as YAML. It compiles with
`santhosh-tekuri/jsonschema/v6` and correctly accepts/rejects a test corpus
(valid full manifest, valid connection-less manifest; rejects unknown
properties, `toPod` on a wiretap, and a manifest with neither connection nor
attachments).

Shape:

```yaml
apiVersion: telepresence.io/v1alpha1
kind: WorkstationState
connection:        # optional; flags of "telepresence connect"
  name: dev
  context: kind-dev
  namespace: default
  kubeFlags: {request-timeout: 30s}   # remaining kubectl passthrough flags
  mappedNamespaces: [...]
  managerNamespace: ...
  alsoProxy/neverProxy/allowConflictingSubnets: [CIDR...]
  proxyVia: [{subnet: pods, workload: echo-server}]
  rerouteLocal/rerouteRemote: [...]
attachments:       # ordered; names unique
  - type: intercept | replace | ingest | wiretap
    name: ...
    namespace/container/env/mount/nodeAgent: ...   # common
    # intercept/wiretap: workload, service, ports, address, mechanism,
    #   httpHeaders, httpPath*, plaintext; intercept also metadata, toPod
    # replace: ports (default [all]), address, toPod
    # ingest: toPod
```

Schema design decisions:

- **Single ordered `attachments` list with a `type` discriminator** rather than
  four parallel arrays. Apply establishes them in order, delete removes them in
  reverse order. Composition uses `allOf` + `unevaluatedProperties: false`, so
  unknown or misplaced properties are hard errors.
- **`--vnat` has no schema property.** It is CLI sugar that `CommitFlags`
  already rewrites to `proxy-via CIDR=local`; the manifest expresses it as
  `proxyVia: [{subnet: ..., workload: local}]`.
- **Structured where the CLI is stringly**: `proxyVia` is `{subnet, workload}`
  objects, `metadata` is a map, env/mount are objects (`mount: {enabled, path,
  readOnly, localMountPort}` instead of the tri-state `--mount` string).
  Port lists, reroutes, `toPod`, and HTTP filters keep the exact CLI string
  formats to avoid inventing a second syntax.
- **Excluded** (besides docker mode: `--docker`, `--expose`, `--hostname`,
  `--docker-*`): the hidden profiling ports, and the UX-only `--wait-message`
  and `--detailed-output`.
- **No foreground command** (the `-- <cmd>` suffix of intercept/ingest).
  `apply` is declarative and returns; attachments are created detached, exactly
  like running the imperative commands without a command argument.

## Command semantics

### `telepresence apply [--dry-run] -f <file>`

1. Load the file (`-f -` reads stdin), convert YAML→JSON, validate against the
   embedded schema, then strict-unmarshal into Go structs. Semantic validation
   on top: unique attachment names, port-string syntax, etc.
2. Connection handling:
   - `connection` present, real apply: build a `connector.ConnectRequest` the
     same way `daemon.CobraRequest.CommitFlags` does. When a matching daemon
     is already running, first ask it — read-only, via the `CheckConnect`
     RPC, which wraps the daemon's authoritative `session.CheckStatus` —
     whether the request is aligned; drift is a User error and nothing is
     touched. Connect is never attempted against a drifted session, because
     the connect flow treats a failed Connect by deleting the daemon info
     file, which shuts the daemon down together with every attachment it
     carries (including ones the manifest doesn't own). Only an aligned or
     session-less daemon proceeds into the standard `connect.InitCommand`
     flow with the request in context, exactly like a repeated
     `telepresence connect`. There is no client-side field comparison.
   - `connection` present, dry run: look up a running daemon matching the
     connection name without launching one. None running → "would-connect",
     nothing else can be checked. One running → call the read-only
     `CheckConnect` RPC (verifies alignment without altering state); a nil
     result means the connection matches and the per-attachment comparison
     proceeds, `Unavailable` means the daemon is running but has no session
     (treated as "would-connect"), and any other error means drift, reported
     to the user.
   - `connection` absent: standard `connect.InitCommand` with a required
     session — the existing connection selected via `--use`/`--context`
     applies; error out if none exists.
3. For each attachment, in order, resolve the current state by name
   (`GetIntercept`, then `GetIngest`) and reconcile:
   - not found → create it by reusing the existing
     `intercept.Command`/`ingest.Command` state machinery (create only, no
     command to run, no leave), which also writes env files and establishes
     mounts.
   - found with a matching spec → leave untouched.
   - found with drift (different attachment type, or a differing value in any
     field that the daemon state can be compared against: workload, namespace,
     container, ports, address, HTTP filters, plaintext, metadata, toPod,
     mount point/port/read-only) → re-create: remove the existing attachment,
     then create it from the manifest. Fields with no server-side
     representation (the env file paths) can't be drift-checked; they take
     effect when an attachment is (re-)created.
4. Print a summary line per attachment (created / unchanged / re-created),
   honoring `--output` formatting like other commands.

`--dry-run` performs the same load, validation, and comparison but changes
nothing: it reports would-connect / reuse / connection-drift (an error, exit
non-zero) and, per attachment, would-create / unchanged / would-re-create.
When the manifest declares a connection that doesn't exist yet there is no
session to query, so every attachment is reported as would-create.

First failure aborts apply; already-created attachments from the same run are
left in place (they are part of the desired state, and re-running apply is
idempotent). The error reports which attachment failed.

### `telepresence delete -f <file>`

1. Same load/validation.
2. Remove each attachment in reverse manifest order (`RemoveIntercept` /
   `LeaveIngest`); a not-found attachment is not an error.
3. Only if the manifest declares `connection`: disconnect that connection
   (`Disconnect`, not `Quit -s` — daemons keep running). Without `connection`
   in the manifest, the connection is left untouched.

### Handler processes

Each attachment may declare `command`, an argv array naming the local process
that handles the attachment's traffic — the manifest equivalent of the
trailing `-- <cmd> <args...>` accepted by the imperative commands. The process
must see the attachment's remote environment and mount paths, so apply starts
it only after the attachment is established, with the environment from the
create response merged over the local environment. Unlike the imperative
commands, apply cannot supervise the handler (apply is one-shot), so:

- The handler is started detached (its own session/process group; stdout and
  stderr go to `handler-<attachment>.log` in the user log dir) and registered
  with the user daemon via the existing `AddInterceptor` RPC, which makes the
  daemon terminate it whenever the attachment is removed — by delete, by a
  drift re-create, or by an imperative leave.
- The pid and argv are also recorded client-side under the user cache dir,
  keyed by daemon ID and attachment name, so a later apply can tell whether
  the declared handler is still the one running.
- Reconcile semantics on an unchanged attachment: running with equal argv →
  untouched; exited → started again; argv differs → terminated and started
  with the new argv; `command` removed from the manifest → terminated. A
  handler command change alone never re-creates the attachment (the command
  has no server-side representation).
- Delete terminates the handler (via the daemon registration, plus the
  recorded pid as fallback) and removes the client-side record.
- `--dry-run` reports would-start / would-restart / would-stop without
  touching anything.

## Implementation layout

- `pkg/client/cli/manifest/` (new package):
  - `state.schema.yaml` — the schema (done), embedded with `go:embed`.
  - `types.go` — Go structs mirroring the schema (`State`, `Connection`,
    `Attachment` with type-discriminated decode).
  - `load.go` — read file/stdin, YAML→JSON (`sigs.k8s.io/yaml`), schema
    validation (`santhosh-tekuri/jsonschema/v6`, promoted from indirect to
    direct dependency — no new module), strict unmarshal, semantic checks.
  - `apply.go` / `delete.go` — mapping from the structs to
    `daemon.Request`, `intercept.Command`, `ingest.Command` and driving the
    create/remove flows.
- `pkg/client/cli/cmd/apply.go`, `pkg/client/cli/cmd/delete.go` — thin cobra
  commands (`-f/--filename`, session annotations), registered in
  `telepresence.go` `WithSubCommands`.
- Unit tests in `pkg/client/cli/manifest` with a `testdata/` corpus of valid
  and invalid manifests (reusing the corpus built while authoring the schema).
- Changelog entry for the new commands.

## Steps

1. Manifest package: types, embedded schema, loader + validation, unit tests.
2. Apply: connection resolution + attachment creation.
3. Delete: attachment removal + conditional disconnect.
4. Command registration, help texts, changelog.

## Resolved questions

1. `kind: WorkstationState` / `apiVersion: telepresence.io/v1alpha1` stay.
2. Apply detects drift: attachments with differing specs are re-created,
   connection drift is an error.
3. `apply --dry-run` is included.
