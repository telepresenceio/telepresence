# Plan: an RBAC assistant (`telepresence rbac`)

Implements #4215. Five work items, in order. Items 1 and 2 are refactors that the
rest build on. Items 3, 4 and 5 can land as separate PRs.

The command answers two questions against a cluster the caller may not own: "what
does this identity need to install Telepresence" and "what does this identity need to
use it". Every output is plain RBAC YAML (or a `clientRbac` values block) that a
cluster admin can review and apply. Nothing is ever applied by the command itself.

```text
telepresence rbac generate install [--input values.yaml] [--subject KIND:NAME]... [--verbs install|lifecycle] [--all]
telepresence rbac generate client  --subject KIND:NAME... [--namespace NS]... [--input values.yaml] [--format rbac|values]
telepresence rbac verify   install [--input values.yaml] [--verbs install|lifecycle]
telepresence rbac verify   client  [--namespace NS]... [--input values.yaml]
```

`generate` and `verify` say what the command does, so `rbac generate install` doesn't
read as if it installs something next to `telepresence helm install`.

## Which identity is checked

Every sweep uses `SelfSubjectAccessReview`, so it checks the identity the kubeconfig
resolves to after the global impersonation flags (`--as`, `--as-group`), which the
client already applies to its REST config. An admin who wants output for someone else
runs the command with `--as`.

`--subject` only names the subjects of the emitted bindings. When it's omitted, the
subject is the checked identity, read with `SelfSubjectReview`
(`authentication.k8s.io/v1`). When it's given, the command compares it with the
checked identity and refuses on a mismatch, with a message that suggests `--as` for
that subject, or `--all` for output that doesn't depend on any identity. Otherwise an
admin running `--subject User:alice` without `--as` would get an empty result,
because the admin can already do everything.

`SelfSubjectReview` needs Kubernetes 1.28. On an older cluster, or when the review is
refused, the command asks for `--subject` instead of failing, and says in the output
header that it couldn't confirm the subject matches the checked identity.

---

## 1. Share the chart render and access sweep with `setup`

`pkg/client/cli/setup/probe_rbac.go` already does the hard part: it renders the
embedded chart client-side (`loadEmbeddedChart`, `renderChart`), decodes it
(`decodeManifests`), turns every object into `ResourceAttributes`
(`attributesForObjects`, `extraChecks`, `dedupeAttributes`) and checks them with
bounded concurrency (`sweepAccess`). The RBAC assistant has to see the same object
set, or its output drifts from what `setup` reports.

**Change**: new package `pkg/client/cli/rbacgen` (not `rbac`, since the repo imports
`k8s.io/api/rbac/v1` as `rbac` and the generator needs that package).

* Move the render, decode, attribute and sweep helpers out of `setup` into an
  exported API, for example `rbacgen.RenderChart(ctx, ns, values)`,
  `rbacgen.InstallAttributes(objs, ns, nsExists, values)` and
  `rbacgen.Sweep(ctx, client, attrs) (denied, error)`.
* `DeniedAttribute` gets a `Subresource` field (`json:"subresource,omitempty"`), for
  `pods/portforward`, `pods/eviction` and `logs/yaml`. It's empty for everything
  `setup` reports today, so its output doesn't change.
* `setup` calls the new package. `probeRBAC`, `evaluateChartAccess` and
  `PlannedObjects` keep their behavior and their tests. This item changes no output.

**Tests**

* The existing `probe_rbac_test.go` and `planned_test.go` pass unchanged.
* Move the helper-level tests that exercise the moved functions next to them.

**Done when**: `telepresence setup` output is byte-identical before and after.

---

## 2. Share the manager's authorization reviews with the CLI

`verify client` has to ask the same questions the manager asks, or the two drift. The
review definitions live in `cmd/traffic/cmd/manager/auth/authorizer.go` (`Review`,
`ConnectReview`, `AttachmentReview`, `EnsureAgentReview`, `NamespaceReview`, the
`logs` attributes in `CanGetLogs`) and the grant modes in `grant.go`. Nothing under
`pkg/client` imports `cmd/traffic` today.

**Change**

* Move `Review`, its constructors, the `logs` attribute builder and `Grant` into a
  package both sides can import, for example `pkg/authz`. The manager keeps its
  `Authorizer` (`SubjectAccessReview` for a principal, with its cache) and evaluates
  the shared definitions as before.
* Add the CLI side of the evaluation: the same "any alternative passes" logic as
  `Authorize`, but with `SelfSubjectAccessReview` for the caller.

**Tests**

* The manager's authorizer tests pass unchanged.
* For each grant mode (`portforward`, `telepresence`, `any`), the CLI evaluation and
  the manager's `Authorize` agree on the same set of allowed attributes.

