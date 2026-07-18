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
| `--dry-run` | Probe, interview, and print the proposal; change nothing |
| `--values-out FILE` | Write the generated values.yaml |
| `--non-interactive` | Never prompt; unanswered questions fall back to flag values or safe defaults, and the final confirmation is skipped only with `--yes` |
| `--yes` | Skip the final apply confirmation (not `--force`, which already means force-recreate in kubectl/helm) |
| `--attach[=bool]` | Answer Q1 (clients attach to workloads) |
| `--replace[=bool]` | Answer Q2 (replace command will be used) |
| `--upgrade-manager[=bool]` | Answer Q4 (upgrade existing traffic-manager) |
| `--quic=auto\|on\|off` | Override the QUIC probe verdict |
| `--node-agent=auto\|on\|off` | Override the node-agent probe verdict |
| `--scope=all\|namespaces\|selector\|mapped` | Answer Q5 (namespace limiting strategy) |
| `--managed-namespaces=LIST` | Namespace list when `--scope=namespaces` |

Plus the standard kube flags (`--kubeconfig`, `--context`, `-n` for the
manager namespace, resolved the same way `helm install` resolves it today,
`pkg/client/cli/helm/install.go`).

Prompting is plain stdin/stdout (bufio + `[y/N]`-style questions), no new TUI
dependency. When stdin is not a TTY the command behaves as
`--non-interactive`.

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
question is answerable via flag for scripting:

1. **Attach or VPN-only** (always): "Will clients attach to workloads
   (intercept/replace/ingest/wiretap), or is this cluster access only?"
   VPN-only → no agent machinery at all: `agentInjector.enabled: false`,
   `nodeAgent.enabled: false`, and the report notes the reduced RBAC
   footprint.
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
   (`namespaceSelector`), client-side mapped namespaces (emitted as a
   workstation state manifest `connection.mappedNamespaces` snippet, not a
   helm value), or no limit. The default answer is "no limit" for small
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
  `{facts, answers, proposal, actions}` honoring `--output json|yaml`;
  otherwise sectioned text: Findings, Proposed configuration (the values
  document verbatim), Actions.
- **Dry-run** stops after the report (actions rendered as `would-install` /
  `would-upgrade`, matching the state-manifest verb style).
- **Apply**: confirmation prompt showing the values document (skipped with
  `--yes`), then hand the marshaled values to the existing
  `helm.Request.Run` daemon path — inheriting `Atomic`, `Wait`, and the
  `runManagerHelm` event-watch abort diagnostics for free.
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
  recommend.go      // pure decision engine
  render.go         // values generation + report (manifest/report.go pattern)
  apply.go          // confirmation + helm.Request handoff + verification
pkg/client/cli/cmd/setup.go   // command registration
```

## Testing

- **Unit**: table-driven tests for `Recommend` (the decision table above ×
  QUIC/scope permutations); probe tests against fake clientsets and
  `pkg/k8sapi/fake_auth.go`.
- **Integration** (`integration_test/setup_test.go`): against the kind test
  cluster — `setup --dry-run --non-interactive --attach` should conclude
  node-agent viable (containerd, permissive PSS) and QUIC = NodePort (no
  LB); a full `setup --yes` install followed by connect + a node-agent
  attachment; an upgrade scenario over a pre-installed older-values release
  verifying the merge-and-diff behavior.
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
3. **Mapped-namespaces emission**: when the user picks client-side scoping,
   emit a `WorkstationState` manifest snippet (this branch's schema) via
   `--manifest-out FILE`, printed otherwise.
4. **Default mode**: apply with a confirmation prompt; `--dry-run` proposes
   without changing anything, `--yes` applies without confirmation
   (`--force` was rejected: kubectl and helm both use it to mean
   force-recreate, and setup delegates to helm upgrade).
5. **Namespace scoping**: no automatic threshold — the scope question is
   always asked, with the probed namespace count presented as evidence.
