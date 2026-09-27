# `telepresence setup` covers the Kafka provider


## Context

`telepresence setup` is the recommended install path for the traffic-manager. It probes the cluster, asks a short interview, writes or applies Helm values, and verifies the result. It knows nothing about the optional Kafka personal-intercept provider: it never asks about it, never checks that the caller may create the provider's objects or its two CRDs, never lists the CRDs among planned objects, and never verifies the provider after apply. Operators today enable Kafka with `telepresence helm upgrade --set kafka.enabled=true`, outside setup's checks.

Decided scope (user): **provider install only**. Setup asks whether to enable the provider, the webhook failure policy and the replica count, probes prerequisites and privileges, applies with CRDs, and verifies. No KafkaSplit creation or detection.

## Facts that shape the design

- Questions are hand-written code in `pkg/client/cli/setup/interview.go` (`Interview`, helpers `askYesNo`, `askChoice`, `askUntilValid`, explanation funcs, `legacyAccessDefault` reading `iv.effective`). `Answers` and `Preset` are parallel structs.
- `input.go`: `PinAnswers` with per-area `pinX`; `ValidateValues` holds the hard rules.
- `recommend.go`: `RecommendWithInput` builds `rec`, reconciles each Helm key with `decide(e, "dotted.key", input, recommended)`, then `helm.MergeValues(rec, input)` and `decideAction` (ChangedKeys via `DiffValues`).
- `probe_rbac.go`: sweeps the chart rendered from `DefaultCandidateValues()` (`facts.go:199`) with SelfSubjectAccessReviews; `extraChecks` has no chart access. The dry-run render omits `crds/`, so CRD permissions are never checked today.
- `verify.go` `VerifyInstall(ctx, ki kubernetes.Interface, ns, values, auth)`; `planned.go` `PlannedObjects` already lists every `templates/kafka.yaml` object when `kafka.enabled` is true, only the CRDs are missing.
- `pkg/client/cli/helm/install.go` already server-side applies the Kafka CRDs (`crds.go` `applyCRDs`, unexported `crdName`) when `vals.Kafka.Enabled`. Values mirror: `helm.Kafka{Enabled, Replicas, Image, Webhook{FailurePolicy, Port, TimeoutSeconds}, Resources}`; chart defaults replicas 2, `Fail`.
- No traffic-manager replica recommendation exists; the single-node signal is `facts.NodeAgent.TotalNodes` (`probe_nodeagent.go`).
- CRD "served" can be checked with `ki.Discovery().ServerResourcesForGroupVersion("kafka.telepresence.io/v1alpha1")` (technique of `certManagerFinding` in `probe_external.go`), no apiextensions client needed. Listing splits needs a REST call (`Discovery().RESTClient().Get().AbsPath(...)`, as `cmd/traffic/cmd/manager/kafka.go` does); the fake clientset's REST client is nil, so the Prober gets an injectable hook like `ReleaseLookup`.
- The mutating-webhook create SSAR the injector probe already issues (`probe_webhook.go`) answers "may I create a MutatingWebhookConfiguration" for Kafka too; `ValidatingWebhookConfiguration` is a new kind for the sweep.

## Design

### 1. Probes

**Kafka-only privilege attribution** (`probe_rbac.go`): render the chart twice, base candidate (Kafka off) and base plus `Kafka.Enabled=true`, take the set difference of `ResourceAttributes` keyed by `attributeKey`, add create and patch on `apiextensions.k8s.io/customresourcedefinitions` for each chart CRD name, and sweep that set separately. Kafka's objects do not depend on the managed scope, so one Kafka sweep serves both the cluster-wide and namespaced findings, and the existing `Missing` lists stay unchanged. Split `evaluateChartAccess` into `chartAttributes` (render, decode, extras, dedupe) and `sweepFinding` (sweep, format); add `evaluateKafkaAccess`. Export `helm.CRDNames(chrt)` in `pkg/client/cli/helm/crds.go`. `PrivilegeFacts` gains `Kafka Finding`, `MissingKafka []string`, `MissingKafkaAttributes []DeniedAttribute`. `DefaultCandidateValues` sets `Kafka.Enabled=false` explicitly.

