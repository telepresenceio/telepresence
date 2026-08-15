# Queue splitting: personal intercepts for message consumers

## Status

Proposed. Implementation must not begin until the Kafka and RabbitMQ cutover
prototypes in **Delivery plan** pass. Those prototypes are explicit go/no-go
gates because safe ownership transfer, not the provider interface or CLI, is the
principal technical risk.

## Problem

Telepresence can intercept traffic that arrives at a workload, but a message
consumer initiates its connection to the broker. There is no inbound socket for
the traffic-agent to redirect. Replacing the workload can move all consumption
to one developer, but it cannot select messages by header or support several
developers independently.

Queue splitting inserts a manager-owned `queue-agent` into a declared
consumption stream. While a split is active, the queue-agent consumes the real
stream and copies each message to one durable shadow destination:

- the application shadow, for messages that match no developer; or
- one session shadow per developer filter.

The application and each local consumer are pointed at their shadows through
environment overrides. Kafka and RabbitMQ are both v1 backends. The control
plane remains provider-neutral, but provider-specific configuration and handoff
code remain explicit inside the queue-agent.

This feature prioritizes **no silent loss** over availability. If ownership
cannot be handed back safely, the split stays active and reports a degraded or
draining state; it does not guess at offsets or delete a non-empty shadow.

## Goals and non-goals

### Goals

- Filter Kafka records and RabbitMQ messages by headers using equality and AND
  semantics.
- Allow several non-overlapping filtered attachments to the same logical queue.
- Preserve the application's existing at-least-once behavior. Duplicates are
  possible at publish/ack or publish/commit crash boundaries.
- Survive queue-agent and traffic-manager restarts without losing the desired
  route table or deleting durable shadow resources.
- Make attach, detach, recovery, and cleanup observable in CLI status and
  manager/agent metrics.
- Keep manager orchestration independent of Kafka and RabbitMQ mechanics.

### Non-goals for v1

- Exactly-once delivery.
- Preserving ordering across different destinations. Ordering is preserved only
  among messages routed to the same destination, subject to normal broker
  guarantees.
- RabbitMQ exchange/binding interception. V1 consumes a named queue.
- Kafka consumer groups shared with consumers outside the selected workload.
- RabbitMQ queues consumed by any workload other than the selected workload.
- Workloads controlled by an HPA, Argo Rollout, or bare ReplicaSet. V1 supports
  Deployments and StatefulSets only.
- Zero-downtime cutover. Activation and final deactivation briefly scale the
  workload to zero to transfer ownership without racing consumers.
- SQS implementation. SQS remains a likely provider, but FIFO behavior,
  visibility extension, IAM, message-attribute limits, and queue lifecycle need
  a separate design; v1 does not claim that SQS is a mechanical port.

## User model

A workload declares logical queues in the `telepresence.io/queue-config`
annotation. A developer names them without needing to know the provider:

```console
telepresence intercept checkout \
  --queue orders \
  --queue-header x-telepresence-user=thomas
```

`--queue` is repeatable. Repeated `--queue-header K=V` flags form one AND
predicate applied to every named queue. A queue attachment can accompany a port
intercept or use `--no-default-port`.

An attachment without `--queue-header` requests all messages and is exclusive.
Filtered attachments may coexist only when their predicates cannot both match
the same message. Two conjunctions overlap when every key they share has the
same value; the manager rejects such a pair instead of using attachment order
as an implicit priority.

The active split set is workload-scoped. The first attachment to a workload and
the final detach from it are disruptive. Adding the first attachment for a
logical queue that is not yet in that workload's active split set is also
disruptive, because its source ownership and app env must change. Once a logical
queue is active, adding or removing filtered routes on it does not restart the
application. A queue with no remaining developer route stays app-shadowed until
the workload's final detach, which deactivates the whole active set together.
The CLI identifies every operation that scales the workload and supports
`--yes` for automation.

## Delivery semantics

The common provider contract is exercised by the shared conformance suite:

1. Every message accepted from the source is durably published to exactly one
   shadow before its source delivery is acknowledged or splitter offset is
   committed.