**Done when**: the manager's behavior is unchanged and the CLI can run the same
reviews.

---

## 3. `telepresence rbac generate install`

**What the attribute set covers**

The attribute set describes what `telepresence helm install` does, not only what the
chart renders. On top of the rendered objects from item 1:

* `get` on every rendered object. Helm's `existingResourceConflict`
  (`pkg/action/validate.go`) reads each one before creating anything, and any error but
  NotFound aborts the install.
* The `lookup` reads in `templates/agentInjectorWebhook.yaml` when the agent injector
  is enabled (the default): `list` on namespaces, `get` on the `traffic-manager`
  ConfigMap in every namespace, and `get` on the webhook secret. A refused lookup fails
  the render. The client-only render in `setup` never runs these, so they're added
  explicitly.
* Helm's release storage in the manager namespace: `list`, `get`, `create` and
  `update` on secrets. These can't be limited by name, because the names carry the
  revision.
* `Wait` and `Atomic` (`pkg/client/cli/helm/install.go`): read access to the objects
  the install waits for, and `delete` on the rendered objects for the rollback of a
  failed install.

The regression test in this item settles the exact set. Anything it shows is missing
goes into the attribute set, not into a special case in the output.

**Behavior**

* Render the chart for the intended values: the embedded defaults, overlaid with
  `--input` (the same loader `setup` uses, `setup.LoadInputValues`).
* Sweep the attribute set above for the checked identity (see "Which identity is
  checked"). `create` reviews are sent without a name. The API server authorizes a
  create before it knows the object's name, so a review that carries the name can be
  allowed by a rule that would refuse the real create. The name is kept for reporting
  only.
* Emit manifests that cover only the denied pairs:
  * one `ClusterRole` plus `ClusterRoleBinding` for cluster-scoped kinds
    (ClusterRole, ClusterRoleBinding, MutatingWebhookConfiguration, Namespace, ...)
    and for the cluster-wide reads above,
  * one `Role` plus `RoleBinding` per namespace for namespaced kinds,
  * rules grouped by `(apiGroup, resource, verbs)`. `create` rules never carry
    `resourceNames`, because such a rule grants nothing. `get`, `update`, `patch` and
    `delete` carry them when the chart names the object.
* Output is deterministic (sorted rules, sorted documents, stable names such as
  `telepresence-install-<namespace>`), and starts with a comment header saying which
  Telepresence version, values and identity produced it, and what the admin should
  check before applying.
* `--all` skips the sweep and emits the complete set, for GitOps flows where the
  generator has no access to the target cluster.
* The cert-manager `Certificate` and the `kube-system` x509 RoleBinding are part of
  the render when the values enable them, so they're emitted like everything else.

**Escalation prevention**

Creating the chart's Roles and ClusterRoles requires the installer to already hold
every permission those roles grant, or to hold `escalate` on roles and `bind` on
bindings. The generator copies the union of the rules in the chart's own Roles and
ClusterRoles into its output, scoped the same way, instead of granting `escalate`.

The one exception is the x509 RoleBinding in `kube-system`
(`templates/trafficManagerRbac/x509-auth.yaml`). It binds the stock
`extension-apiserver-authentication-reader` Role, which isn't in the chart, so the
union doesn't cover it. For that binding the output grants `bind` on `roles` with
`resourceNames: [extension-apiserver-authentication-reader]` in `kube-system`. `bind`
is checked against the name of the referenced role, so the restriction holds there,
unlike for `create`.

Because the rules are copied, the installer ends up holding everything the
traffic-manager holds, cluster-wide in a cluster-wide install: pod logs, evictions,
workload patch, token reviews and access reviews. The header says so in plain words,
so the admin knows what's being granted.

**Verbs**

The default (`--verbs install`) is the set a first install needs, described above.
`--verbs lifecycle` adds what `helm upgrade` and `telepresence helm uninstall` need on
the same objects (`update`, `patch`, `delete`, and the release secrets). The sweep
always checks the same verb set it emits.

**Tests**

Unit tests in `pkg/client/cli/rbacgen`, with a fake clientset whose
`SelfSubjectAccessReview` reactor denies a chosen set (the pattern
`probe_rbac_test.go` already uses):

* Nothing denied: no manifests, and a message saying the identity can install.
* A denied webhook plus a denied namespaced Role: the right ClusterRole and Role
  rules, in the right namespace, and no `resourceNames` on any `create` rule.
* `create` reviews are sent without a name.
* Chart Roles present: their rules appear in the escalation section.
* x509 auth enabled: the binding and the single named `bind` rule appear.
* `--subject` that doesn't match the checked identity: refused with the `--as` hint.
* `SelfSubjectReview` unavailable: asks for `--subject`.
* `--verbs lifecycle`: the lifecycle verbs appear and the sweep checked them.
* Golden-file test of the full output for the default values, so changes to the
  chart show up as a reviewable diff.

These run against a fake clientset, so they can't show the output is enough. That's
what the regression test is for:

* A new test under `regression_test/suites/auth`, which already works with restricted
  identities (`minimal_rbac.go`). It creates an identity with no other rights, runs
  `rbac verify install` as that identity and expects a failure, runs
  `rbac generate install --as` that identity, applies the output as admin, then runs
  `telepresence helm install` as the identity and expects it to succeed, followed by a
  passing `rbac verify install`. A second case does the same with x509 auth enabled.

---

## 4. `telepresence rbac generate client`

The chart already renders the client roles (`templates/clientRbac/connect.yaml`,
`namespace-scope.yaml`, `cluster-scope.yaml`). Since #4266 their content depends on
the installed configuration: required grant, external control endpoint,
`clientRbac.legacyAccess`, and whether the manager is cluster-wide or limited by
`namespaces` / `namespaceSelector`. The generator reuses those templates instead of
copying their rules, so it can't fall out of step with the chart.

**Behavior**

* Read the installed release's values (the release probe `setup` already runs,
  `probe_release.go`) to learn the manager namespace, scope, required grant,
  external endpoint and legacy access. `--input` overrides them for an install that
  doesn't exist yet.