**New `probe_kafka.go`**, phase "Probing Kafka provider prerequisites" appended to `ProbePhases` and `GatherFacts`:
```go
type KafkaFacts struct {
    CRDs         Finding // splits and routes served under kafka.telepresence.io/v1alpha1
    ArgoRollouts Finding // argoproj.io/v1alpha1 rollouts served; informational
    ActiveSplits int     // KafkaSplit resources whose status.phase is not Disabled; counted only when CRDs are served
    SplitsListDenied bool
    SplitsListError  string
}
```
`ClusterFacts.Kafka KafkaFacts`. Prober hook `KafkaSplits func(ctx) ([]unstructured.Unstructured, error)`, nil means a REST list of `/apis/kafka.telepresence.io/v1alpha1/splits` (nil REST client recorded as `SplitsListError`, Forbidden as `SplitsListDenied`). `render.go` gets a `kafka` findings area and `kafkaSummary` (one line: CRDs present or absent, N active KafkaSplits, Argo Rollouts present or absent), with `MissingKafka` as evidence so the admin hand-off list is complete.

**Health on an existing installation** (`probe_health.go`): when the release enables Kafka, `kafkaProviderFinding` reads Deployment `tp-kafka` and classifies it with `workloadReadiness`; NotFound is a No. Added to `healthLabeledFindings` as "kafka provider". Reused by verification.

### 2. Interview (`interview.go`)

Types: `KafkaFailurePolicyFail = "Fail"`, `KafkaFailurePolicyIgnore = "Ignore"`, `ParseKafkaFailurePolicy`. `Answers` gains `Kafka bool`, `KafkaFailurePolicy string`, `KafkaReplicas int32`; `Preset` gains the three bools.