2. A crash between shadow publish and source acknowledgement/commit may publish
   the message twice. The system never promises exactly-once processing.
3. A route stops accepting new messages before its shadow is drained or moved.
4. A shadow is deleted only after its consumer is gone and the provider has
   proved that no acknowledged backlog remains.
5. The application returns to the source only after session residue has moved to
   the app shadow, that shadow has drained, and the provider-specific handoff has
   completed.
6. A timeout changes status to `DEGRADED` or `DRAINING`; it never turns an
   unproved state into success.

The application can still lose a message if its consumer acknowledges or
commits before processing it. Queue splitting cannot strengthen the
application's acknowledgement discipline.

### Filter semantics

- Filter keys are case-sensitive and all entries must match.
- Values are byte-for-byte UTF-8 strings. Non-string AMQP header values and
  non-UTF-8 Kafka header values do not match.
- Kafka permits duplicate header keys; the last header with a key is used.
- Message bodies, keys, headers/properties, timestamps, and Kafka partition
  selection are preserved where the destination API permits. Provider tests
  enumerate fields that cannot be preserved, such as AMQP's broker-generated
  redelivery flag.

## Architecture

```text
                    manager-owned desired state
                      (namespaced ConfigMap)
                                |
                                v
source stream ---> queue-agent Deployment
                       |                |
              no filter match       filter match
                       |                |
                       v                v
                 app shadow       session shadow
                       |                |
                       v                v
                cluster workload   laptop consumer
```

There is one queue-agent Deployment per workload with an active or recoverable
split. One process hosts an engine per declared logical queue, so a workload may
use Kafka and RabbitMQ simultaneously. The Deployment has one replica and a
`Recreate` strategy. The process must also acquire an activation-scoped fence
before consuming, so a delayed old Pod and its replacement cannot both own the
RabbitMQ source during a Kubernetes partition.

The Deployment and state ConfigMap live in the workload namespace. They are
labelled with the manager install ID and workload UID, but deliberately have no
workload owner reference: deleting a workload must not garbage-collect an agent
that may hold the only copy of undrained messages. Workload-deletion and
uninstall reconcilers perform orderly handoff before cleanup.

Before source consumption begins, the manager adds a queue-split finalizer to
the workload. Deletion quiesces remaining consumers and uses a replay-safe
provider path: Kafka leaves the original source-group offsets at the activation
checkpoint unless it can prove a later contiguous handoff, while RabbitMQ moves
all ready and requeued shadow messages back to the source. This may replay Kafka
records but does not skip them. The finalizer is removed only after broker data
is safe and state is retained for any cleanup retry. If the broker is
unavailable, deletion remains blocked and status identifies the finalizer; the
documented force procedure preserves the ConfigMap and broker resource list for
manual recovery before removing it.

The queue-agent keeps a gRPC session to the manager for prompt desired-state and
status updates. The same desired state is written first to a manager-owned
ConfigMap and mounted into the queue-agent. The ConfigMap is the restart source
of truth; gRPC is the low-latency transport, not the only copy. Updates use a
monotonically increasing generation, acknowledged only after reconciliation.
Desired state contains Secret references, never Secret values.

Shadow resources and broker groups use bounded deterministic names derived from
manager install ID, workload UID, logical queue, activation epoch, and route ID.
They are not derived directly from a topic or queue name. The ConfigMap records
every created broker resource so cleanup cannot delete a similarly named user
resource.

## Queue declaration

The annotation is parsed with other Telepresence annotations, but its
provider-specific validation belongs in a new `pkg/queueconfig` package rather
than in manager orchestration.

```yaml
telepresence.io/queue-config: |
  queues:
    - name: orders
      container: app
      kafka:
        brokersEnv: KAFKA_BROKERS
        sourceEnv: ORDERS_TOPIC
        groupEnv: KAFKA_GROUP
        offsetReset: earliest
        tls:
          caSecret: kafka-ca
          caKey: ca.crt
        sasl:
          mechanism: SCRAM-SHA-512
          usernameEnv: KAFKA_USERNAME
          passwordEnv: KAFKA_PASSWORD
    - name: invoices
      container: app
      rabbitmq:
        urlEnv: AMQP_URL
        managementUrlEnv: RABBITMQ_MANAGEMENT_URL
        sourceEnv: INVOICE_QUEUE
        tls:
          caSecret: rabbitmq-ca
          caKey: ca.crt
```

