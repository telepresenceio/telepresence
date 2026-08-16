# Phase 0 findings: provider feasibility gates

Date: 2026-08-15. Verdict: **GO** for both providers. Every proof ran as Go
tests against a real single-node broker in a container. One design change
resulted (quorum-queue source ownership, below); everything else confirmed the
plan as written.

Prototype code is throwaway and deliberately uncommitted; it lives in the
local working tree under `hack/queue-splitting-phase0/{kafka,rabbitmq}/`, each
a standalone Go module with a `RESULTS.md` recording raw observations. The
durable record is this document; the phase-2 conformance suite re-encodes
these behaviors as maintained tests.

## Kafka (apache/kafka:latest = 4.3.1, KRaft; franz-go v1.21.6, kadm v1.18.0)

| Proof | Result | Observed |
|-------|--------|----------|
| K1 offset bootstrap | Pass | Empty-group `FetchOffsets` + pre-join `CommitOffsets` onto the splitter group resume at exact per-partition positions (verified with uneven offsets 6/3/8) |
| K2 crash before commit | Pass | Crash after shadow-produce ack, before source commit: exactly one duplicate, nothing lost |
| K3 alter empty group's offsets | Pass | Durable while the group is empty; refused with `UNKNOWN_MEMBER_ID` (25) while any member is live |
| K4 drain arithmetic | Pass | `ListEndOffsets` vs committed offsets differ by exact backlog, equal when drained |
| K5a static-membership fencing | Pass | Same `group.instance.id` rejoins: old consumer fenced with `FENCED_INSTANCE_ID` (82), new one gets the full assignment |
| K5b producer-epoch fencing | Pass | Replacement producer with the same transactional ID fences the stale one: produce fails `INVALID_PRODUCER_EPOCH` (47), commit fails `PRODUCER_FENCED` (90) |
| K6 retention preflight | Pass | `retention.ms=-1` / `retention.bytes=-1` read back exactly via `DescribeTopicConfigs` |

Findings folded into the plan:

- Fencing surfaces only on broker round-trips. A fenced producer's
  `BeginTransaction()` still succeeds (client-side bookkeeping); the engine
  must treat produce/commit errors as the fence signal, never a successful
  transaction begin.
- The broker refuses *any* external offset commit while the group has a live
  member, and the error is the blunt `UNKNOWN_MEMBER_ID`, not a conflict
  code. This is the guard the handback sequence relies on, and the error is
  worth mapping to an actionable message.

## RabbitMQ (rabbitmq:4-management = 4.3.4; amqp091-go v1.13.0)

| Proof | Classic | Quorum | Observed |
|-------|---------|--------|----------|
| R1 exclusive source consumer | Pass | **Not enforced** | Classic rejects a competitor with 403 `ACCESS_REFUSED`; a quorum queue admits a second consumer with no error despite `exclusive=true` |
| R2 lock-queue fencing | Pass | n/a (lock queue is always classic) | 405 `RESOURCE_LOCKED` for the contender; released on connection close |
| R3 confirms + mandatory returns | Pass | n/a (exchange-level) | Unroutable publish still gets `ack=true`; only `basic.return` (312 `NO_ROUTE`) signals it, correlated to confirms by publish sequence number |
| R4 management-API observability | Pass | Pass | Type, arguments, ready/unacked/consumer counts all reported; unacked deliveries invisible to passive declare |
| R5 quiesce/drain + rollback | Pass | Pass | Unacked requeued with redelivered flag on connection close; drain to shadow and republish-to-source both verified with confirms |
| R6 quorum shadow parity | n/a | Pass | Quorum shadow behaves identically end-to-end |
| R7 conditional queue.delete | Partial | **Rejected** | Classic honors `if-unused` (406 for an active `basic.consume`) and `if-empty`, but neither sees a parked unacked `basic.get` delivery — the delete destroys it; quorum rejects both flags outright (540 `NOT_IMPLEMENTED`, closing the whole connection), while its live consumer count does surface a parked `basic.get` hold (classic's reads 0) |

Findings folded into the plan:

- **Quorum queues do not enforce exclusive consumption** — the R1 headline
  and the one result that changes the design. The exclusive-consume flag is
  honored on classic queues only. Consequence: on a quorum source, ownership
  cannot be broker-enforced against foreign consumers. The plan now treats
  quorum sources the way it already treats Kafka consumer groups: exclusive
  use is a documented operator precondition, verified at preflight
  (zero consumers) and monitored continuously through the management API,
  with `DEGRADED` on violation. Stale *queue-agent* processes are still
  fenced for both queue types by the classic exclusive lock queue (R2).
  `x-single-active-consumer` cannot substitute: it is a declaration argument
  the existing source queue does not carry.
- A positive confirm does not mean "routed": `ack=true` arrives even for an
  unroutable mandatory publish. The plan's requirement of *confirm plus no
  return* is exactly right and now proven necessary, with sequence-number
  correlation demonstrated (a `basic.return` carries no delivery tag).
- Quorum-queue management-API statistics lag AMQP-visible state by up to the
  stats emission interval (about 5 s observed). Anything reading counts must
  poll with deadlines, not read once.
- Drain mechanics that the engine must encode: cap prefetch or the broker
  eagerly pushes the entire ready backlog into the unacked set; cancel the
  consumer before acking its final deliveries or freed slots are instantly
  refilled; cancel all consumers before a rollback republish or an active
  consumer re-consumes the republished messages immediately.
- **No loss-proof delete exists for a quorum queue** (R7, verified on both
  3.13.7 and 4.3.4): both conditional `queue.delete` flags are rejected
  outright, and
  an unconditional delete destroys whatever arrives concurrently — a
  check-then-delete sequence is an unbounded race, not a guarantee. Classic
  `if-unused` guards only active `basic.consume` clients; a parked unacked
  `basic.get` delivery is invisible to it and to classic's consumer count,
  and is destroyed by the delete. Consequence: cleanup readiness is causal —
  the manager verifies consumer quiescence (connections closed, unacked
  requeued) and the engine reads live broker state; classic shadows then
  delete under `if-unused`+`if-empty` belts, while quorum shadows are never
  deleted automatically: verified empty, retained as cleanup-pending
  inventory, reaped only by deliberate operator action.
