# Kafka personal intercepts: review fixes

Checklist from the 2026-09-26 review of the `thallgren/kafka-intercepts`
branch. Ordered by engineering dependency, not by user impact: items that
reshape code come first so later fixes land on the final structure. This
file is removed in the last commit once every item is done.

## 1. Lifecycle: drop the handoff scheduling gate

- [x] Reorder disable: pause splitters, wait for application shadow lag
      zero, stop splitters and confirm they left the original group, restore
      normal admission, then replace the shadow-consuming Pods through the
      Eviction API. Nothing consumes the source in the gap, so the gate that
      held replacement Pods is unnecessary.
- [x] Remove `KafkaAdmissionBlocked`, `SplitPhaseQuiescingApplication`,
      `removePods`, `podBlockedForSplit`, `HandoffGate`, the webhook gate
      branch, and the `Blocked` enum value in the CRD.
- [x] Remove the Kubernetes 1.27 / `PodSchedulingReadiness` requirement from
      the chart guard and both docs pages; update the lifecycle text.
- [x] Update unit and regression tests that waited on the gate.

## 2. Traffic-manager integration

- [ ] Use the typed `pkg/kafkaintercept/api/v1alpha1` package instead of raw
      REST and copied phase strings.
- [ ] Automatic discovery treats a missing CRD or a forbidden list as "no
      splits"; only an explicit Kafka request fails.
- [ ] Forget closed routes in the expiry refresh cache.
- [ ] Restore intercepts one at a time on reconnect so one failing Kafka
      route does not drop a client's other intercepts.
- [ ] Document that route environment overrides the application's own
      variables by design: that is how the local consumer receives its
      personal topics, group, and isolation level.

## 3. Chart and RBAC

- [ ] Install the CRDs on upgrade, not only on first install: render them as
      templates gated on `kafka.enabled` with a keep policy, or apply them
      from the Helm code.
- [ ] Scope the Pod-mutating webhook to namespaces that hold a KafkaSplit,
      via a namespace label the controller maintains; keep `failurePolicy:
      Fail` inside that scope only.
- [ ] Drop unused grants: PodDisruptionBudget reads, event writes, the
      finalizer subresources.
- [ ] Exclude the provider's own Pods by namespace rather than by a label.

## 4. Controller and broker

- [ ] Prune member Leases for retired ordinals when the splitter StatefulSet
      shrinks, and count only ordinals below the replica count.
- [ ] Fence the route drain: re-verify the personal group is memberless
      before every drain transaction and abort when a member appears.
- [ ] Pin the route reconciler to one worker with a comment, or make
      preprovisioned slot allocation a compare-and-swap.
- [ ] Check split composition against freshly resolved workloads, not only
      the other split's recorded snapshot.
- [ ] List Pods per selected workload instead of the whole namespace.
- [ ] Splitter: log and continue on retriable fetch errors instead of
      exiting.

## 5. Tests and CI

- [ ] Unit tests for each fix above: discovery without the CRD, reconnect
      with one failing route, environment collision, replica scale-down,
      reconnecting consumer during drain, concurrent slot allocation,
      PDB-blocked eviction leaving the split pending.
- [ ] Add a `kafka.enabled` axis to the clusterless chart matrix.
- [ ] Gate the Kafka conformance job on Kafka paths.
- [ ] Pin the Argo Rollouts version the regression suite installs.
- [ ] Add a make target that runs controller-gen for the CRDs and deepcopy.

## 6. Documentation

- [ ] Replace `DrainPending` with the real condition names.
- [ ] Document the webhook's failure policy and its scope.
- [ ] State that splitter replicas cannot be reduced while enabled, or
      document the recovery once item 4 lands.
- [ ] Give concrete guidance for a PodDisruptionBudget that stalls a
      transition.
