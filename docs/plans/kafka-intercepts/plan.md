# Kafka personal intercepts

## Status

Approved for implementation.

This plan adds Kafka personal intercepts as an independent provider. Kafka
CRDs, admission, lifecycle, broker operations, and routing live in a separate
`telepresence-kafka` binary and image. The traffic-manager contains only the
small attachment adapter needed to make Kafka routes part of an ordinary
Telepresence intercept.

Kafka transactions are mandatory. The splitter temporarily becomes the sole
intended member of the application's original consumer group. It publishes
each source record to exactly one application or developer shadow and commits
the original group offsets in the same transaction.

## User contract

An operator creates a namespaced `KafkaSplit` for one application consumer
group and one or more explicit source topics. The resource selects the
workload fleet and declares the container environment variables used for topic,
group, isolation level, and optionally transactional ID.

An ordinary `telepresence intercept <workload>` discovers every enabled split
whose active workload snapshot contains that workload. It creates a personal
route for every discovered split and starts the local handler only after the
network intercept and all Kafka routes are ready. The handler receives the
normal intercepted container environment with the session topic, group,
`read_committed`, and a route-unique transactional ID overlaid.

`--no-kafka` requests a network-only intercept. `--kafka-only` supports
workloads with no incoming network traffic. Kafka predicates use independent
`--kafka-header`, `--kafka-key`, and `--kafka-key-prefix` flags; HTTP predicates
are never reused implicitly.

Several developers may intercept the same split when their Kafka predicates
are provably non-overlapping. Predicates are AND expressions over exact Kafka
headers and an optional exact or prefix key. Header names are case-sensitive
and match the last occurrence. Values are UTF-8 by default with an explicit
base64 form. Payloads remain opaque. V1 has no priority or fan-out.

When an intercept closes or expires, its route first stops accepting new
records. Any unconsumed session residue is transactionally returned to the
application shadow before managed session resources are deleted.

## APIs

### KafkaSplit

`KafkaSplit.kafka.telepresence.io/v1alpha1` is namespaced and operator-owned.
One resource owns one original consumer group and a fixed, non-empty source
topic set. Multiple splits may select the same workload when their source
ownership and environment bindings do not conflict.

The spec contains:

- `desiredState: Enabled|Disabled`;
- a workload label selector and container name;
- bootstrap servers, TLS, and exactly one optional SASL mechanism using Secret
  references or workload identity;
- the original group, explicit topics, and an offset policy for a group with no
  committed offsets;
- application topic, group, isolation-level, and optional transactional-ID
  environment bindings;
- configurable splitter StatefulSet replicas;
- `Managed` or `Preprovisioned` shadow resources; and
- optional shadow-only application credentials for strict ACL isolation.

Connection profiles support TLS/mTLS, PLAIN, SCRAM-SHA-256/512,
OAUTHBEARER, GSSAPI/Kerberos, and AWS MSK IAM. Credentials never enter
traffic-manager memory or persisted status.

Status records observed and active generations, the resolved workload
kind/name/UID snapshot, lifecycle conditions, source topic IDs and partitions,
broker inventory, splitter membership, acknowledged route generation,
transaction health, and lag.

### KafkaRoute

`KafkaRoute.kafka.telepresence.io/v1alpha1` is a durable child resource created
by the traffic-manager after ordinary intercept authorization. It contains the
split reference, attachment and session identities, expiry, desired state, and
predicate. Status contains session resources, readiness, generation
acknowledgement, drain state, and errors. A finalizer prevents removal until
residue is safely returned or retained.

### Manager and client compatibility

Manager RPCs gain backward-compatible fields for Kafka predicates and route
summaries, guarded by an advertised manager capability. Traffic-agents remain
unaware of Kafka. If any staged Kafka route fails, the newly created network
intercept and all Kafka routes are rolled back together.

## Provider architecture

The pure-Go `telepresence-kafka` image contains `controller` and `splitter`
subcommands. It is an optional Helm component, disabled by default, with its
own ServiceAccount, RBAC, Service, certificates, leader election, metrics, and
webhook configuration. No Kafka library is linked into `tel2`.

The controller uses `controller-runtime` and owns both CRDs, reconciliation,
status, finalizers, broker inventory, and Pod admission. At least two replicas
serve admission while one leader reconciles. Admission is fail-closed only in
configured managed namespaces and excludes system/provider Pods.

The controller never patches a selected workload's template, labels,
annotations, or replica count. It stages an admission mode and replaces old
Pods through the Eviction API. Deployment, StatefulSet, Rollout, HPA, and PDB
controllers retain control of desired replicas and disruption policy.

One splitter StatefulSet exists per enabled split. Each ordinal has stable
consumer-group and transactional identities. Kubernetes Leases serialize
operations affecting the same workload and broker group.

## Transactional data path

The splitter consumes the original topics through the original application
group using `read_committed`. For each bounded batch it:

