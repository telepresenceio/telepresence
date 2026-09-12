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
| Namespace scale | How many namespaces exist, presented as evidence when the managed-scope question is asked. |
| Existing installation | Whether a `traffic-manager` Helm release already exists, its version, and its current values. |
| Client update | A best-effort check of the latest released client, advisory only. |
| Existing-install health | When a release is found: StatefulSet readiness and recent warning events (a pre-2.32 release still has a Deployment, which is reported as still to be migrated), webhook presence and certificate expiry, agent-injector endpoint readiness, QUIC endpoint state, client/manager version skew, and — under enforcing authentication — whether this client's credentials (a bearer token, or a client certificate) will be accepted. |
| External endpoint prerequisites | Whether the `certificates.cert-manager.io` CRD is served, and which `kubernetes.io/tls` Secrets exist in the manager namespace, with their DNS names and expiry. |
| Routing conflicts | Whether the workstation's local routes overlap the cluster's pod/service subnets (read from the local route table; the remedy is always cluster-side). |

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

### Minimal client permissions

```console
$ telepresence setup --apply
```

Answering yes to "Enforce caller authentication?", yes to "Enable Direct
Connect, so clients reach the traffic-manager at a published address instead
of through the Kubernetes API?", picking an existing TLS Secret (or
cert-manager) for the certificate, and accepting the default `telepresence`
required grant walks the [client permission ladder](../howtos/client-rbac.md)
in one run. The resulting values:

```yaml
security:
  authentication:
    mode: enforcing
  authorization:
    requiredGrant: telepresence
externalEndpoint:
  enabled: true
  tls:
    secretName: traffic-manager-external-tls
clientRbac:
  legacyAccess: false
```

A client configured with the `cluster.managerAddress` that the post-apply
verification prints then connects without any Kubernetes API request: its
kubeconfig credentials carry authentication, and the `telepresence.io`
policy grants bound to `clientRbac.subjects` carry authorization. The
ladder's last rung, `clientRbac.create: false`, is not something setup
decides; add it to the values file by hand when no client should hold a
Kubernetes grant at all. See [Minimize the client's cluster
permissions](../howtos/client-rbac.md) for what each setting removes and
what it requires.

### Re-run with the previous decisions

```console
$ telepresence setup --input values.yaml --output values.yaml
```

`--input` reads a previously generated (or hand-written) values file and
treats its settings as pinned: anything it already decided —
`agentInjector.enabled`/`nodeAgent.enabled` (attach/replace),
`quicTunnel.enabled`/`quicTunnel.service.type` (QUIC), `namespaces` /
`namespaceSelector` (managed scope), `client.cluster.mappedNamespaces`
(mapped namespaces, pinned independently of the managed scope),
`security.authentication.mode` (enforce authentication),
`security.authorization.requiredGrant` (required grant),
`externalEndpoint.enabled` (with its `tls` settings, Direct Connect),
and `clientRbac.legacyAccess` (legacy client access) — is never asked about
again and never silently changed. If a fresh probe recommends something
different, the interactive session asks whether to keep the pinned value
(default: keep); a non-interactive run keeps it and the report carries a
warning note instead. Keys the tool has no opinion about (image, resources,
`logStreaming`, ...) pass through untouched, so an
`--input FILE --output FILE` round trip is lossless. The file is checked
against the chart before anything runs: a key the chart does not define is
an error naming the key, and `client.*` accepts every client setting, since
the chart hands that block to clients as their configuration. This is also how every
setting that used to be its own flag is expressed now: there is no
`--attach`, `--quic`, `--managed-namespaces`, `--client-rbac-subjects`, or
similar — write (or hand-edit) a values file and pass it with `--input`.

### No cluster-admin: hand off to an admin

Run setup as the person who needs the cluster, without admin rights:

```console
$ telepresence setup --output values.yaml
```

The command still computes and writes the proposal; the report itemizes
exactly which privileges are missing (verb, API group, resource, and
namespace for namespaced ones — also available structured, as
`facts.privileges.missingAttributes`, via `--format json`). Hand that list
and `values.yaml` to a cluster admin. Once the admin has granted the missing
privileges (by hand, or via their own RBAC tooling), they run:

```console
$ telepresence setup --input values.yaml --apply
```

Because the input file pins everything the original run already decided,
the admin's run asks nothing new. To also grant the team RBAC to use
Telepresence, add a `clientRbac` block directly to `values.yaml` before
handing it off — the tool has no opinion about it, so it passes through the
round trip unchanged.

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
| `--non-interactive` | Never prompt; unanswered questions fall back to an input-pinned setting or a safe default. A non-TTY stdin behaves the same way automatically. |

