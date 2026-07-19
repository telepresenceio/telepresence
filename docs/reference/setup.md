---
title: Guided cluster setup
description: How "telepresence setup" probes a cluster, asks only the questions the probes leave open, and validates, emits, or applies a fully configured traffic-manager installation.
---

# Guided cluster setup

Installing a traffic-manager that actually fits a cluster means knowing
whether the cluster can expose a QUIC endpoint, whether the node-agent will
pass admission, whether the webhook can reach the API server, how many
namespaces there are, and what (if anything) is already installed. Today
that knowledge comes from reading the reference docs and hand-tuning a
values file. `telepresence setup` closes that gap: given cluster access, it
probes the cluster itself, asks only the questions the probes leave open,
and then validates, writes, or applies a fully configured Helm install.

```console
$ telepresence setup --apply
```

The command never mutates anything on the workstation. Every remedy it
produces is either a cluster-side Helm value or an informative message —
running it is safe to repeat, and safe to run read-only.

## What it probes

`telepresence setup` connects to the cluster (the ordinary kube flags:
`--kubeconfig`, `--context`, `-n`/`--namespace`, `--manager-namespace`) and
runs a set of read-only probes before asking anything:

| Probe | What it determines |
|-------|---------------------|
| Install privileges | Whether the current identity can create everything the chart needs, via `SelfSubjectAccessReview` against the rendered chart objects. When cluster-scoped objects (ClusterRole, MutatingWebhookConfiguration) are denied but namespaced ones are not, it re-checks a namespace-scoped install. |
| QUIC viability | Whether the cluster can expose a working QUIC endpoint, and which Service type to use: `LoadBalancer` when the cluster shows working LB provisioning, `NodePort` when a cluster-wide install has no LB signal, or "unavailable" for a namespaced install with neither. |
| Node-agent viability | Whether nodes are Linux, whether the container runtime is one the agent supports, whether the cluster is GKE Autopilot (unsupported), and a server-side dry-run admission canary that exercises Pod Security admission and any policy engine (Kyverno, Gatekeeper, ...) directly. |
| Webhook viability | Whether the mutating webhook can be created, and whether the cluster has the known API-server-cannot-reach-Services problem some CNIs exhibit. |
| Namespace scale | How many namespaces exist, presented as evidence when the scope question is asked. |
| Existing installation | Whether a `traffic-manager` Helm release already exists, its version, and its current values. |
| Client update | A best-effort check of the latest released client, advisory only. |
| Existing-install health | When a release is found: Deployment readiness and recent warning events, webhook presence and certificate expiry, agent-injector endpoint readiness, QUIC endpoint state, and client/manager version skew. |
| Routing conflicts | Whether the workstation's local routes overlap the cluster's pod/service subnets (read from the local route table; the remedy is always cluster-side). |
| Sample workloads | A handful of Deployments in the relevant namespaces, used to personalize the next-steps epilogue. |

Every probe tolerates denied permissions — a probe that cannot get an
answer is reported as unknown rather than aborting the command.

## Typical flows

### Quick start: install now

```console
$ telepresence setup --apply
```

Probes the cluster, asks whatever the probes leave open, installs or
upgrades the traffic-manager with the resulting values, and runs the
post-apply verification (see below).

### GitOps: generate a values file

```console
$ telepresence setup --output values.yaml
```

Probes and interviews exactly as above, but instead of installing, writes
the resulting configuration to `values.yaml` and stops. The file is a normal
Helm values document — commit it, review it, and hand it to your existing
pipeline:

```console
$ helm install traffic-manager datawire/telepresence-oss -n ambassador -f values.yaml
```

`telepresence setup --output -` writes the values document to stdout
instead of a file and suppresses the report, so it composes with a pipe:

```console
$ telepresence setup --non-interactive --output - | helm install -f - traffic-manager datawire/telepresence-oss -n ambassador
```

Under `--output -`, prompts and progress move to stderr so stdout stays a
clean values stream, whether the session is interactive or not (combine
`--output -` with `--non-interactive` for a fully scripted pipeline).
`--output -` cannot be combined with `--format`; both would claim stdout.

### Re-run with the previous decisions

```console
$ telepresence setup --input values.yaml --output values.yaml
```

`--input` reads a previously generated (or hand-written) values file and
treats its settings as pinned: anything it already decided —
`agentInjector.enabled`/`nodeAgent.enabled` (attach/replace),
`quicTunnel.enabled`/`quicTunnel.service.type` (QUIC), `namespaces` /
`namespaceSelector` / `client.cluster.mappedNamespaces` (scope), and the
`clientRbac.*` keys — is never asked about again and never silently
changed. If a fresh probe recommends something different, the interactive
session asks whether to keep the pinned value (default: keep); a
non-interactive run keeps it and the report carries a warning note instead.
Keys the tool has no opinion about (image, resources, ...) pass through
untouched, so an `--input FILE --output FILE` round trip is lossless.