Placement: in `Interview`, right after the replace question and before the client-update and manager-upgrade messages, gated on `a.Attach` (replace is the attach question's own sub-question; Kafka is the next topic).

`askKafka`:
1. Unless preset: print `explainKafka` when interactive. If the release enables Kafka and `Facts.Kafka.ActiveSplits > 0`, print "N active KafkaSplit resources exist; the Kafka provider stays enabled while they do.", set `a.Kafka = true`, skip the question. Otherwise `askYesNo("Enable Kafka personal intercepts? ", kafkaDefault())`.
2. If enabled and not preset: failure policy via `askChoice` with two options, default from the release value or `Fail`.
3. If enabled and not preset: replicas via a new generic `askInt(prompt, min, def)` on `askUntilValid`; default the release value when the release enables Kafka, else 1 when `TotalNodes == 1`, else 2.

Defaults: `kafkaDefault` is false on a fresh install, else the release's `kafka.enabled` (mirrors `legacyAccessDefault`). Non-interactive takes every default, so Kafka stays off unless pinned or already on.

Texts:
```
Kafka personal intercepts let a developer's local consumer take a filtered
share of a consumer group's records while the in-cluster consumer keeps the
rest. Enabling installs the tp-kafka provider next to the traffic-manager: the
KafkaSplit and KafkaRoute CustomResourceDefinitions, a Deployment, and
admission webhooks. Nothing changes for a workload until a KafkaSplit is
declared for it.
```
Conditional lines: "The installed traffic-manager already enables the Kafka provider."; "Installing the provider needs privileges you lack (<first missing>); a yes still writes the values for an admin to apply."; "The Kafka CRDs are already present in the cluster."

```
While the Kafka provider is down, what should happen to Pods created in
namespaces that hold a KafkaSplit?
  1) Fail: refuse the Pod until the provider is back (a Pod admitted without
     its shadow configuration would consume the source group beside the splitter)
  2) Ignore: admit the Pod unchanged, so it may consume the source group
     beside the splitter
Choose 1-2 [1]:
```
```
The provider runs two replicas for leader failover; one is enough on a
single-node cluster.
Kafka provider replicas [2]:
```

### 3. Pinning (`input.go`)

`pinKafka(in, a, pre)`: `in.Kafka.Enabled` pins `Kafka`; a parseable `Webhook.FailurePolicy` pins the policy; `Replicas >= 1` pins the count. Invalid values stay unpinned so `ValidateValues` reports them on the merged values.

### 4. Recommendation (`recommend.go`)

`e.kafkaValues(rec)` after `securityValues`:
- `decide(e, "kafka.enabled", ...)`; always emit `rec.Kafka.Enabled` (an explicit false is what turns a release's true off through `MergeValues`, and lands in `ChangedKeys`).
- Disabled: if the release enables Kafka, warn "kafka.enabled false removes the tp-kafka provider and its webhooks; the Kafka CRDs and any KafkaSplit/KafkaRoute resources are left in place"; if the split count is unknown, warn that setup could not list KafkaSplit resources.
- Enabled: `decide` for `kafka.webhook.failurePolicy` (default Fail) and `kafka.replicas` (default 2); `checkKafkaPrivileges` through `privilegeDenial` (error when applying, warning plus hand-off note otherwise, same contract as `checkPrivileges`); info note "Kafka personal intercepts enabled: the KafkaSplit and KafkaRoute CRDs are applied before the chart and the tp-kafka provider runs N replica(s); declare a KafkaSplit per consumer workload (see the Kafka intercepts how-to)"; warn on `Ignore`; info when the CRDs already exist and the release did not enable Kafka; warn when Kafka is pinned on while attach is off ("Kafka routes are created by intercepts, which need the agent-injector or the node-agent").
- Image and resources pass through untouched from the input via `MergeValues`.
- Accepted side effect: a release enabled with a bare `--set kafka.enabled=true` gets one values-only upgrade adding replicas and failure policy the first time setup runs.

### 5. Validation (`ValidateValues`)

When Kafka is enabled: the mutating webhook must be creatable (hard, like the injector); failure policy must be Fail or Ignore; replicas at least 1; when applying, `Privileges.Kafka` must not be No (mirrors the cluster-wide rule). When Kafka is disabled while the release enables it and `ActiveSplits > 0`: refuse with "kafka.enabled: false would remove the Kafka provider while N active KafkaSplit resources exist; disable or delete them first, or keep the provider enabled". Reason: removing the provider leaves the splitter StatefulSets, shadow topics and the namespace labels behind with nothing reconciling them, and the next Pod created in such a namespace is admitted without its shadow configuration and consumes the source group beside the still-running splitter. The remedy is precise, so a hard stop beats a warning. An unknown count only warns.

### 6. Planned objects and report

`PlannedObjects`: append "CustomResourceDefinition <name>" for each `helm.CRDNames(chrt)` when Kafka is enabled. `PrintReport`: after the uninstall lines, "The Kafka CRDs and any KafkaSplit/KafkaRoute resources are left in place by 'telepresence helm uninstall'." Optional `(*helm.Values).KafkaEnabled()` accessor in `values_access.go`, used by `install.go`, health, verify, planned, render.

### 7. Verification (new `verify_kafka.go`)

When Kafka is enabled: CRDs served (shared with the probe); `tp-kafka` Deployment ready (shared with health); Mutating and Validating webhook configurations `tp-kafka-<ns>` present. Signature of `VerifyInstall` unchanged. No dry-run KafkaSplit create: it would need `create splits` (which the non-admin path lacks), a body that passes the CRD schema yet fails the webhook, and a REST client the fake clientset cannot supply; Deployment readiness plus present webhook configurations plus served CRDs give the same signal.

### 8. Docs

- `docs/reference/setup.md`: probe table (privileges row mentions the separate Kafka sweep; new "Kafka provider prerequisites" row), re-run list gains the three keys, non-interactive defaults rows (Kafka: no, release value on upgrade; policy: Fail; replicas: 2, or 1 on a single-node cluster), interview item after replace with the exact texts and `kafka.enabled`, `kafka.webhook.failurePolicy`, `kafka.replicas` (renumber the rest), validation report (kafka area, CRD lines, leave-behind note, disable refusal), post-apply verification bullet, See also link.
- `pkg/client/cli/cmd/setup.go` `Long` gains "whether to enable Kafka personal intercepts"; `make docs-files` regenerates `docs/reference/cli/telepresence_setup.md`.
- `docs/reference/kafka-intercepts.md` "Installation and components": setup is the recommended path; `helm install/upgrade` with `kafka.enabled=true` is equivalent; setup refuses to disable the provider while active splits exist.
- `docs/howtos/kafka-intercepts.md` "Install the provider": `telepresence setup --apply` and answer yes; keep the helm command as the alternative.
- `docs/install/manager.md` question list gains Kafka.
- `CHANGELOG.yml`: extend the existing 2.33.0 Kafka entry with a sentence that `telepresence setup` installs the provider; `make docs-files`.

## Files

Core: `pkg/client/cli/setup/{interview.go,input.go,recommend.go,facts.go,probe_rbac.go,probe_health.go,render.go,planned.go,verify.go}`, new `probe_kafka.go`, `verify_kafka.go`; `pkg/client/cli/helm/crds.go` (+ `values_access.go`); `pkg/client/cli/cmd/setup.go`; `regression_test/suites/install/setup.go`; docs listed above.

## Tests

- `probe_rbac_test.go`: Kafka-only denial (deny create validatingwebhookconfigurations and customresourcedefinitions gives ClusterWide Yes, Kafka No, MissingKafka exactly those); Kafka allowed; candidate render contains `Deployment tp-kafka` only with Kafka on; attribute subtraction.
- `probe_kafka_test.go`: CRDs served / absent (fake discovery resources); split hook returns active and disabled splits, Forbidden, error; Argo presence.
- `probe_health_test.go`: provider ready, unready, not found, release disables Kafka.
- `interview_test.go`: fresh default no with prompt `Enable Kafka personal intercepts? [y/N]`; skipped when attach is no; yes asks policy then replicas; single-node default `[1]`; release enabled defaults yes and release replicas; active splits skip the question; preset skips; non-interactive off, on when the release enables; invalid replicas reprompts; question order (after replace, before managed scope); explain lines.
- `input_test.go`: pin cases (enabled, policy and replicas, invalid policy not pinned); ValidateValues cases (webhook, policy, replicas, privileges only when applying, disable refused with active splits, disable passes with unknown count or only disabled splits); round-trip keeps `kafka.image.registry` and `kafka.resources`.
- `recommend_test.go`: off emits `kafka.enabled: false` only; on emits policy, replicas and the info note; Ignore warns; pinned on with attach off warns; upgrade keeps release on with no ChangedKeys; enabling on upgrade yields the three keys; disabling yields `kafka.enabled` plus the leave-behind warning; privilege denial errors on apply and warns with hand-off note otherwise.
- `planned_test.go`, `render_test.go`, `summary_test.go`, `verify_kafka_test.go`, `helm/crds_test.go` (`TestCRDNames`) as named above.
- Regression `Test_KafkaInput` in the install Setup suite: input with `kafka.enabled: true` and agent machinery off, `setup --non-interactive --input --output --apply`; assert `Action: install`, the CRD lines, `Deployment tp-kafka.<ns>`, verification mentions the provider; CRDs exist; rollout ready; output file and `helmGetValues` carry enabled, replicas 2, policy Fail; identical rerun gives `Action: none`; input with `kafka.enabled: false` gives `Action: upgrade` with `kafka.enabled` in the changed keys and the Deployment gone. Needs the `telepresence-kafka` image loaded (`make load-kafka-image`; CI's `make load-images` already includes it). The refusal path stays unit-tested since a real split needs a broker.

## Commits (dependency order; each passes `make lint` and the unit suite)

1. Add the plan at `docs/plans/setup-kafka/plan.md`.
2. Probe the Kafka provider's privileges and prerequisites (`helm.CRDNames`, RBAC refactor and Kafka sweep, `probe_kafka.go`, facts and phase, health finding, render area, tests).
3. Ask about Kafka personal intercepts in the interview (types, questions, `askInt`, `pinKafka`, tests).
4. Propose and validate the Kafka provider values (`kafkaValues`, `checkKafkaPrivileges`, `ValidateValues` rules, tests).
5. List and verify the Kafka provider objects (planned CRD lines, report note, `verify_kafka.go`, tests).
6. Cover the Kafka provider in the setup regression suite; changelog sentence and `make docs-files`.
7. Document installation through setup (setup.md, cobra Long and regenerated CLI page, kafka reference and how-to, manager.md); remove the plan folder.

Implementation runs in sonnet subagents per commit with compact briefs; lint and unit tests after each; the regression test runs on kind-dev with the provider image loaded, then a full `TestSetup` area rerun.

## Verification

- `go test ./pkg/client/cli/setup/... ./pkg/client/cli/helm/... ./pkg/client/cli/cmd/...` and the whole unit suite once.
- `make lint`, `make lint-docs`, `make docs-files` with no leftover diff, `make generate` clean.
- kind-dev: `make build load-kafka-image`, then `go test ./regression_test -run '^TestInstall$/^Setup$'` with `RTEST_CONTEXT=kind-dev RTEST_TEARDOWN=1`; then the Kafka area once more to confirm a setup-installed provider works end to end (`RTEST_MANAGER_KAFKA=1` path unchanged).
- Manual: `telepresence setup --output -` against kind-dev, answer yes, confirm the explain text, defaults, planned CRD lines and the report; `--apply` then `kubectl get crd,deploy/tp-kafka -n <ns>`.