Plus the standard kube flags (`--kubeconfig`, `--context`, ...)
and `--manager-namespace`. The traffic-manager's namespace comes from
`--manager-namespace` alone; without it, setup uses an existing
traffic-manager's namespace when it finds one and otherwise `ambassador`.
Also the global
`--format`/`--progress` flags. There is no flag to preset an individual
answer — every one of them is already expressible in an `--input` values file
(see "Re-run with the previous decisions" above); the interview and `--input`
are the only two ways to decide something.

### Non-interactive defaults

Under `--non-interactive` (or whenever stdin is not a TTY), every
unanswered question takes a default. Precedence, highest first: an
input-pinned value, then the default below.

| Setting | Default |
|---------|---------|
| Attach | yes |
| Replace | no |
| Manager upgrade | yes (only relevant when an older release is installed) |
| QUIC | `auto` — the probe verdict decides |
| Node-agent | `auto` — the probe verdict decides |
| Managed scope | no limit; when the probes show cluster-wide install privileges are missing, a managed list containing just the manager namespace |
| Managed namespaces | with a managed scope of namespaces and no input-pinned list: the manager namespace. A managed scope of selector without an input-pinned label selector is an error — the label selector needs a prompt and none is possible non-interactively. |
| Mapped namespaces | none unless an input file's `client.cluster.mappedNamespaces` sets them; not asked interactively. |
| Enforce authentication | yes when this client's own credentials would be accepted under enforcing mode, otherwise no; on an upgrade, the installed release's current mode |
| Required grant | `any`, or `telepresence` when Direct Connect ends up enabled; on an upgrade, the release's current value |
| Direct Connect | never enabled unless an input file pins `externalEndpoint.enabled: true` or the installed release already enables it; a pinned enable with several TLS Secrets, or only an expired one, and no pinned `tls.secretName`, or a cert-manager choice with no pinned issuer and DNS names, is an error |
| Legacy client access | no (`clientRbac.legacyAccess: false`); on an upgrade, the release's current value, which is yes when the release never set it; forced to yes when `apiPort` is overridden in the input or release values |
| Routing conflicts | not accepted; the report carries a warning naming both remedies (see "Routing conflicts" below) |

## The interview

Every question below is skipped when `--input` pins it (see "Re-run with the
previous decisions"); "always" means "unless pinned by the input".

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
5. **Managed scope** (always, presenting the probed namespace count as
   evidence): choose which namespaces the traffic-manager manages — no
   limit, a managed namespace list (`namespaces`), or a label selector
   (`namespaceSelector`). The default is "no limit" for small clusters,
   with a recommendation to limit when the namespace count is large; a
   cluster where cluster-wide privileges are missing defaults to a
   namespace list instead. Mapped namespaces are not part of this question:
   an input file's `client.cluster.mappedNamespaces` sets a client-side
   default for each client's own namespace mapping (local flags/config
   still override it) that is independent of the managed scope — a
   namespace-limited managed scope and a mapped-namespaces default can both
   be set at once.
6. **Enforce authentication** (always): "Enforce caller authentication?"
   Setup first explains what the setting does: enforcing refuses telepresence
   clients older than 2.31, and it is also required for Direct Connect. One
   more line says whether this caller's own kubeconfig would pass. The
   fresh-install default is yes when this kubeconfig would pass (a bearer
   token or a client certificate), otherwise no. On an upgrade the default is
   the installed release's current mode. Answer -> `security.authentication.mode`.