Rules:

- `name` is unique within the workload and is the only queue identifier exposed
  by the CLI.
- `container` is required unless the workload has exactly one app container.
- `sourceEnv` must name a literal `EnvVar` in that container. The manager reads
  the original source from it and the webhook shadows it in admitted Pods.
- Kafka `groupEnv` is required and must also be a literal `EnvVar`. Safe startup
  and handoff require the original group's committed offsets.
- `offsetReset` is required for Kafka and defines the start for a partition with
  no committed application offset.
- Connection and credential env vars must be explicit entries in `env`; v1
  rejects referenced names supplied by `envFrom`. Literal values,
  `secretKeyRef`, and `configMapKeyRef` are copied to the queue-agent Pod;
  `fieldRef` and `resourceFieldRef` are rejected because they would resolve
  against the queue-agent rather than the app Pod. The manager never reads a
  Secret value.
- TLS Secrets must be in the workload namespace and already be referenced by
  the selected app container's env or mounted volumes. The queue-agent mounts
  only the named key. Supported Kafka modes are PLAINTEXT, TLS, SASL/PLAIN, and
  SASL/SCRAM. Supported RabbitMQ modes are `amqp` and `amqps` with URL or
  username/password authentication. The exact mutually exclusive forms live in
  the Go schema and generated reference documentation.
- RabbitMQ `managementUrlEnv` is required. V1 uses the HTTP management API,
  authenticated by explicit env references from the same container, to inspect
  queue type, declaration arguments, ready/unacknowledged counts, and consumers.
  An installation without that API cannot enable the RabbitMQ backend.
- RabbitMQ v1 supports durable classic and quorum source queues. Quorum queues
  are RabbitMQ's recommended default and classic queues are on the deprecation
  path, so excluding quorum would scope out the common case; their support is
  proved in the phase-0 prototype rather than assumed. Declaration arguments
  other than `x-queue-type` (TTL, priority, dead-letter, and similar), stream
  queues, and exclusive or auto-delete queues remain unsupported.
- Within one container, every `sourceEnv` and Kafka `groupEnv` used by a logical
  queue must be unique across the declaration, including across roles. Broker
  connection and credential env vars may be shared. This prevents two active
  queues from requiring different values for one app env name.

Validation happens twice: static annotation validation produces a workload
configuration error; broker preflight during attachment returns an actionable
intercept error. A malformed declaration never starts a partial split.

## Workload environment and cutover

The existing traffic-agent remains responsible for capturing the app
container's effective environment and mounts, including for queue-only
intercepts. Queue routing is independent, but v1 calls `EnsureAgent` so the
normal intercept environment path remains valid. The manager merges the
queue-agent's source and Kafka group overrides into a copy of the environment
for that intercept. It must not mutate the shared
`AgentInfo.ContainerInfo.environment` map.

That effective environment is used only for the laptop intercept. Queue-agent
Pod construction comes directly from the validated workload Pod template: it
copies only declared connection/credential `EnvVar` sources and supported Secret
volume/key references. It never attempts to reuse resolved values or mounts
reported by a running traffic-agent, and unsupported `fieldRef`, `resourceFieldRef`,
`envFrom`, projected-volume, or arbitrary app-volume cases fail validation.

The application override is admission-time state, not a rewrite of the
workload's declared env list. The manager's queue-state informer supplies the
active override generation to the webhook. The webhook appends overrides to the
selected container and annotates the Pod with that generation. The existing
`addTPEnv` helper can be generalized, but queue state must not live only in the
injector's memory.

V1 therefore requires the agent-injector webhook even when the traffic-agent was
otherwise installed manually. Attachment fails before broker preparation if
admission mutation is disabled or the workload namespace is outside the
webhook's scope.

