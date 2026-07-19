# Auto-configuration: `telepresence setup`

## Summary

A guided tool that, given cluster access, probes the cluster, asks the user a
small number of gated questions, and then either proposes (`--dry-run`) or
applies a fully configured traffic-manager installation. It closes the gap
between "helm install with defaults" and the hand-tuned values files that
QUIC, node-agent, webhook, and namespace-scoping decisions require today.

The tool produces three artifacts:

1. A generated Helm values document (printable and savable via
   `--values-out`, suitable for GitOps).
2. A human-readable (or `--output json|yaml`) report of findings,
   recommendations, and planned actions.
3. On apply: an installed/upgraded traffic-manager via the existing
   `helm.Request`/`EnsureTrafficManager` path, followed by post-install
   verification.

## Command surface

New top-level command:

```
telepresence setup [flags]
```

| Flag | Meaning |
|------|---------|
| `--output FILE` | Write the resulting values.yaml, suitable for a Helm install. `-` writes it to stdout and silences all other output: the report is suppressed and prompts/advisories go to stderr, so `setup --output - \| helm install -f -` works, interactively or not. Combining `--output -` with `--format` is an error (both claim stdout) |
| `--input FILE` | Read such a values file; its settings become pinned defaults (see Input pinning) |
| `--apply` | Install/upgrade the traffic-manager with the resulting values |
| `--non-interactive` | Never prompt; unanswered questions fall back to flag values, input-pinned settings, or safe defaults |
| `--attach[=bool]` | Answer Q1 (clients attach to workloads) |
| `--replace[=bool]` | Answer Q2 (replace command will be used) |
| `--upgrade-manager[=bool]` | Answer Q4 (upgrade existing traffic-manager) |
| `--quic=auto\|on\|off` | Override the QUIC probe verdict |
| `--node-agent=auto\|on\|off` | Override the node-agent probe verdict |
| `--scope=all\|namespaces\|selector\|mapped` | Answer Q5 (namespace limiting strategy) |
| `--managed-namespaces=LIST` | Namespace list when `--scope=namespaces` |

All flags are optional and every combination is allowed. The mode falls out
of `--output`/`--apply`: with neither, the command probes, interviews, and
reports — it simply validates the setup. `--output` additionally writes the
values file; `--apply` additionally performs the install/upgrade. There is
no separate confirmation step and no `--dry-run`/`--yes`: not passing
`--apply` is the dry run, and passing it is the consent.

A local `--output FILE` deliberately shadows the deprecated hidden global
`--output` format flag; the output package detects the shadowing by the
global flag's `"default"` default value and leaves the local flag alone, and
structured output remains available via `--format`.

Plus the standard kube flags (`--kubeconfig`, `--context`, `-n` for the
manager namespace, resolved the same way `helm install` resolves it today,
`pkg/client/cli/helm/install.go`).

### Non-interactive defaults

With `--non-interactive` (or a non-TTY stdin) every unanswered question takes
a default. Precedence per setting: explicit flag > input-pinned value >
default. The defaults are:

| Setting | Default |
|---------|---------|
| attach | yes |
| replace | no |
| manager upgrade | yes (only relevant when an older release is installed) |
| QUIC | `auto` — the probe verdict decides |
| node-agent | `auto` — the probe verdict decides |
| scope | no limit; when the probes show cluster-wide privileges are missing, a managed list containing just the manager namespace |
| managed namespaces | with `--scope=namespaces` and no list: the manager namespace; `--scope=mapped` without `--managed-namespaces` is an error; `--scope=selector` is an error (the label selector has no flag and needs a prompt) |

Prompting is plain stdin/stdout (bufio + `[y/N]`-style questions), no new TUI
dependency. When stdin is not a TTY the command behaves as
`--non-interactive`.

### Input pinning (`--input`)

The input file is a Helm values document (typically one produced by
`--output`, but any traffic-manager values file works). Its settings are
authoritative defaults:

- A setting present in the input is never asked about when it doesn't
  conflict with the probes' conclusions: `agentInjector.enabled` /
  `nodeAgent.enabled` pin the attach/replace decisions they imply,
  `quicTunnel.enabled` (and `quicTunnel.service.type`) pin the QUIC choice,
  and `namespaces` / `namespaceSelector` /
  `client.cluster.mappedNamespaces` pin the scope question.
