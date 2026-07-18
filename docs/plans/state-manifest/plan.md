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

### `telepresence apply -f <file>`

1. Load the file (`-f -` reads stdin), convert YAML→JSON, validate against the
   embedded schema, then strict-unmarshal into Go structs. Semantic validation
   on top: unique attachment names, port-string syntax, etc.
2. Connection handling:
   - `connection` present: resolve the connection name (explicit `name` or
     derived from context/namespace, same daemon logic as today). If that
     connection already exists, reuse it as-is — no drift reconciliation in v1.
     Otherwise connect with the declared flags (build a
     `connector.ConnectRequest` the same way `daemon.CobraRequest.CommitFlags`
     does).
   - `connection` absent: standard `connect.InitCommand` with a required
     session — the existing connection selected via `--use`/`--context`
     applies; error out if none exists.
3. For each attachment, in order: if it already exists (`GetIntercept` /
   `GetIngest` by name) it is skipped — apply is idempotent. Otherwise create
   it by reusing the existing `intercept.Command`/`ingest.Command` state
   machinery (create only, no command to run, no leave), which also writes env
   files and establishes mounts.
4. Print a summary line per attachment (created/already present), honoring
   `--output` formatting like other commands.

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

## Open questions

1. `kind: WorkstationState` and `apiVersion: telepresence.io/v1alpha1` — happy
   to rename (e.g. `State`, `v1`).
2. Apply-time drift: v1 reuses an existing connection/attachment by name
   without comparing flags. A later iteration could detect drift and
   re-create (attachments) or error (connection).
3. Should `apply` gain `--dry-run` (validate + report only)? Cheap to add;
   left out of v1 unless wanted.