### Preconditions

Before changing broker ownership, the manager verifies that:

- the workload is a settled Deployment or StatefulSet;
- it is not targeted by an HPA;
- every env reference resolves under the rules above;
- no active split holds the same broker ownership key;
- after quiescence, the original Kafka app group is empty, or RabbitMQ reports
  no remaining source-queue consumer; and
- every requested queue can be prepared. A multi-queue attachment rolls back if
  any queue fails before cutover.

The queue-agent derives a non-secret ownership hash from normalized broker
endpoint, vhost/topic/queue, and Kafka group. The manager holds a Kubernetes
Lease for that hash for the split lifetime. The Lease serializes manager
reconciliation and detects conflicting declarations; it is not a broker fence.
Provider-held fencing described below prevents stale agent processes from
publishing. Kafka group membership cannot prove Kubernetes workload identity or
prevent another principal from joining later. Exclusive ownership of the
original Kafka app group is therefore a documented operator precondition; the
agent monitors and reports unexpected members but cannot make that topology
safe. RabbitMQ enforces source ownership with an exclusive consumer.

### Activation state machine

The state and pre-activation replica count are persisted before each step. Every
step is idempotent and restartable.

One workload-level coordinator records the active split set and phase of every
requested queue. For initial activation, the workload remains at zero replicas
while all requested engines start. When expanding an existing active set, its
engines continue pumping to their app shadows while the app is quiesced, and
only newly requested engines transfer source ownership. The workload is restored
with overrides for the complete active set only after every new engine reports
`STARTED`. A failure sends every newly started engine through
`ABORTING_ACTIVATION`; existing engines remain active. There is no cross-broker
transaction, so status can show mixed per-queue recovery progress, but the app
is never deliberately started with only a subset of the persisted active-set
overrides.

1. `PREPARING`: create durable shadows and provider checkpoints for queues being
   added to the active set without consuming their sources.
2. `QUIESCING_APP`: record replicas, scale the workload to zero, wait for Pods
   and broker consumers to disappear, and let final acknowledgements/commits
   settle.
3. `STARTING_PUMP`: start each newly added engine at the final application
   position. Kafka uses the original group's committed offsets; RabbitMQ
   consumes the source. Engines already in the active set continue unchanged.
4. `REDIRECTING_APP`: persist the active override generation, restore replicas,
   and wait until every ready Pod carries that generation and consumes only the
   app shadow.
5. `ACTIVE`: accept filtered routes. If workload restore fails, stop the pump and
   enter `ABORTING_ACTIVATION` while the workload remains scaled down. Kafka
   leaves the original group at its pre-activation offsets; RabbitMQ republishes
   every unconsumed shadow message to the source. Only after every provider has
   proved that replay is safe does the manager remove overrides and restore the
   source workload. A partially started app may cause duplicates during this
   recovery, but not skipped messages. If proof is unavailable, remain
   `DEGRADED` at zero replicas instead of attempting normal handback.

Scaling to zero is intentional downtime. It avoids a window in which the app
and queue-agent independently consume the source. HPA support and zero-downtime
fencing are follow-up work, not implicit promises.

Scale changes use optimistic resource-version preconditions. A conflicting
manual scale during a cutover aborts into the proved handback path rather than
overwriting the user's value. Manual scaling is permitted in `ACTIVE` because
the webhook redirects every new Pod; final deactivation snapshots the then
current replica count instead of restoring the activation-time count.

### Session attach and detach

Attach creates a durable session shadow before publishing its route table. The
agent acknowledges the route generation before the intercept becomes `ACTIVE`.
Kafka overrides both topic and group env vars; RabbitMQ overrides the queue env.

Detach first marks the route `DRAINING`, so it receives no new messages. The
agent installs the new route generation, stops classifying messages to the
removed route, and waits for every publish selected under an earlier generation
to receive its provider acknowledgement. Only then does it capture Kafka end
offsets or inspect RabbitMQ backlog. That publish barrier is part of the
generation acknowledgement. The local consumer must then close:

- Kafka waits for the session group to become empty, reads its committed
  shadow-topic position, and starts a deterministic drain group at that
  position. The drain group publishes the unconsumed suffix to the app shadow
  and commits each shadow offset only after the publish succeeds. Its committed
  offsets are the durable crash-recovery checkpoint. The session topic/group and
  drain group are deleted only after the drain group reaches the per-partition
  end offsets captured after routing to that session stopped. No mapping back to
  source offsets is required: the immutable shadow-topic offsets define the
  suffix and its deletion proof.
- RabbitMQ waits for consumer count zero so unacked deliveries are requeued,
  republishes all ready messages to the app shadow with confirms, then deletes
  the empty queue.

The CLI waits for a bounded interval. On timeout it says that the local consumer
must stop and leaves the route `DRAINING`; a reconciler keeps retrying. The
shadow is not discarded. A duplicate is possible if a consumer processed but
did not acknowledge its final message.

### Final deactivation

After the last developer route on the workload drains, deactivate every logical
queue in its active split set together:

1. stop source consumption at a recorded frontier;
2. let the application consume the app shadow to broker-reported zero lag;
3. scale the workload to zero, wait for consumers to disappear, and recheck for
   deliveries requeued during shutdown; repeat the drain if needed. For Kafka,
   reread every app-shadow group committed offset and partition end offset only
   after the group is empty, and repeat until they are equal;
4. for Kafka, set the original app group's source offsets to the splitter
   frontier while the group has no members; RabbitMQ needs no offset operation;
5. persist an inactive override generation, restore replicas, and verify that
   ready Pods no longer carry a queue override; and
6. delete empty shadows and provider groups, release the Lease, and reap the
   idle queue-agent after its TTL.

New source messages wait at the broker between steps 1 and 5. No source and
shadow consumer run concurrently during handback.

## Provider engine contract

One engine owns one logical queue. The interface uses provider-neutral outcomes
and opaque checkpoints:

```go
type Engine interface {
    Prepare(context.Context, QueueConfig, ActivationID) (EnvOverrides, error)
    Start(context.Context) error
    ReconcileRoutes(context.Context, []Route) error
    DrainRoute(context.Context, RouteID) error
    Stop(context.Context) (Handoff, error)
    DrainApplication(context.Context, Handoff) error
    CommitHandoff(context.Context, Handoff) error
    Cleanup(context.Context) error
    Recover(context.Context, DesiredState) error
    Status(context.Context) Status
}
```

`Handoff` is opaque to the manager. Methods are idempotent for one activation
and desired generation. `Recover` reconciles deterministic resources and
broker-native checkpoints after restart. The manager owns workload scaling and
state transitions; engines prove broker state is safe for the next transition.

The interface is provisional until both real-broker prototypes pass. The shared
conformance suite is the contract; the method spelling is not.

### Kafka

The Kafka engine uses `github.com/twmb/franz-go`.

- A unique splitter group consumes the source. On first activation its positions
  are initialized from the original app group's committed offsets, applying
  `offsetReset` where no offset exists.
- A deterministic static group instance ID fences an older queue-agent process
  before its replacement accepts work. The producer also uses a deterministic
  transactional ID solely for producer-epoch fencing, even though v1 does not
  promise transactional delivery. The replacement initializes its producer and
  obtains the new epoch before consuming; the prototype must prove that a stale
  producer cannot publish after that point. A fenced process closes all broker
  clients and reports unhealthy.
- Each shadow topic starts with the source partition count. Records retain their
  partition index, and produces for one destination partition remain ordered.
- A source offset commits only after the destination produce is acknowledged. A
  crash after produce and before commit causes a duplicate.
- The app and every session use distinct shadow groups. This leaves the original
  group empty while the split is active and makes handback well-defined.
- Partition growth is detected. V1 either grows every shadow before accepting a
  new partition or enters `DEGRADED`; it never modulo-maps silently. Topic
  recreation and partition-count reduction are unsupported.
