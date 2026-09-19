# Typed chart values for the setup package

Branch: `thallgren/setup-security-questions`. Status: implemented on this branch; remove before the PR merges.

## Why

Helm's Go API hands values around as `map[string]any`. The setup
package let that type leak from the Helm boundary into every stage: probes,
interview, pins, recommend, validate, render and verify all read and write
values through string paths. That produced the `valueAt` helper family,
`nestedMap`, `stringsToAny`, `numericValue`, `deepClone`, `diffKeys`, and
about 85 untyped-map sites, plus `any`-typed parameters on a dozen functions.
None of it is checked by the compiler.

The chart's value schema (`charts/telepresence-oss/values.schema.yaml`) sets
`additionalProperties: false` at the top level and in every nested object, and
the chart is packaged with the JSON form of that schema, so Helm rejects any
key the schema does not know. A complete typed model of the chart values
therefore loses nothing: there are no "unknown keys" to pass through.

## Rules the result must satisfy

- No `map[string]any` outside the two functions that convert to and from
  Helm's representation, and `any` is always spelled `any`, never
  `interface{}`.
- No serialization round trips as a conversion shortcut. Decoding a file
  or a stored document into the struct is a decode; converting between the
  struct and Helm's map is a reflection field copy.
- No function in the package takes or returns `any`. Printf-style
  `...any` note collectors go too; notes are formatted by the caller.
- No string-path lookups of values. Reading a setting is a field access.

## Design

### `Values` in the existing `pkg/client/cli/helm` package

The type lives in `pkg/client/cli/helm`, which already owns the Helm
boundary (chart loading, install, upgrade), so `helm.Values` reads without
stutter and the two map conversions sit next to the Helm calls that need
them. `type Values struct` mirrors the whole chart schema, hand-written from
`values.schema.yaml` with `json:"<key>,omitzero"` tags matching the chart
keys. Object blocks are plain value structs: an absent block and an empty
one are the same thing, a zero struct is omitted on output, and a field
chain such as `v.Client.Cluster.MappedNamespaces` never needs a nil check.
Only leaves are nilable: scalars are pointers (`*bool`, `*string`,
`*int32`, `*resource.Quantity`) so nil means unset, and lists and maps are
nil when absent and kept when explicitly empty. Kubernetes-shaped fields
reuse the API types as values (`core.Affinity`, `core.ResourceRequirements`,
`core.Probe`, `meta.LabelSelector`, `[]core.Toleration`, and so on).

The `client` block is the client configuration, which the chart copies
verbatim into the traffic-manager's ConfigMap, so its children mirror every
section of `pkg/client`'s config type and a reflection test keeps the two in
step; the schema leaves those objects open on purpose.

Boundary, in one file, the only place Helm's map type appears. No
serialization round trips: each conversion is a direct decode or a
reflection-based field copy.

- `ValuesFromMap(map[string]any) (*Values, error)`: converts a map Helm
  produced, the installed release's stored values or the parsed `--set`
  and `-f` input of the helm commands, with
  `runtime.DefaultUnstructuredConverter.FromUnstructured`, which walks the
  struct's json tags by reflection. Unknown keys are an error, as Helm
  itself would report.
- `(*Values) ToMap() (map[string]any, error)`: the reverse, with
  `ToUnstructured`, used only where Helm's install action and the chart
  renderer demand a map.
- `ParseValues([]byte) (*Values, error)`: `sigs.k8s.io/yaml.UnmarshalStrict`
  straight into the struct, for `--input` files.
- `DefaultValues() (*Values, error)`: `ParseValues` of the embedded chart's own
  `values.yaml` read from `charts.TelepresenceFS`, decoded once. No chart
  archive is built or loaded for this.
- Writing values out is `sigs.k8s.io/yaml.Marshal` of the struct.

Two operations must treat every field of the struct the same way. They are
implemented once with `reflect` over the struct's json tags and exposed with
typed signatures:

- `MergeValues(over, base *Values) *Values`: field-wise; a set field in
  `over` wins and a zero value of any kind counts as unset, structs with
  exported fields recurse, and slices, maps and structs with unexported
  fields replace whole. An upgrade lays the engine's decisions over the
  release's current values with it, so the document handed to Helm and
  written by `--output` is complete.
- `DiffValues(a, b *Values) []string`: dotted paths of every leaf `a` sets
  that `b` lacks or differs from. The report's "Changed from current
  installation" list.