- A pinned setting is never changed without consulting the user. When the
  decision engine would choose differently, the conflict is put to the user
  (`The input sets <key>=<v>; probing recommends <w>. Keep the input value?
  [Y/n]`, default keep). In non-interactive mode the input value is kept and
  the report carries a warning note instead.
- Hard incompatibilities stay errors regardless (e.g. the input enables the
  agent-injector but the webhook cannot be created): validation's job is to
  surface them, not to negotiate.
- Keys the engine has no opinion about (image, resources, ...) pass through
  to the result untouched, so an `--input FILE --output FILE` round trip is
  lossless.

Value precedence, bottom to top: existing release values (when upgrading) <
input values < engine decisions for unpinned keys and consented changes.

## Probes (cluster analysis)

All probes populate a single `ClusterFacts` struct so the decision engine
stays a pure function. Probes run client-side directly against the
kubeconfig (client-go), not through the user daemon; only the final apply is
delegated to the daemon via the existing helm path.

### P1: Install privileges

Rather than a hard-coded resource list, render the embedded chart
(`charts/chart.go`, helm SDK template action) with the candidate values and
issue a `SelfSubjectAccessReview` (`pkg/k8sapi/cani.go`) for `create` on
every rendered object, plus:

- `create namespaces` when the manager namespace does not exist
  (`--create-namespace` semantics),
- `create secrets` in the manager namespace (helm storage driver is
  `secrets`),
- `patch namespaces` on the manager namespace when node-agent is in play
  (privileged Pod Security Standard label).

If cluster-scoped objects (ClusterRole, MutatingWebhookConfiguration) are
denied but namespaced ones are allowed, re-render with a namespace-scoped
value set (`namespaces: [...]`) and re-check, so the tool can propose a
namespaced install instead of just failing. Missing privileges are reported
as an itemized list.

### P2: QUIC viability

`quicTunnel.enabled` is safe to turn on (clients fall back silently to
port-forwarded gRPC), so this probe is about picking a *working* service
type, classified with a confidence level rather than a hard yes/no:

- **LoadBalancer likely**: any existing `type: LoadBalancer` Service in the
  cluster has populated `status.loadBalancer.ingress`, or node
  `spec.providerID` indicates a cloud (gce/aws/azure/...). Recommend the
  default `quicTunnel.service.type: LoadBalancer`.
- **NodePort viable**: no LB signal (kind/minikube/k3d/bare metal), install
  is cluster-wide (NodePort discovery needs Node list/watch RBAC —
  `docs/reference/quic-transport.md`), and nodes expose addresses. Recommend
  `type: NodePort`.
- **Unavailable**: namespaced install with no LB capability → recommend
  `quicTunnel.enabled: false` with an explanatory note.

Constraints encoded: `quicTunnel.enabled` requires `replicaCount: 1` (chart
`fail`s otherwise); actual UDP reachability from the workstation cannot be
verified pre-install and is deferred to post-apply verification.

### P3: Node-agent viability

- All (or some) nodes are Linux (`kubernetes.io/os` label) — record the
  subset.
- CRI supported: `status.nodeInfo.containerRuntimeVersion` prefix maps to
  containerd/CRI-O/cri-dockerd; unknown CRI → viable only with an explicit
  `nodeAgent.criSocket`.
- GKE Autopilot detected (node labels) → not viable (`hostPID` banned,
  `docs/reference/node-agent.md`).
- **Admission canary**: server-side dry-run create
  (`kubectl create --dry-run=server` equivalent) of a minimal Pod in the
  manager namespace with `hostPID: true`, the CRI hostPath mount, and the
  agent's capability set. This exercises Pod Security admission, Autopilot,
  and policy engines (Kyverno/Gatekeeper) in one shot instead of
  heuristically guessing at each. If the manager namespace doesn't exist
  yet, fall back to reading cluster-default PSS configuration and report
  "probable" instead of "confirmed".

### P4: Webhook viability

Needed when the user wants `--replace`, or when node-agent is not viable.

- SSAR for `create mutatingwebhookconfigurations` (covered by P1, surfaced
  separately because it decides a feature, not just install success).