### No cluster-admin: hand off to an admin

Run setup as the person who needs the cluster, without admin rights:

```console
$ telepresence setup --output values.yaml
```

The report lists exactly which privileges are missing. Generate ready-to-
review RBAC manifests covering them:

```console
$ telepresence setup --rbac-out rbac.yaml
```

`rbac.yaml` contains a ClusterRole/ClusterRoleBinding pair for the
cluster-scoped denials and a Role/RoleBinding pair per namespace for the
namespaced ones, with an empty subjects list and a commented example the
admin fills in with the requester's identity. Hand `rbac.yaml` and
`values.yaml` to an admin, who applies the RBAC and then runs:

```console
$ telepresence setup --input values.yaml --apply
```

Because the input file pins everything the original run already decided,
the admin's run asks nothing new.

## Command reference

Neither `--output` nor `--apply` given: the command probes, interviews, and
prints a report — it validates the setup without writing or changing
anything. `--output` additionally writes the values file. `--apply`
additionally installs or upgrades the traffic-manager. There is no separate
confirmation step: `--apply` itself is the consent.

| Flag | Meaning |
|------|---------|
| `--output FILE` | Write the resulting values.yaml, suitable for a Helm install. `-` writes it to stdout and silences all other output to stderr, so `telepresence setup --output - \| helm install -f -` works, interactively or not. Combining `--output -` with `--format` is an error. |
| `--input FILE` | Read a previous values file; its settings become pinned defaults (see "Re-run with the previous decisions" above). |
| `--apply` | Install or upgrade the traffic-manager with the resulting values. |
| `--non-interactive` | Never prompt; unanswered questions fall back to flag values, input-pinned settings, or safe defaults. A non-TTY stdin behaves the same way automatically. |
| `--attach[=bool]` | Answer "will clients attach to workloads?" |
| `--replace[=bool]` | Answer "will you use the replace command?" |
| `--upgrade-manager[=bool]` | Answer "upgrade the existing traffic-manager?" |
| `--quic=auto\|on\|off` | Override the QUIC probe verdict. |
| `--node-agent=auto\|on\|off` | Override the node-agent probe verdict. |
| `--scope=all\|namespaces\|selector\|mapped` | Answer the namespace-scope question. |
| `--managed-namespaces=LIST` | Namespace list when `--scope=namespaces` or `--scope=mapped`. |
| `--rbac-out FILE` | When install privileges are missing, write ready-to-review RBAC YAML covering them to this file. |
| `--client-rbac-subjects=LIST` | Grant these subjects (`kind:name` for `User`/`Group`, `ServiceAccount:name:namespace`) the RBAC needed to use Telepresence: sets `clientRbac.create`, `clientRbac.namespaces` (from the chosen scope), and `clientRbac.subjects` in the generated values. |

Plus the standard kube flags (`--kubeconfig`, `--context`, `-n` for the
manager namespace) resolved the same way `telepresence helm install` resolves
them.

### Non-interactive defaults

Under `--non-interactive` (or whenever stdin is not a TTY), every
unanswered question takes a default. Precedence, highest first: explicit
flag, then an input-pinned value, then the default below.

| Setting | Default |
|---------|---------|
| Attach | yes |
| Replace | no |
| Manager upgrade | yes (only relevant when an older release is installed) |
| QUIC | `auto` — the probe verdict decides |
| Node-agent | `auto` — the probe verdict decides |
| Scope | no limit; when the probes show cluster-wide install privileges are missing, a managed list containing just the manager namespace |
| Managed namespaces | with `--scope=namespaces` and no list: the manager namespace. `--scope=mapped` without `--managed-namespaces` is an error. `--scope=selector` is an error — the label selector has no flag and needs a prompt. |
| Routing conflicts | not accepted; the report carries a warning naming both remedies (see "Routing conflicts" below) |

## The interview

Every question below is skipped when a flag already answers it or when
`--input` pins it (see "Re-run with the previous decisions"); "always"
means "unless answered by a flag or pinned by the input".

1. **Attach or cluster-access-only** (always): "Will clients attach to
   workloads (intercept/replace/ingest/wiretap), or is this cluster access
   only?" A no disables all agent machinery
   (`agentInjector.enabled: false`, `nodeAgent.enabled: false`) and reduces
   the RBAC footprint accordingly.