- Shadows use delete cleanup with `retention.ms=-1` and `retention.bytes=-1`
  while an activation exists. Preflight reads the effective topic configuration
  and rejects brokers or policies that cannot guarantee unbounded retention.
  Cleanup restores nothing because the topics are then deleted. Documentation
  covers disk quotas and lag alerts. Preflight also verifies permission to
  describe/create/delete topics, consume/produce, and read/alter group offsets.
- Cleanup removes only resources recorded for the activation. A delete failure
  is an observable leak, not a reason to undo a completed handoff.

The prototype must prove with a real broker that offsets can be read and altered
in the intended states, produce-before-commit crashes recover, and app-shadow
end positions compare reliably with committed positions. `kfake` unit tests are
not evidence for these admin and recovery semantics.

### RabbitMQ

The RabbitMQ engine uses `github.com/rabbitmq/amqp091-go`.

- It consumes with manual acknowledgements, publishes through the default
  exchange to a durable shadow, enables publisher confirms, and acknowledges the
  source only after a positive confirm.
- Before consuming, it holds a deterministic exclusive lock queue on the same
  broker connection. A replacement cannot start until RabbitMQ has closed the
  predecessor's connection and released that lock.
- The source consumer is itself exclusive. After the initial zero-consumer
  preflight, RabbitMQ rejects any later competing consumer for the source queue.
- Every publish sets `mandatory=true`. The engine correlates returns and
  publisher confirms by sequence number and acknowledges the source only after
  a positive confirm with no return. Returned, negatively confirmed, and
  channel-ambiguous publishes are retried without acknowledging the source.
  Recovery recreates channels and consumers but never auto-deletes shadows.
- Message properties and headers are copied. Broker-generated delivery metadata
  is not treated as a portable message property.
- Route drain requires zero consumers before moving ready messages, so unacked
  deliveries cannot race the mover.
- Final app drain uses a quiesce-and-recheck cycle because passive queue depth
  does not include unacked deliveries.
- Preflight verifies a supported durable classic or quorum source and, after
  workload quiescence, no other consumers. The required management API supplies
  source properties and ready/unacknowledged counts; AMQP passive-declare
  results alone are not treated as sufficient proof.
- Shadow queues are declared with the source's queue type: a quorum (replicated)
  source must not drain into a single-node classic shadow, or a broker-node
  failure would violate the no-silent-loss principle that the source itself
  upholds. The lock queue stays a classic exclusive queue in either case.
- Source-consumer exclusivity must hold on both queue types. The phase-0
  prototype proves that a competing consumer is rejected on a quorum source —
  via the exclusive-consume flag if quorum queues honor it, otherwise by an
  equivalent guard — before the design relies on it.

A future exchange/binding engine may implement the lifecycle contract, but it
is not a hidden fallback in v1.

## Manager, RPC, and client changes

### Manager

Add a queue controller under `cmd/traffic/cmd/manager/state/` and share only
generic workload rollout primitives with the injector. It owns desired-state
persistence, Leases, workload cutover, intercept conflicts, agent health, and
recovery. Provider selection and broker operations remain under
`cmd/traffic/cmd/queueagent/`.

On startup, reconcile labelled ConfigMaps, Deployments, and Leases before
reaping anything. Client reconnect restores intercept intent; each queue route
also carries a stable random route ID and durable owner identity in
`InterceptSpec`, so an authorized reconnect can adopt its prior shadow. The
user-daemon retains that ID in its reconnect payload instead of regenerating it.
Each persisted route has a renewable expiry independent of the manager's RPC
session ID. A disconnected client keeps receiving routed messages for the
documented grace period; after expiry the route enters normal draining. A client
that lost its route ID cannot silently adopt the shadow and must use the
recovery command or wait for drain before creating a replacement.

If the queue-agent dies, the app may drain its shadow and then stall. The
Deployment restarts the agent, which loads the persisted generation and resumes
from broker checkpoints. The manager must not immediately point the app to the
source: that could replay the source while published shadows remain. An
unrecoverable agent is degraded and requires proved handback or an explicit,
documented recovery command.

### RPC

Changes to `rpc/manager/manager.proto` are additive and use new field numbers:

- `InterceptSpec` gains repeated attachments with logical queue, stable route
  ID, and string-map filter.