- Known-problem heuristic: API server cannot reach in-cluster Services on
  some clusters (e.g. EKS with a non-VPC CNI); when detected, recommend the
  documented `agentInjector.service.type: NodePort` + `webhook.url` +
  `certificate.altNames` arrangement, otherwise chart defaults.
- Note in the report that `failurePolicy: Ignore` means a broken webhook
  degrades silently; verification happens post-apply.

### P5: Namespace scale

Count namespaces (when listing is permitted). The count is not a decision
threshold — it is presented to the user as evidence when interview Q5 asks
whether to limit scope, together with a recommendation to limit when the
count is large. Mention `maxNamespaceSpecificWatchers` (default 10) in the
rationale when a selector is chosen.

### P6: Existing installation

Locate an existing `traffic-manager` helm release (same lookup the helm
commands use), record its chart/app version and current values
(`helm get values` equivalent). Feeds Q4.

### P7: Client update check

Best-effort fetch of `stable.txt` (the `ann.Tel2` URL format,
`pkg/client/cli/ann/annotations.go`) with a short timeout; compare against
`version.Structured`. Feeds Q3.

## Interview (gated questions)

Questions are only asked when the probes make them relevant, and every
question is answerable via flag for scripting. An answer pinned by the
`--input` file (see Input pinning) is never asked either — "always" below
means "unless answered by a flag or pinned by the input":

1. **Attach or VPN-only** (always): "Will clients attach to workloads
   (intercept/replace/ingest/wiretap), or is this cluster access only?"
   VPN-only → no agent machinery at all: `agentInjector.enabled: false`,
   `nodeAgent.enabled: false`, and the report notes the reduced RBAC
   footprint. An input carrying both of those keys pins this answer (both
   false → VPN-only), so the question is skipped.
2. **Replace usage** (only if Q1 = attach AND node-agent viable): "Will you
   use the replace command?" — replace is implemented by the injection
   machinery and is not supported in node-agent mode, so a yes keeps the
   webhook enabled alongside node-agents. If node-agent is *not* viable the
   question is skipped (the webhook is required regardless).
3. **Client upgrade** (only if P7 found a newer client): advisory only —
   the CLI cannot replace its own binary across install methods, so the
   tool prints the newer version and install instructions; it never
   attempts a self-update.
4. **Manager upgrade** (only if P6 found an older release): "traffic-manager
   X.Y.Z is installed, client is X.Y.Z+n — upgrade?" A *newer* manager than
   the client inverts the message: recommend upgrading the client, never
   downgrade the manager.
5. **Namespace scope** (always asked, presenting the P5 count as evidence):
   choose between a managed namespace list (`namespaces`), a label selector
   (`namespaceSelector`), mapped namespaces (an unrestricted install that
   sets `client.cluster.mappedNamespaces` — the manager delivers it to
   clients as their mapped-namespaces default, which local flags/config
   still override), or no limit. The default answer is "no limit" for small
   clusters, with a recommendation to limit when the namespace count is
   large.

## Decision engine

`Recommend(facts ClusterFacts, answers Answers) (Proposal, error)` — a pure
function, fully unit-testable. Core decision table:

| Attach | Node-agent viable | Replace | Resulting values |
|--------|-------------------|---------|------------------|
| no | - | - | `agentInjector.enabled: false`, `nodeAgent.enabled: false` |
| yes | yes | no | `nodeAgent.enabled: true`, `agentInjector.enabled: false` |
| yes | yes | yes | `nodeAgent.enabled: true`, `agentInjector.enabled: true` |
| yes | no | - | `agentInjector.enabled: true` (webhook required; error if P4 says it cannot be created) |

Orthogonally: QUIC values from P2 (`quicTunnel.enabled`, `service.type`,
plus the `replicaCount: 1` constraint), namespace scoping from Q5, and
`nodeAgent.criSocket` when P3 identified a non-probeable CRI.

For upgrades (Q4 = yes), the proposal is computed as: existing release
values (base) ⊕ recommendation overlay, and the report shows the value-level
diff. This avoids both the `--reuse-values` pitfall (stale values silently
kept) and clobbering deliberate operator settings.

The `Proposal` carries the values map, a list of notes/warnings (with the
probe evidence that produced each decision), and the planned action
(install/upgrade/none).

## Output and apply