* Reading the release needs read access to the Helm secrets in the manager
  namespace, which the people this command is for often don't have. When that read is
  refused, the command stops with a message saying so and asking for `--input` with
  the values the manager was installed with.
* Render the chart with `clientRbac.create=true`, the given `--subject`s and
  `--namespace`s, and keep only the objects from the `clientRbac` templates.
* Reject a `--namespace` that the manager doesn't manage, with a message naming the
  manager's scope.
* `--format values` emits the equivalent `clientRbac:` values block instead, for
  people who manage the install through Helm.

**Tests**

* Cluster-wide install: a ClusterRole with the namespace rules, plus the connect Role.
* Namespaced install with two namespaces: two Roles and the connect Role.
* External endpoint published: no `pods/portforward` rules, matching `rbac.md`.
* `legacyAccess: true`: the legacy grants are present.
* Release secrets not readable: the `--input` message.
* `--format values` round-trips: rendering the chart with the emitted values gives
  the same objects as the `rbac` format.

---

## 5. `telepresence rbac verify`

**`verify install`** runs the item 3 sweep and prints what's missing. The exit code is
non-zero when anything is denied, so it works in CI. It takes the same `--input` and
`--verbs` as `generate install`.

**`verify client`** doesn't check every rule in the rendered client role, because the
manager accepts alternatives: with the default required grant either the
`telepresence.io` grant or `pods/portforward` passes, and agent provisioning and
namespace reads accept `create` or `get` on `attachments`. Checking every rule would
tell a user who holds only the `telepresence.io` grants that `pods/portforward` is
missing, although attaching works without it.

Instead it runs the shared reviews from item 2 (`ConnectReview`, `AttachmentReview`,
`EnsureAgentReview`, `NamespaceReview` and the `logs` review) under the installed
required grant, and reports a miss only when no alternative passes. The transport
need is reported separately: `pods/portforward` on the manager pod is what a client
needs to reach the manager when no external endpoint is published, whatever the
required grant.

* `--format json|yaml` reuses `DeniedAttribute`, now with `Subresource`.
* Admins check someone else with `--as` / `--as-group`, as for `generate`.

**Tests**

* `verify install` with the fake reactor: the denied list and the exit code.
* `verify client` for each grant mode: a user with only the `telepresence.io` grants
  passes under `telepresence` and `any`, a user with only `pods/portforward` passes
  under `portforward` and `any`, and neither is told about the alternative it
  doesn't need.
* No external endpoint and no `pods/portforward` on the manager pod: reported as a
  transport miss, separate from the review results.
* JSON output matches `DeniedAttribute`, including subresources.

---

## Docs and release notes

* `docs/reference/rbac.md`: a short "Generating RBAC" section linking each mode, and
  a pointer from the install and client sections.
* `docs/reference/setup.md`: when `setup` reports missing install privileges, point
  to `telepresence rbac generate install`.
* Regenerate the CLI reference under `docs/reference/cli`.
* `CHANGELOG.yml`: one feature entry.

This plan is removed in the last commit of the PR that completes item 5.

## Decisions from review

1. Subcommands, named `rbac generate install|client` and `rbac verify install|client`.
2. The cert-manager `Certificate` and the x509 RoleBinding are emitted when the values
   enable them. The x509 binding needs the single named `bind` rule above.
3. Upgrade and uninstall verbs wait behind `--verbs lifecycle`. The default is the
   first-install set, not create-only.