- `InterceptInfo` exposes per-queue phase and a human-readable blocking reason.
- Queue-agent RPCs cover arrival/reconnect, desired-generation watch,
  acknowledgement, health/status, and departure.

A newer client version-gates queue splitting and returns a clear error from an
older manager. Queue-agent and manager normally share a cluster image, but proto
changes still preserve wire compatibility. Run `make protoc`, `make lint-rpc`,
and the repository's proto compatibility review.

### Client

- Extend `pkg/client/cli/intercept/command.go` with `--queue` and
  `--queue-header`.
- Permit queue-only intercepts through the no-default-port path while ensuring a
  traffic-agent for effective app environment and mounts.
- Merge overrides only into the intercept-specific environment sent to that
  client.
- Show `PREPARING`, `ACTIVE`, `DRAINING`, and `DEGRADED` states and filters in
  `list`, `status`, JSON, and YAML. Redact endpoints and credentials.
- Present multi-queue attach as one coordinated operation: the intercept becomes
  active only when every queue is active. Failures expose per-queue rollback or
  degraded progress rather than claiming cross-broker transactionality.

## Security and installation

Add a disabled-by-default `queueSplitter:` section to
`charts/telepresence-oss/values.yaml`, with image and log-level fallbacks to the
manager image. Update `values.schema.yaml` and generated chart documentation.

Manager RBAC gains least privileges in managed namespaces for queue-agent
Deployments, state ConfigMaps, workload scale subresources, and HPA reads.
Ownership Leases live centrally in the manager namespace so identical broker
streams in different workload namespaces still conflict. The queue-agent gets
no list access to Secrets. Kubernetes resolves
explicit EnvVars and mounts explicit Secret keys into its Pod. Its bound service
account token is verified on the manager's internal agent path.

The threat model covers a user modifying workload annotations to exfiltrate a
Secret through a queue-agent. Secret and EnvVar references are limited to those
already present in the selected workload Pod template. Authorization reuses
intercept authorization and the managed-namespace boundary.

Document NetworkPolicy requirements from queue-agent to broker and laptop to
broker. An in-cluster broker normally uses the existing VIF; an external broker
may be reached directly, but that is a deployment property, not a guarantee.

For an ordinary Helm uninstall with hooks enabled, the pre-delete hook attempts
orderly deactivation of every split. If a broker is unavailable, the hook fails
and the release remains installed. This cannot protect `--no-hooks`, namespace
deletion, direct finalizer removal, or out-of-band resource deletion. The
operations guide documents those bypasses, labels used to discover orphaned
ConfigMaps/Deployments, broker resource naming, and a manual recovery/handoff
procedure. Telepresence tooling never silently deletes broker data.

## Observability

Expose per-workload and per-queue state without high-cardinality route labels:

- source messages accepted, shadow publishes, retries, and detectable
  duplicates;
- ready/unacked or committed/end lag per shadow;
- desired and observed generation;
- time in prepare, quiesce, drain, and degraded states; and
- cleanup failures and orphaned broker resources.

Logs include install ID, workload UID, logical queue, activation ID, and route
ID, but never credential-bearing URLs, Secret values, message bodies, or
headers. `telepresence status` reports the state-machine step and safe operator
action when progress is blocked.

## Testing

### Shared conformance suite

Run the same lifecycle scenarios against both real providers:

- unmatched, single-filter, disjoint filters, and rejected overlap;
- header edge cases and field preservation;
- source and shadow acknowledgement/commit crash boundaries;
- stale-agent broker fencing, RabbitMQ return/confirm correlation, and Kafka
  unbounded-retention preflight;
- attach, route stop, consumer disconnect, residue move, and cleanup;
- app drain and handback with in-flight deliveries;
- queue-agent, manager, and combined restart from each persisted state; and
- route lease adoption/expiry, workload deletion finalizers, partial multi-queue
  activation rollback, and cleanup retries proving non-empty shadows remain.

Messages carry unique IDs. Tests assert every input is eventually observed,
allow documented duplicates, and ensure no message reaches two live
destinations before a crash/recovery boundary.

