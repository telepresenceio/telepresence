# Make `enforcing` the default authentication mode

Status: approved on 2026-11-23 with D1 (a), D2 as recommended, D3 (a) and D4 as recommended.

## Goal

A traffic-manager that is installed or upgraded without an explicit
`security.authentication.mode` runs in `enforcing` mode from 2.34.0.
`permissive` and `disabled` stay available as explicit choices.

## Where the default lives today

| Place | What it says |
|-------|--------------|
| `charts/telepresence-oss/values.yaml` | `mode: permissive` |
| `charts/telepresence-oss/values.schema.yaml`, chart README | "permissive (the default)" |
| `managerutil/envconfig.go` | `AuthenticationMode ... default:"permissive"` |
| `auth/mode.go` | an empty mode string becomes permissive |
| `pkg/client/cli/setup/recommend.go` | starts from permissive, writes the answer explicitly |
| Docs | `reference/authentication.md` (mode table, "Staged rollout"), `howtos/client-rbac.md`, `reference/external-endpoint.md` |
| `auth/authorizer.go` | log text "before the enforcing-mode default flips" |

The chart template sets the `AUTHENTICATION_MODE` variable only when the
value is set, so the chart default and the traffic-manager's own default
must both change.

## What changes for a user

**New installation.** Enforcing from the start.

**Who is refused by an enforcing traffic-manager:**

- Clients older than 2.31.0. They send no token.
- Traffic-agents older than 2.31. With the fix in #4412 the upgrade
  replaces them anyway.
- A user without the needed grant. To connect: `create` on
  `connections.telepresence.io` in the traffic-manager's namespace, or
  `pods/portforward` there. To intercept, replace or wiretap: `create` on
  `attachments.telepresence.io` for the workload, or `pods/portforward` in
  its namespace. To ingest: `get` on the attachment. The default
  `security.authorization.requiredGrant: any` accepts either kind, so a
  user who can port-forward in the workload's namespace needs nothing new.
  A cluster administrator needs nothing new.
- A kubeconfig that uses a client certificate, when the x509 listener is
  switched off or the API server is behind an authenticating proxy with
  another CA.

**What an upgrade does depends on how it is run. This is the main
decision (D1).**

| How the upgrade is run | Mode after the upgrade, if the mode was never set |
|------------------------|---------------------------------------------------|
| `telepresence helm upgrade` with no value flags | Stays permissive. The command reuses the old release's values, and Helm then keeps the old chart's defaults too. |
| `telepresence helm upgrade` with `--set` or `-f`, or with `--reset-values` | Enforcing. |
| `helm upgrade` from the chart, Argo CD, Flux, `helm template` | Enforcing, unless `--reuse-values` is used. |
| `telepresence setup --apply` | Asks. The proposed answer becomes enforcing. |

So with no other change, the most common upgrade command keeps existing
installations on permissive, silently and for every later upgrade.

## Decisions

### D1. Should an existing installation switch on upgrade?

- **(a) Yes.** Change `telepresence helm upgrade` so that, when no value
  flags are given, it takes the new chart's defaults and puts the values
  the user set earlier on top (Helm's "reset then reuse"). An
  installation that never set the mode becomes enforcing. One that set
  `permissive` explicitly stays permissive. This also makes every future
  default change reach upgraded installations, which it does not today.
- **(b) No.** Leave the command as it is. Only new installations and
  the other upgrade paths switch. The release note tells administrators
  how to switch.

I recommend (a), with a release note that names who is refused and how
to stay on permissive (`--set security.authentication.mode=permissive`).
The promise made for 2.34.0 was the default, and (b) leaves most existing
installations where they are.

A consequence of (a) to accept: other chart defaults that changed since
an installation was made also take effect on its next upgrade.

### D2. Client RBAC defaults

`clientRbac.create` is `false` and `requiredGrant` is `any`. I recommend
leaving both. Administrators who restrict port-forwarding grant the
`telepresence.io` roles themselves, as `howtos/client-rbac.md` describes.

### D3. Where the work lives

- **(a) In the private audit fork**, as one more pull request on top of
  the follow-up branch.
- **(b) As a public pull request** against `release/v2` now.

I recommend (a). The change itself describes no weakness, but most of
the work is in the regression suite, and that work has to cover the
suites that exist only in the fork. Done in public first, it would have
to be done a second time when the fork is merged.

### D4. Regression suite

`managers.Default` sets no mode, so 81 suites follow the chart default.
I recommend keeping it that way: the suites then test what users get.
Suites that need permissive say so with `managers.AuthPermissive()`.
Compatibility runs with a client older than 2.31 pin permissive.

## Work items

1. **Defaults.** Chart value, schema text, chart README, the
   traffic-manager's own default, the empty-string rule, and the log text
   in `authorizer.go`. Check that a release made with a chart older than
   2.31, which has no `security` values at all, still renders.
2. **Upgrade command** (if D1 is (a)). `telepresence helm upgrade`
   without value flags uses the new chart defaults plus the user's earlier
   values. `--reuse-values` and `--reset-values` keep their meaning.
3. **Setup.** The proposed answer follows the new default. No other
   change.
4. **Docs.** Mode table, the "Staged rollout" section (rewritten as "how
   to move an existing installation to enforcing, or stay on
   permissive"), `howtos/client-rbac.md`, `reference/external-endpoint.md`,
   `install/manager.md` (what an upgrade does with values), and a
   changelog entry of type `change`. No text that describes permissive
   mode as a weakness.
5. **Regression suite.**
   - Find every suite that relies on permissive while using the default
     spec, and give it `AuthPermissive()`. Known so far: the permissive
     SFTP suite and the node-agent file tests.
   - Make the default test identity pass the enforcing reviews. It
     already has `pods/portforward` and the chart's client RBAC.
   - Pin permissive for compatibility runs with clients older than 2.31.
   - New tests:
     - A fresh installation with no mode set refuses a call without a
       token.
     - An upgrade from the released 2.33.4 chart with
       `telepresence helm upgrade` ends in enforcing when the mode was
       never set, and stays permissive when it was set explicitly.
   - Run the whole suite. I expect several rounds; this is the largest
     part of the work.
6. **Chart tests** (`regression_test/golden`). They set the mode
   explicitly today; add the "not set" case.

## Not in scope

- Removing permissive or disabled mode.
- Changing `requiredGrant` or `clientRbac.create`.
- Any startup or install warning about the mode.
