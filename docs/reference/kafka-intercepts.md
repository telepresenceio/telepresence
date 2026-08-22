---
title: Kafka personal intercepts
---

# Kafka personal intercepts

Kafka personal intercepts are implemented by the optional Kafka provider. The
provider owns Kafka-specific Kubernetes
resources, admission, broker operations, and the transactional splitter. The
traffic-manager only discovers enabled splits and attaches their routes to
ordinary Telepresence intercepts.

## Delivery guarantee

Kafka transactions are mandatory. A splitter consumes source topics through
the application's original consumer group with `read_committed`. For each
batch, it publishes every record to exactly one application or personal shadow
and commits the source-group offsets in the same Kafka transaction.

When an original-group offset advances, exactly one committed destination copy
exists for the acknowledged routing generation. An aborted transaction
advances no source offset and exposes no shadow record to `read_committed`
consumers. This guarantee does not make the application itself exactly once;
normal consumer crash and commit behavior can still produce application-level
duplicates.

Records retain their source partition number, key, value, headers, and
timestamp. Headers and payloads are not decoded by the provider.

## Installation and components

Set `kafka.enabled=true` in the Telepresence Helm chart. The chart installs:

- the `KafkaSplit` and `KafkaRoute` CRDs;
- a two-replica `tp-kafka` controller Deployment by default;
- validating webhooks for splits and routes and a Pod mutation webhook;
- a ServiceAccount, RBAC, certificates, and leader-election Leases; and
- a Service exposing the webhook on port 443 and metrics on port 8080.

The provider uses the separate `telepresence-kafka` image and binary. No Kafka
library is linked into the `tel2` image used by the traffic-manager and
traffic-agents.

The provider requires Kubernetes 1.27 or newer with the
`PodSchedulingReadiness` feature enabled. The feature is enabled by default
starting in Kubernetes 1.27 and stable starting in Kubernetes 1.30.

All fixed names installed by the provider are 30 characters or shorter.
Generated names can exceed 30 only when they retain a user or cluster
identifier that is useful to operators:

- `tp-kafka.<manager-namespace>.svc` for webhook DNS;
- `tp-kafka-<manager-namespace>` for cluster-scoped chart resources;
- `<workload-namespace>-<split-name>` for per-split provider resources, with
  `-<ordinal>` added to splitter member Leases;