7. **Direct Connect** (only when enforcing, and a TLS Secret or cert-manager
   was found; asked before the required-grant question because it changes
   that question's default): "Enable Direct Connect, so clients reach the
   traffic-manager at a published address instead of through the Kubernetes
   API?" default no. A yes asks which certificate to use: "The endpoint needs
   a TLS certificate that clients can trust. Which one should it use?", with one
   numbered line per `kubernetes.io/tls` Secret in the manager namespace
   (showing its DNS names and expiry) plus, when cert-manager is present,
   "a new one issued by cert-manager". A single Secret is the default; a
   Secret named `traffic-manager-external-tls` is listed first and is the
   default; otherwise there is no default. An expired or soon-to-expire
   certificate is marked and is never the default. Choosing cert-manager
   asks three more prompts: "cert-manager issuer name:", "cert-manager
   issuer kind (Issuer or ClusterIssuer) [ClusterIssuer]:", and "DNS names
   clients will use to reach the endpoint (comma-separated):". The Service
   type itself is not asked: `LoadBalancer` when the QUIC probe found it
   viable, `NodePort` otherwise, with a note that the DNS names must then
   resolve to a node address. When enforcing but neither a Secret nor
   cert-manager exists, the question is skipped and an info note names
   both prerequisites. Answer -> `externalEndpoint.enabled` and its `tls`
   settings.
8. **Required grant** (only when enforcing): "Which grant should the
   traffic-manager require for authorization?"

   1. `telepresence` — Telepresence's own policy grants; clients lose
      direct traffic-agent port-forwards unless QUIC is enabled.
   2. `portforward` — the `pods/portforward` permission.
   3. `any` — either grant satisfies the check.

   The default is `telepresence` when Direct Connect was chosen,
   otherwise `any`. On an upgrade the default is the release's current
   value. Answer -> `security.authorization.requiredGrant`.
9. **Legacy client access** (always): "Do clients older than 2.32 need to
   connect to this traffic-manager?" The fresh-install default is no,
   which renders `clientRbac.legacyAccess: false`. On an upgrade the
   default is the release's current value, and yes when the release never
   set it, since that is the chart default. Forced to yes, with a note,
   when `apiPort` is overridden in the input or the release values.
   Answer -> `clientRbac.legacyAccess`.
10. **Routing conflicts** (only when the probe finds an overlap): "Local
    routes overlap the cluster's subnets. Allow the conflicts cluster-wide
    (traffic to those ranges goes to the cluster for every client)?" A yes
    sets `client.routing.allowConflictingSubnets`; a no leaves it to
    individual clients, with a note recommending
    `telepresence connect --vnat <subnet>` for whoever hits the conflict.

## The validation report

Every run — whether or not `--output` or `--apply` is given — ends with a
report. In text mode it has up to three sections:

- **Findings**: one line per probed area (cluster, privileges, quic,
  node-agent, webhook, namespaces, routing, release, authentication, external
  endpoint, and — when an existing release was found — a health subsection
  covering the traffic-manager StatefulSet, the webhook and its certificate,
  agent-injector endpoints, the QUIC endpoint, the external endpoint's health
  when enabled, version skew and, under enforcing authentication, whether
  this client's credentials will be accepted) plus supporting evidence.
- **Proposed configuration**: the generated values document verbatim, plus,
  when upgrading an existing release, the list of keys that would change.
- **Notes**: warnings and informational notes explaining any decision that
  needed one (a routing conflict left unresolved, an `--input` value kept
  over the probe's recommendation, missing install privileges and how to
  hand off to an admin, a Deployment-to-StatefulSet migration on upgrade, a
  reminder that attachments need QUIC enabled alongside the external
  endpoint, ...).

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

- the traffic-manager StatefulSet becomes ready (this is already covered by
  the underlying Helm install's atomic wait; migrating a pre-2.32 Deployment
  to it is part of the same upgrade);
- when QUIC is enabled, setup attempts a real QUIC handshake against the
  endpoint (LoadBalancer ingress address or the allocated NodePort) with a
  short timeout. Any completed handshake, or even a certificate rejection,
  proves a peer answered over UDP, since the manager's CA is generated per
  session and unknown to the probe. When nothing answers, setup narrows the
  cause before reporting: a host that rejects the UDP port outright is a
  LoadBalancer or node that does not forward UDP; a host that answers a TCP
  connect to the same address but stays silent on UDP has the port dropped
  on the way, by a firewall or by a LoadBalancer or node that does not
  forward UDP, and the note names which one to check; a host
  that answers neither is unreachable from this workstation, and the note
  says which interface or gateway the workstation routes it through, and
  points out a Docker bridge, since a kind cluster's node network is only
  reachable from the host that runs Docker;
- when the webhook is enabled, setup checks that the agent-injector Service
  has ready endpoints — the point being that the webhook's
  `failurePolicy: Ignore` lets a broken injector degrade silently: pods
  simply stop getting agents, with no error anywhere;
- when the external endpoint is enabled, setup resolves the published
  Service (waiting briefly for a LoadBalancer ingress, as with QUIC above),
  then confirms the TLS handshake, the version handshake, and — when this
  kubeconfig yields a bearer token — that an authenticated session can be
  opened and closed, printing a ready-to-paste client config block
  (`cluster.managerAddress` and `cluster.managerServerCA`) once the version
  handshake succeeds. When the certificate comes from cert-manager, setup
  first waits for the issued Secret to appear, within the same verification
  window, before probing.

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