- **Report rendering** mirrors `pkg/client/cli/manifest/report.go`:
  `output.WantsFormatted` → one structured object
  `{facts, answers, proposal, actions}` honoring `--format json|yaml`;
  otherwise sectioned text: Findings, Proposed configuration (the values
  document verbatim), Actions.
- **Validation mode** (no `--apply`) stops after the report and any
  `--output` file (actions rendered as `would-install` / `would-upgrade`,
  matching the state-manifest verb style).
- **Apply** (`--apply`): hand the marshaled values to the existing
  `helm.Request.Run` daemon path — inheriting `Atomic`, `Wait`, and the
  `runManagerHelm` event-watch abort diagnostics for free. No confirmation
  prompt: the flag is the consent, and the interview already engaged the
  user for everything debatable.
- **Post-apply verification**:
  - traffic-manager Deployment ready (already covered by Atomic/Wait);
  - QUIC: when enabled, poll the quic Service for LB ingress (LoadBalancer)
    or confirm NodePort allocation; report "QUIC endpoint available" vs
    "no endpoint yet — clients will fall back to gRPC";
  - webhook: best-effort server-side dry-run of an annotated canary pod to
    confirm the injector answers (bounded by the `failurePolicy: Ignore`
    silent-degradation caveat).

## Package layout

```
pkg/client/cli/setup/
  facts.go          // ClusterFacts + probe orchestration (parallel where safe)
  probe_rbac.go     // P1 (chart render + SSAR sweep)
  probe_quic.go     // P2
  probe_nodeagent.go// P3 (incl. admission canary)
  probe_webhook.go  // P4
  probe_scale.go    // P5
  probe_release.go  // P6
  probe_update.go   // P7
  interview.go      // Answers, gating, prompting, flag overrides
  input.go          // --input parsing, pinned-answer derivation, conflicts
  recommend.go      // pure decision engine
  render.go         // values generation + report (manifest/report.go pattern)
  apply.go          // helm.Request handoff + verification
pkg/client/cli/cmd/setup.go   // command registration
```

## Testing

- **Unit**: table-driven tests for `Recommend` (the decision table above ×
  QUIC/scope permutations); probe tests against fake clientsets and
  `pkg/k8sapi/fake_auth.go`.
- **Integration** (`integration_test/setup_test.go`): against the kind test
  cluster — `setup --non-interactive --attach` should conclude node-agent
  viable (containerd, permissive PSS) and QUIC = NodePort (no LB); a full
  `setup --apply` install followed by connect + a node-agent attachment; an
  upgrade scenario over a pre-installed older-values release verifying the
  merge-and-diff behavior; an `--output` / `--input` round trip preserving
  passthrough keys and skipping the pinned questions.
- **Docs**: `docs/reference/setup.md` (or a quick-start slot) + changelog
  entry.

## Milestones

1. Probes + `ClusterFacts` (P1–P7), unit-tested against fakes.
2. Interview + decision engine + dry-run report (tool is useful read-only
   here).
3. Apply path + post-apply verification.
4. Integration tests + docs + changelog.

## Resolved questions

1. **Command name**: `telepresence setup`.
2. **Q3 action**: advisory only; the tool never attempts a client
   self-update.
3. **Mapped-namespaces emission** (superseded 2026-07-19): originally a
   `WorkstationState` snippet via `--manifest-out`. Removed: setup stores
   nothing client-side — its only client-side output is the update-check
   advisory (`ann.UpdateCheckFormat: ann.Tel2`, as on `telepresence
   intercept`). The mapped choice instead sets the Helm value
   `client.cluster.mappedNamespaces`: the chart writes `.Values.client`
   into the manager's `client.yaml`, `GetClientConfig` delivers it at
   session start, and `effectiveMappedNamespaces`
   (`pkg/client/userd/trafficmgr/session.go`) applies it as the default
   with local flag/config priority.
4. **Default mode** (superseded 2026-07-19): originally
   apply-with-confirmation plus `--dry-run`/`--yes` (`--force` was rejected:
   kubectl and helm both use it to mean force-recreate). Replaced by the
   `--output`/`--input`/`--apply` surface: validation is the default, the
   mutating flags are the consent, and no confirmation prompt exists.
5. **Namespace scoping**: no automatic threshold — the scope question is
   always asked, with the probed namespace count presented as evidence.