### Unit and cluster tests

- Unit: annotation parsing, env resolution, overlap detection, bounded names,
  state transitions, generations, ownership keys, and recovery decisions.
- Kafka may use `franz-go/pkg/kfake` for unit tests, but offset handoff and admin
  behavior run against real single-node KRaft Kafka.
- RabbitMQ confirm, reconnect, consumer-count, and unacked behavior run against
  a real broker; narrow routing code can use fakes.
- `regression_test/` deploys both brokers per suite and runs one
  provider-parameterized scenario body for activation, two sessions, conflict,
  detach, deactivation, agent kill, manager kill, and failed rollback. A mixed
  workload runs one engine of each provider in one queue-agent.
- Workload tests cover Deployment and StatefulSet, replicas greater than one,
  rejected HPA, active-set expansion while another queue pumps, final
  workload-wide deactivation, override-name collisions, failed rollout,
  concurrent manual scale, client disconnect, manager upgrade, and chart
  uninstall.

Before completion, run `make check-unit`, the focused regression suite, `make
lint`, `make lint-rpc`, and `make lint-docs`.

## Dependencies and licenses

- `github.com/twmb/franz-go` (BSD-3-Clause).
- `github.com/rabbitmq/amqp091-go` (BSD-2-Clause).
- Test images: Apache Kafka (Apache-2.0) and RabbitMQ server (MPL-2.0), used only
  at test runtime.

Confirm transitive versions and regenerate `DEPENDENCIES.md` and
`DEPENDENCY_LICENSES.md` with `make generate` before landing dependencies. The
forbidden-license check remains authoritative.

## Delivery plan

Each phase is reviewable. The feature stays disabled until the final chart and
documentation phase.

0. **Provider feasibility gates.** Build throwaway real-broker prototypes for
   Kafka offset transfer and RabbitMQ quiesce/drain, the latter against both a
   classic and a quorum source queue: consumer exclusivity, management-API
   ready/unacked reporting, republish-to-source, and shadow queue type behavior
   must hold on both. Record observed behavior in tests. Stop and revise this
   plan if either handoff cannot prove the contract, or scope quorum queues out
   explicitly if only they fail.
1. **Config and state model.** Add annotation schema, validation, naming,
   durable ConfigMap schema, state machine, overlap detection, and recovery unit
   tests.
2. **Engine contract and providers.** Write the real-broker conformance harness,
   then implement Kafka and RabbitMQ together. Do not freeze the interface until
   both pass activation, crash recovery, route drain, and handback.
3. **Queue-agent and manager controller.** Add the subcommand, Deployment and
   ConfigMap reconciliation, Leases, authenticated RPCs, health, restart
   recovery, and cutover for Deployment and StatefulSet.
4. **Intercept and client integration.** Add proto fields, stable route IDs,
   queue-only flow, env merging, CLI flags, conflicts, status, and reconnect.
5. **Installation and operations.** Add Helm values/schema/templates, RBAC,
   pre-delete behavior, metrics, alerts, and recovery tooling.
6. **Regression tests and documentation.** Add provider-parameterized tests, a
   how-to, reference, troubleshooting/recovery guidance, architecture docs, and
   a `CHANGELOG.yml` entry. Run `make docs-files` after the changelog edit.

When all phases are implemented and documented, remove this plan directory in
the final implementation change, as required by repository policy.

## Accepted limitations and follow-up work

- Activation and final deactivation interrupt the workload. Future broker
  fencing may remove that downtime.
- Filter overlap is rejected rather than prioritized.
- Kafka partition growth and retention need operational limits; topic
  recreation is unsupported.
- RabbitMQ v1 supports classic and quorum queues but no stream queues and no
  declaration arguments beyond `x-queue-type`.
- A local consumer must stop before its route can finish draining.
- Broker resources can leak when deletion fails, but leaks are named, reported,
  and safe to retry after handoff.
- SQS, RabbitMQ exchange bindings, standing splits, HPA-aware cutover, and
  zero-downtime activation are separate proposals.