1. evaluates one acknowledged route generation;
2. preserves partition, key, value, headers, and timestamp while publishing to
   exactly one mapped shadow;
3. sends the original group offsets to the transaction; and
4. commits the transaction.

It aborts on produce failure, rebalance revocation, stale generation,
cancellation, or fencing. An aborted transaction advances no source offset and
is invisible to shadow consumers.

The splitter guarantee is: whenever it advances an original-group offset,
exactly one committed destination copy exists under the acknowledged routing
generation. This is not end-to-end exactly-once application processing.

Managed shadows mirror source partition counts and have effective retention,
maximum-message-size, and durability settings no weaker than the source.
Partition growth expands every affected shadow before the new source partitions
resume. Preprovisioned resources block with `NeedsProvisioning` until expanded.
Source-topic recreation under the same name blocks instead of guessing at
offsets.

## Lifecycle

Enable resolves and snapshots the workload fleet, validates environment
composition, acquires ownership, proves broker capabilities and permissions,
and creates or verifies application shadows. Admission then redirects
replacement Pods to the application shadows. The controller evicts old Pods,
requires every selected replacement to carry the active generation, and
requires the original group to be empty before starting splitters. Enabled is
reported only after all source partitions have transactional owners.

Route attachment allocates session resources, rejects overlap, and publishes a
new routing generation. Every current partition owner adopts generations only
between transactions. A route becomes ready after all owners acknowledge it.

Route closure publishes a generation that stops new routing, waits for all
owners and the local group to become memberless, and transactionally republishes
unconsumed session records to the application shadows while committing session
offsets. Managed resources are deleted only after committed offsets equal end
offsets. Preprovisioned slots return to their pool.

Disable rejects new routes, closes existing routes, pauses splitters between
transactions, and lets the application drain its shadows. Admission then
blocks replacements while the controller evicts current Pods and waits for the
shadow group to become memberless. Splitters leave the original group,
admission returns to normal, and normally configured Pods are allowed to
reappear. Managed application resources are deleted after causal emptiness
checks; preprovisioned resources remain.

A safety-relevant spec or selector change disables the active snapshot and
then enables the new snapshot. PDBs, unavailable applications, ambiguous broker
results, or unproved emptiness remain pending with actionable status. Resource
inventory stays persisted until deletion has an explicit success or
already-absent result.

## Delivery phases

### Phase 0: transactional feasibility (complete)

Prove consume-transform-produce transactions against the original application
group using `franz-go`. Test commit and abort visibility, crashes on both sides
of commit, static membership, transactional fencing, rebalances, and multiple
splitter replicas against Kafka 3.8 and current Kafka 4.x. Record the results
before implementing the Kubernetes lifecycle.

The central transaction and open-transaction recovery proofs passed on both
broker lines and are recorded in [phase0-findings.md](phase0-findings.md).
Static-membership fencing was already proven by the earlier Kafka feasibility
work; rebalance and multi-replica coverage remain part of the maintained
phase-2 conformance suite.

### Phase 1: provider foundation

Add CRDs, validation, Helm integration, controller/webhook, image targets,
connection parsing, status, inventory, and metrics. Unit-test selectors,
environment composition, filter overlap, names, Secret isolation, and retry
classification.

### Phase 2: transactional engine

Implement shadow management, the splitter, route-generation barriers,
transactional residue return, partition expansion, cleanup, and recovery. Add
real-broker conformance for normal routing, disjoint developers, transactional
applications, rebalances, crashes, controller restarts, disconnects, and
cleanup retries.

### Phase 3: workload and Telepresence integration

Implement admission modes, eviction, multi-split composition, ordinary
intercept discovery, all-or-nothing attachment, queue-only mode, environment
overlays, expiry, and reconnect recovery. Add Kubernetes regression coverage
for supported workload adapters, HPA, PDB, network-plus-Kafka interception,
queue-only workloads, and several splits on one workload.

### Phase 4: completion

Add a how-to, exhaustive reference, Helm values, permissions, limitations,
metrics, troubleshooting, and operator recovery procedures. Run unit, race,
broker conformance, regression, generated-file, image, and full lint checks.
Remove this plan in the final implementation commit once permanent code and
documentation fully represent it.

## Boundaries

- Transactions and `read_committed` are mandatory.
- Sources and shadows are in one Kafka cluster.
- V1 supports explicit topic sets, not regex subscriptions.
- Applications must accept declared environment-variable overrides; argument
  and generated-properties-file rewriting are out of scope.
- Ordinary and transactional consumer/producer applications are supported.
  Kafka Streams internal topics, state stores, and topology migration are not
  managed.
- Payload and Schema Registry filtering, fan-out, route priority, manual
  partition assignment, and custom offset stores are out of scope.
- Unexpected original-group members degrade the split. Optional shadow-only
  credentials provide the broker-enforced mode.
- Future providers use separate CRDs, binaries, images, and semantics rather
  than extending a shared MQ engine.