- `<client-name>-<intercept-name>-<split-name>` for KafkaRoute resources; and
- the topic, group, and transaction forms documented under
  [Shadow resources](#shadow-resources).

Those names are validated rather than truncated. The only generated hash is
the short name of an internal source-group ownership Lease.

Relevant Helm values are:

```yaml
kafka:
  enabled: false
  replicas: 2
  image:
    registry: ""       # defaults to image.registry
    name: telepresence-kafka
    pullPolicy: ""     # defaults to image.pullPolicy
  webhook:
    port: 9443
    failurePolicy: Fail
    timeoutSeconds: 5
  resources:
    requests:
      cpu: 25m
      memory: 64Mi
    limits:
      memory: 256Mi
```

## KafkaSplit

`KafkaSplit.kafka.telepresence.io/v1alpha1` is namespaced and
operator-managed. One split owns one original consumer group and a fixed,
non-empty source topic set.

### Workload selection

`spec.workloadSelector` is evaluated in the KafkaSplit namespace. It can
resolve to one or more Deployments, StatefulSets, standalone ReplicaSets, and
Argo Rollouts. The resolved kind, name, and UID set is stored in status as the
active snapshot. A selector can therefore represent a fleet of equivalent
consumers.

The provider never patches workload templates, labels, annotations, replica
counts, HPAs, or PDBs. It changes modes in its own CR status and replaces Pods
using the Eviction API. Kubernetes workload, HPA, and disruption controllers
remain authoritative. PDBs can keep a transition pending until disruption is
allowed.

Several splits can select the same workload and container when their source
groups, source topics, and environment bindings do not conflict.

### Application bindings

`spec.application` maps Kafka settings to variables in the selected container:

- specify either `topicEnv` plus `topicSeparator`, or one `topicBindings` entry
  for every source topic;
- `groupEnv` identifies the consumer group variable;
- `isolationLevelEnv` identifies the consumer isolation variable;
- optional `transactionalIDEnv` identifies a producer transactional ID; and
- `shadowCredentials` replaces additional environment variables from Secret
  keys while the application consumes shadows.

The topic, group, isolation, and optional transactional-ID variables must be
explicit literal entries in `container.env`. Topic and group values must match
the declared source. Values supplied only by `envFrom`, field references, or
other indirect sources cannot be safely inspected and are rejected. Shadow
credentials use `secretKeyRef`.

While enabled, admitted replacement Pods receive application shadow topics, a
shadow group, `read_committed`, and a Pod-unique transactional ID when that
binding is present. Normal Pod configuration is restored by admission after a
disable completes; the workload template remains unchanged throughout.

The application transactional ID is `<source-group>.tp-app.<pod-uid>`. It is
deliberately unique to each replacement Pod;
producer fencing therefore comes from Kafka's group-aware transaction protocol
(KIP-447) on the supported Kafka versions, not from reusing one stable
transactional ID across Pod incarnations.

`shadowCredentials` can give application Pods credentials restricted to the
application shadows while the split is enabled. When Kafka ACLs deny those
credentials access to the source topics and original group, this is the
broker-enforced isolation mode; without it, exclusive source ownership is
continuously monitored and enforced by lifecycle gates.

### Connection and authentication

`spec.connection.bootstrapServers` contains `host:port` seed addresses.
Optional TLS supports a CA, client certificate, private key, and server-name
override. TLS material is read from Secret keys.

At most one SASL mechanism is configured:

- `PLAIN`;
- `SCRAM-SHA-256` or `SCRAM-SHA-512`;
- `OAUTHBEARER` client credentials;
- `GSSAPI` with a password or keytab and krb5 configuration; or
- `AWS_MSK_IAM`, using the provider Pod's AWS credential chain and a declared
  region.

Passwords, OAuth client secrets, keytabs, TLS private keys, and shadow
credentials must use Secret references. Credentials are resolved by the Kafka
provider and are never sent through the traffic-manager or stored in CR
status.

### Source offsets

`spec.source.group` is the original application group and
`spec.source.topics` is an explicit topic list. Regex subscriptions and manual
partition assignment are unsupported.

`offsetReset` is required and is either `earliest` or `latest`. It is used only
when the original group has no committed offset for a source partition. The
provider initializes exact offsets before source ownership is transferred.

### Splitter capacity

`spec.splitter.replicas` controls the splitter StatefulSet size and defaults to
one. Stable ordinal-based group-instance and transactional IDs fence stale
processes. `batchSize` bounds each Kafka transaction and defaults to 100.

The split becomes `Enabled` only when all expected splitter members are healthy
and have acknowledged the current route generation. Route changes are adopted
only between transactions.

### Shadow resources

`Managed` mode creates application and personal topics and groups with
deterministic names. Each shadow topic mirrors the source partition count and
uses effective unbounded retention, `cleanup.policy=delete`, a replication
factor no weaker than the source, and source-compatible message-size,
minimum-ISR, and timestamp settings. Managed resources are deleted only after
broker-confirmed drain and explicit successful or already-absent responses.

For source topic `T`, source group `G`, route `R`, and splitter ordinal `n`,
managed broker names are visible and scope-based:

- application topic `T.tp.G.app` and group `G.tp-app`;
- personal topic `T.tp.G.R` and group `G.tp.R`;
- splitter instance `tp-splitter-n` and transaction `G.tp-splitter-n`;
- application transaction `G.tp-app.<pod-uid>`;
- personal client transaction `G.tp-client.R`; and
- route-drain transaction `G.tp-drain.R`.

Names are never truncated or replaced with hashes. The provider rejects a
generated name that exceeds a Kafka limit and identifies which source or route
name must be shortened. Managed mode also requires source groups to contain
only characters Kafka permits in topic names because the group is necessary
to distinguish independent subscriptions to the same source topic.

`Preprovisioned` mode uses operator-owned application topics, an application
group, and a pool of named personal session slots. Every slot maps each source
topic to an existing shadow topic and declares its consumer group. A route
remains `NeedsProvisioning` when no free slot is available. Preprovisioned
resources are verified but never deleted.

Source partition growth expands managed shadows before the new partitions
resume. Preprovisioned shadows must be expanded by the operator. Partition
count reduction and source-topic recreation under the same name block the
split.

## KafkaRoute

`KafkaRoute.kafka.telepresence.io/v1alpha1` is a durable child resource made
by the traffic-manager after normal intercept authorization. Users generally
manage routes through `telepresence intercept` and `telepresence leave`, not
with `kubectl`.

A route records its split, attachment and client session identities, expiry,
desired state, and predicate. The controller reports its personal topics,
group, local environment overlay, acknowledged generation, drain lag, owned
resources, conditions, and phase. Its finalizer remains until personal residue
has been returned or cleanup has completed.

The route resource name is the DNS-safe form of
`<client-name>-<intercept-name>-<split-name>`. A normalization collision or a
name over the Kubernetes limit is rejected instead of being hidden behind a
hash or truncation.

The manager refreshes expiry for live attachments and reconstructs route
finalizers after restart. If a client disappears, expiry begins the same close
and drain workflow as an explicit detach.

## Filtering and concurrent developers

A predicate is an AND expression containing zero or more exact headers and at
most one exact key or key prefix. Header names are case-sensitive and the last
occurrence of a repeated header is matched. CLI values are UTF-8 unless
prefixed with `base64:`. Prefix a literal value with `text:` when it begins
with `base64:` or `text:`. Payloads remain opaque.

The controller rejects any pair of routes for which one record could satisfy
both predicates. For example, `tenant=blue` and `tenant=green` are disjoint;
an unfiltered route overlaps every route. V1 has no fan-out, priority, payload
filter, Schema Registry integration, or arbitrary predicate language.

An ordinary `telepresence intercept` attaches all enabled splits in the
workload's active snapshot. `--no-kafka` opts out. `--kafka-only` creates Kafka
routes without network interception, traffic-agent installation, mounts, or
port forwarding. Route creation is all-or-nothing: if any matching route fails,
the new Kafka routes and network intercept are rolled back.

Human-readable and structured intercept output lists attached split names in
`Kafka splits`/`kafka_splits`. A client talking to an older manager strips only
the empty automatic-discovery request and continues with a network intercept;
explicit Kafka filters and `--kafka-only` fail with an unsupported-manager
error.

## Lifecycle and status

Setting `desiredState: Enabled` performs broker and workload preflight, creates
or verifies application shadows, redirects replacement Pods, proves the
original group memberless, and starts the splitter. The active spec and
workload snapshot remain in status so a later unsafe spec or selector change
first disables the old snapshot before enabling the replacement.

Setting `desiredState: Disabled` rejects new routes, closes existing routes,
pauses splitters between transactions, waits for the application shadow lag to
reach zero, quiesces shadow consumers, stops splitters, restores normal Pod
admission, and removes managed resources. During source handoff, replacement
Pods are admitted with a scheduling gate so they cannot start consuming; the
controller evicts those gated Pods after normal admission resumes. This
transition intentionally causes consumer downtime. It does not change the
workload's desired replicas.

Important status fields include:

- `phase` and `conditions`, including a machine-readable reason and message;
- `activeGeneration`, `activeSpec`, and `workloads` for the active snapshot;
- `sourceTopics` with topic IDs and partition counts;
- `resources` for durable broker inventory;
- `routeGeneration` and `members`, whose `healthy`, `generation`, and
  `lastSeen` fields report transactional splitter health; and
- `applicationLag` during shutdown, or route `lag` during personal residue
  return, containing the last broker-confirmed count of committed records that
  remain to be consumed.

The controller exposes standard controller-runtime process, work-queue,
leader-election, webhook, and reconciliation metrics at
`tp-kafka:8080/metrics`. Kafka safety and lifecycle state is exposed
through CR status so that it remains durable across controller restarts.

## Broker permissions

The provider identity needs Kafka authorization for:

- metadata and configuration reads for source and shadow topics;
- reading source topics through the original group;
- transactional writes to application and personal shadow topics;
- describing and committing the original, application, personal, and drain
  groups;
- using every generated splitter, application, personal-client, and drain
  transactional ID; and
- in `Managed` mode, creating topics, increasing partitions, and deleting
  owned topics and groups.

The exact ACL syntax depends on the Kafka distribution and authorizer. Managed
resource names are recorded in `.status.resources`; use them when deriving a
narrow resource-prefix policy. In `Preprovisioned` mode, creation, partition
expansion, and deletion permissions are not required, but all declared
resources still require metadata, read, write, group, and transaction access
appropriate to their role.

The provider's Kubernetes ServiceAccount can read referenced Secrets in
managed namespaces, evict selected Pods, read supported workload controllers
and PDBs, and manage its own ConfigMaps, headless Services, StatefulSets, and
Leases. The Helm chart installs these permissions only when the provider is
enabled.

## Recovery and troubleshooting

Reconciliation is retryable and driven from CR status and broker inventory.
Do not remove finalizers to resolve a normal pending transition; doing so can
discard the inventory needed for safe cleanup.

Start with:

```console
$ kubectl get ksplit,kroute -n shop
$ kubectl describe ksplit orders -n shop
$ kubectl logs -n ambassador deployment/tp-kafka
```

Common conditions are:

- `SourceGroupNotEmpty`: another member still consumes the original group.
  Stop it or correct the workload selector before retrying.
- `ReplacingPods` or `QuiescingApplication`: inspect workload availability and
  PDBs. The provider waits rather than bypassing disruption policy.
- `BrokerPreflightFailed`: correct permissions, transaction support, topic
  configuration, source incarnation, or shadow durability.
- `NeedsProvisioning`: add or expand a preprovisioned personal slot.
- `SessionGroupNotEmpty`: stop the local consumer so route residue can return.
- `DrainPending` or `DrainingApplication`: consumers are quiesced correctly but
  committed records remain. The status lag records the latest count.
- `Degraded`: a splitter is unhealthy or an unexpected original-group member
  appeared. No unsafe lifecycle step proceeds until ownership is proved again.

After correcting the external condition, leave the resource in place; normal
reconciliation resumes. For a safety-relevant spec change, apply the new valid
manifest at any time. The controller first finishes disabling the persisted
active generation and then enables the new generation.

Before uninstalling the provider, set every KafkaSplit to `Disabled` and wait
for every split to report `Disabled` and every route to disappear. Helm cannot
safely finish an enabled split after its controller has been removed. If the
provider was removed prematurely, reinstall it with the same provider
namespace and image, then let reconciliation complete before uninstalling it
again. Removing finalizers manually is a last-resort operation that requires
an independent broker handback and residue audit.

A force-deleted KafkaSplit can leave provider coordination state behind.
Reinstall the provider and restore the resource before attempting cleanup;
deleting internal coordination objects does not drain records or repair broker
resources.

If managed deletion receives an ambiguous broker result, inventory remains in
status and deletion is retried. An operator should manually delete an
inventory entry only after independently proving it belongs to the split and
contains no required records. Preprovisioned resources are always reaped by
the operator according to the organization's own policy.

## Limitations

- Kubernetes 1.27 or newer with Pod scheduling readiness is required.
- Kafka transactions and `read_committed` are mandatory.
- Sources and shadows must be in one Kafka cluster.
- Source topics are explicit; regex subscription and manual assignment are
  unsupported.
- Applications using custom offset stores instead of Kafka consumer-group
  offsets are unsupported.
- Applications must accept environment-variable overrides. Command-line or
  generated-properties-file rewriting is unsupported.
- Kafka Streams internal topics, state stores, and topology migration are not
  managed.
- Payload and Schema Registry filtering, fan-out, and route priority are
  unsupported.
- An unexpected member in the original group degrades the split.
- Enabling and disabling replace Pods and introduce consumer downtime.
- Future message providers use separate APIs, binaries, images, and semantics;
  KafkaSplit is not a shared message-queue abstraction.
