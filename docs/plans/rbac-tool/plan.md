# Plan: an RBAC assistant (`telepresence rbac`)

Implements #4215. Four work items, in order. Item 1 is a refactor that items 2–4
build on. Items 2, 3 and 4 are independent of each other and can land as separate PRs.

The command answers two questions against a cluster the caller may not own: "what
does this identity need to install Telepresence" and "what does this identity need to
use it". Every output is plain RBAC YAML (or a `clientRbac` values block) that a
cluster admin can review and apply. Nothing is ever applied by the command itself.

```text
telepresence rbac install [--input values.yaml] [--subject KIND:NAME]... [--verbs install|upgrade]
telepresence rbac client  --subject KIND:NAME... [--namespace NS]... [--format rbac|values]
telepresence rbac verify  [install|client] [--input values.yaml] [--namespace NS]...
```

---

## 1. Share the chart render and access sweep with `setup`

`pkg/client/cli/setup/probe_rbac.go` already does the hard part for P1: it renders the
embedded chart client-side (`loadEmbeddedChart`, `renderChart`), decodes it
(`decodeManifests`), turns every object into `ResourceAttributes`
(`attributesForObjects`, `extraChecks`, `dedupeAttributes`) and checks them with
bounded concurrency (`sweepAccess`). The RBAC assistant has to see exactly the same
object set, or its output drifts from what `setup` reports.

**Change** — new package `pkg/client/cli/rbac`

* Move the render, decode, attribute and sweep helpers out of `setup` into an
  exported API, for example `rbac.RenderChart(ctx, ns, values)`,
  `rbac.InstallAttributes(objs, ns, nsExists, values)` and
  `rbac.Sweep(ctx, client, attrs) (denied, error)`.
* `setup` calls the new package. `probeRBAC`, `evaluateChartAccess` and
  `PlannedObjects` keep their behavior and their tests; this item changes no output.

**Tests**

* The existing `probe_rbac_test.go` and `planned_test.go` pass unchanged.
* Move the helper-level tests that exercise the moved functions next to them.

**Done when** — `telepresence setup` output is byte-identical before and after.

---

## 2. `telepresence rbac install`

**Behavior**

* Render the chart for the intended values: the embedded defaults, overlaid with
  `--input` (the same loader `setup` uses, `setup.LoadInputValues`).
* Build the attribute set from item 1 and sweep it with `SelfSubjectAccessReview`.
* Emit manifests that cover only the denied pairs:
  * one `ClusterRole` plus `ClusterRoleBinding` for cluster-scoped kinds
    (ClusterRole, ClusterRoleBinding, MutatingWebhookConfiguration, Namespace, ...),
  * one `Role` plus `RoleBinding` per namespace for namespaced kinds,
  * rules grouped by `(apiGroup, resource)`, with `resourceNames` set when the chart
    names the object, so the result is no broader than the install.
* Subjects come from `--subject` (`User:alice`, `Group:devs`,
  `ServiceAccount:ns/name`). When none is given, use the requester's identity from a
  `SelfSubjectReview` (`authentication.k8s.io/v1`), so the output names who asked.
* Output is deterministic (sorted rules, sorted documents, stable names such as
  `telepresence-install-<namespace>`), and starts with a comment header saying which
  Telepresence version and values produced it and what the admin should check.
* `--all` skips the sweep and emits the complete set, for GitOps flows where the
  generator has no access to the target cluster.

**Two RBAC details the output must handle**

* *Escalation prevention.* Creating the chart's Roles and ClusterRoles requires the
  installer to already hold every permission those roles grant, or to hold `escalate`
  on roles and `bind` on bindings. The probe only checks `create` on the role objects,
  so it can't see this. The generator adds the union of the rules in the chart's own
  Roles and ClusterRoles to its output, scoped the same way, and says so in the header.
  It never offers `escalate` or `bind` as the default, because that grant is broader
  than the install.
* *Verbs beyond create.* `create` covers a first install. `helm upgrade` and
  `telepresence helm uninstall` also need `get`, `list`, `update`, `patch` and
  `delete` on the same objects, plus the Helm storage secrets. `--verbs install`
  (the default) emits create only. `--verbs upgrade` adds the lifecycle verbs. The
  sweep checks the same verb set it emits.

**Tests** — `pkg/client/cli/rbac`, with a fake clientset whose
`SelfSubjectAccessReview` reactor denies a chosen set (the pattern `probe_rbac_test.go`
already uses):

* Nothing denied: no manifests, and a message saying the identity can install.
* A denied webhook plus a denied namespaced Role: exactly one ClusterRole rule and one
  Role rule, in the right namespace, with `resourceNames`.
* Chart Roles present: their rules appear in the escalation section.
* `--verbs upgrade`: the lifecycle verbs appear and the sweep checked them.
* Golden-file test of the full output for the default values, so changes to the
  chart show up as a reviewable diff.

---

## 3. `telepresence rbac client`

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
* `--format values` round-trips: rendering the chart with the emitted values gives
  the same objects as the `rbac` format.

---

## 4. `telepresence rbac verify`

**Behavior**

* `verify install` runs the item 2 sweep and prints what's missing. The exit code is
  non-zero when anything is denied, so it works in CI.
* `verify client` builds the item 3 rule set for the current identity and checks
  each rule with `SelfSubjectAccessReview`. The `telepresence.io` resources
  (`connections`, `attachments`, `logs`) are never served by the API server, but the
  authorizer still evaluates them, so the same review works for them.
* `--format json|yaml` reuses the `DeniedAttribute` shape `setup` already exposes.
* Admins can check someone else with the global kubeconfig impersonation flags
  (`--as`, `--as-group`). No new flags are needed.

**Tests**

* Install and client checks with the fake reactor: the denied list and the exit code.
* JSON output matches `DeniedAttribute`.

---

## Docs and release notes

* `docs/reference/rbac.md`: a short "Generating RBAC" section linking each mode, and
  a pointer from the install and client sections.
* `docs/reference/setup.md`: when `setup` reports missing install privileges, point
  to `telepresence rbac install`.
* Regenerate the CLI reference under `docs/reference/cli`.
* `CHANGELOG.yml`: one feature entry.

This plan is removed in the last commit of the PR that completes item 4.

## Open questions

1. Command name: `telepresence rbac` with subcommands, or a single command with
   `--mode`? The plan assumes subcommands.
2. Should `rbac install` also cover the cert-manager `Certificate` and the
   `kube-system` x509 RoleBinding that `probeRBAC` checks separately, or only report
   them? The plan assumes it emits them when the values enable those features.
3. Is `--verbs upgrade` worth it in the first version, or should upgrade
   permissions wait for demand?