Reconciling the recommendation against an `--input` file does not need the
walk. The engine makes a dozen decisions and knows each one's field, so each
decision checks its own input field explicitly through one typed generic
helper in the setup package:

    func decide[T comparable](path string, input *T, recommended T, consult ConsultFunc) (T, error)

An unset input field adopts the recommendation, an equal one needs nothing,
a differing one asks `consult` (nil keeps the input and records a note).
Values are formatted for the prompt with `fmt.Sprint` inside the helper.

Tests: `DefaultValues()` must succeed (a chart key without a struct field fails
the test); `ToMap` then `ValuesFromMap` of `DefaultValues()` reproduces it, proving
the two converters agree; `MergeValues` and
`DiffValues` table tests; a test that the struct's json tags cover every
`properties` key in `values.schema.yaml` (walk the schema, check tags), so
the model cannot drift from the chart silently.

### Setup package changes

- `ReleaseFacts.Values *helm.Values`, never nil (empty when nothing is
  installed); the release probe converts `release.Config` with
  `ValuesFromMap`, and a conversion failure stops setup before the interview.
- `Prober.CandidateValues *helm.Values`; `DefaultCandidateValues()` builds
  the struct. `renderChart` and `PlannedObjects` take `*helm.Values` and
  call `ToMap` at the Helm call.
- Interview: one `effective := helm.MergeValues(release, defaults)` computed
  once per interview; upgrade defaults are field reads on it.
- Input: `LoadInputValues(path) (*helm.Values, error)`. `Pins` is
  deleted; `DerivePins(*helm.Values)` becomes `PinAnswers(in *helm.Values, a *Answers, pre *Preset)`
  reading fields whose nil-ness already means "unpinned".
  `ReconcileWithInput` and its walker are deleted; each engine decision
  goes through `decide` against the input's field.
  `ValidateValues(facts, *helm.Values, applying)` reads fields.
- Recommend: an `engine` struct holds the facts, answers, input, consult
  function and notes, and each decision step is a method on it that builds
  `*helm.Values` by assignment; `Proposal.Values` and `BaseValues` are
  `*helm.Values` (`Proposal.Values` is nil when nothing is proposed); the
  upgrade merge is `helm.MergeValues` and `ChangedKeys` is `helm.DiffValues`.
- Notes: `info, warn func(string, ...any)` parameters are replaced by a
  small `*notes` collector with `info(text string)` and `warn(text string)`
  methods; callers use `fmt.Sprintf`.
- Render: `WriteValues(w, *helm.Values, header...)` marshals with
  `sigs.k8s.io/yaml`; key order becomes struct order rather than
  alphabetical, which the report and the regression assertions tolerate.
- Verify and health read fields (`authEnforced(*helm.Values)`,
  `externalTLSSecretName(*helm.Values)`, `injectorName`, `x509AuthEnabled`).
- `cmd/setup.go` changes only in the types it passes through.

Test code may still decode YAML into maps where it inspects files a test
wrote; production code may not.

### Helm command changes (follow-on)

The helm package has the same problem in miniature: `GetValues` builds the
image, grpc and agent-image settings as nested maps, `getTrafficManagerVersion`
reads `image.tag` back with type assertions, and `coalesceValues` merges the
user's `--set`/`-f` document over them with `chartutil.CoalesceTables`.
With the type in place: `GetValues` returns `*Values` with typed `Image`,
`Grpc` and `Agent.Image` fields; the version read is `values.Image.Tag`; the
user's document, which Helm's own value parser hands over as a map, enters
through `ValuesFromMap`; the merge is `MergeValues`; and `ToMap` is called
only at the install, upgrade and lint actions. `Request.ValuesJson` stays as
the transport from the CLI, decoded once with `ParseValues`.

## Work items

1. `helm.Values`: struct, boundary, `MergeValues` and `DiffValues`, tests. One agent.
2. Migrate `setup` and `cmd/setup.go` to it, delete the helpers, update
   unit tests. One agent, after 1.
3. Regression `Setup` suite on kind-dev, lint, docs regeneration. Main model.
4. Migrate the helm install, upgrade and lint paths to `Values`; regression
   `TestInstall` on kind-dev. One agent, after 3.

Items 1 to 3 land as one commit on this branch after the cleanup commit;
item 4 as a separate commit after it.

## Out of scope

- Generating the struct from the schema with a code generator. The
  coverage test gives the same guarantee without a new build dependency;
  revisit if the chart schema starts changing often.