2. **Replace usage** (only if attach = yes and the node-agent is viable):
   "Will you use the replace command?" Replace needs the injection
   machinery and is not supported in node-agent mode; a yes keeps the
   webhook enabled alongside the node-agent. Skipped when the node-agent is
   not viable, since the webhook is required regardless.
3. **Client upgrade** (only if a newer client was found): advisory only —
   the tool prints the newer version and never attempts a self-update.
4. **Manager upgrade** (only if an older release is installed): "traffic-
   manager X.Y.Z is installed, client is X.Y.Z+n — upgrade?" A newer
   manager than the client inverts the message: it recommends upgrading the
   client instead, and never proposes downgrading the manager.
5. **Namespace scope** (always, presenting the probed namespace count as
   evidence): choose between no limit, a managed namespace list
   (`namespaces`), a label selector (`namespaceSelector`), or mapped
   namespaces (`client.cluster.mappedNamespaces`, delivered to clients as
   their default — local flags/config still override it). The default is
   "no limit" for small clusters, with a recommendation to limit when the
   namespace count is large; a cluster where cluster-wide privileges are
   missing defaults to a namespace list instead.
6. **Routing conflicts** (only when the probe finds an overlap): "Local
   routes overlap the cluster's subnets. Allow the conflicts cluster-wide
   (traffic to those ranges goes to the cluster for every client)?" A yes
   sets `client.routing.allowConflictingSubnets`; a no leaves it to
   individual clients, with a note recommending
   `telepresence connect --vnat <subnet>` for whoever hits the conflict.

## The validation report

Every run — whether or not `--output` or `--apply` is given — ends with a
report. In text mode it has up to three sections:

- **Findings**: one line per probed area (cluster, privileges, quic,
  node-agent, webhook, namespaces, routing, release, and — when an existing
  release was found — a health subsection covering the traffic-manager
  Deployment, the webhook and its certificate, agent-injector endpoints, the
  QUIC endpoint, and version skew) plus supporting evidence.
- **Proposed configuration**: the generated values document verbatim, plus,
  when upgrading an existing release, the list of keys that would change.
- **Notes**: warnings and informational notes explaining any decision that
  needed one (a routing conflict left unresolved, an `--input` value kept
  over the probe's recommendation, an RBAC file written for a missing
  privilege, ...).

The final line is always `Action: <action>` — `install`, `upgrade`, or
`none` when applying, prefixed with `would-` when not (`would-install`,
`would-upgrade`). When the action is an install, the report also lists what
apply will create, grouped by kind and name, and notes that
`telepresence helm uninstall` removes the release's resources, but leaves
the manager namespace behind if setup created it.

`telepresence setup --format json` (or `--format yaml`) prints the same
information as one structured object (`facts`, `answers`, `proposal`,
`action`, and, after an apply, `plannedObjects`/`applyOutcome`/
`verification`) instead of the sectioned text — this is the standard way
to attach cluster state to a bug report, and is exactly what the existing-
install health checks make useful even when nothing needs to change.

## Post-apply verification

After `--apply` installs or upgrades the traffic-manager, setup verifies
the result instead of assuming success:

- the traffic-manager Deployment becomes ready (this is already covered by
  the underlying Helm install's atomic wait);
- when QUIC is enabled, setup attempts a real QUIC handshake against the
  endpoint (LoadBalancer ingress address or the allocated NodePort) with a
  short timeout. Any completed handshake, or even a certificate rejection,
  proves a peer answered over UDP, since the manager's CA is generated per
  session and unknown to the probe; a timeout means the endpoint is not
  reachable from this workstation. The verification note names the likely
  causes: a firewall, the LoadBalancer still provisioning, or an
  unreachable node network (the case a local kind cluster's Docker network
  produces honestly);
- when the webhook is enabled, setup checks that the agent-injector Service
  has ready endpoints — the point being that the webhook's
  `failurePolicy: Ignore` lets a broken injector degrade silently: pods
  simply stop getting agents, with no error anywhere.

A successful apply — or a validation run against an already-healthy
existing release — ends with a personalized next-steps epilogue:
`telepresence connect`, `telepresence list`, and, when a sample workload was
found, an example `telepresence intercept` naming it.

## See also

- [Install Traffic Manager](../install/manager.md) — the plain
  `telepresence helm install` path setup builds on.
- [RBAC](rbac.md) — the full permission reference for administrators who
  want to manage RBAC by hand.
- [QUIC tunnel transport](quic-transport.md) — what the QUIC endpoint is
  for and how clients use it once available.
- [Node-hosted traffic-agent](node-agent.md) — the node-agent mode setup's
  probe evaluates.
- [Telepresence and VPNs](vpn.md) — background on the routing-conflict
  probe's subject matter.
