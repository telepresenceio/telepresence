# Single client watcher for workload and attachment events

## Problem

Each connected client currently holds four concurrent watcher streams against the
traffic-manager, spread over two gRPC connections (the user daemon's and the root
daemon's):

| Stream | Held by | Payload | Scope |
|--------|---------|---------|-------|
| `WatchAgentsDelta` | user daemon (`trafficmgr/agents.go`) | `AgentInfo` (heavy: container env, mounts) | connected namespace |
| `WatchInterceptsDelta` | user daemon (`trafficmgr/intercept.go`) | `InterceptInfo` | this client's intercepts |
| `WatchAgentPodsInNamespacesDelta` | root daemon (`agentpf/clients.go`) | `AgentPodInfo` (slim: pod IP, api port, intercepted, quic SNI) | all port-forwardable mapped namespaces |
| `WatchClusterInfo` | root daemon (`rootd/session.go`) | `ClusterInfo` | cluster networking |

On the manager side, the first three all derive from the same two internal state
watchers (`state.WatchAgents`, `state.WatchIntercepts`); serving them as separate
streams costs one goroutine set and one set of internal subscriptions per stream,
per client: five subscriptions across three streams.

The streams are also denormalized and heavy:

- For the connected namespace, every agent pod is transmitted twice, in two
  shapes, on two connections (`AgentInfo` on the user daemon's watcher,
  `AgentPodInfo` on the root daemon's).
- The `AgentInfo` stream continuously carries the containers map — per-container
  **environment variables** (which can be tens of KB), mount points, and mount
  policies — re-sending all of it in every upsert of an agent, on every pod
  churn event, even though the user daemon only reads it when an ingest is
  created or its backing pod is replaced. Intercepts never read it at all:
  their environment, ports, and mount point arrive in `InterceptInfo`.

## Goal

One traffic-manager watcher stream per client for everything related to
workloads and attachments, registered by the **user daemon**, which relays the
agent-pod events to the root daemon. The second remaining stream is
`WatchClusterInfo` (unchanged, still held by the root daemon).

Requirements:

1. **Full normalization.** Each fact crosses the manager→client boundary
   exactly once, in exactly one shape. Bulk container data (environments,
   mounts, ports) is not streamed at all: it is retrieved on demand — from
   the `EnsureAgent` response when an ingest needs it, and from
   `InterceptInfo` for intercepts.
2. **No partially populated structs.** A message type on a stream is always
   fully populated. In particular, no "slimmed" `AgentInfo` whose containers
   map is silently absent.
3. **Bidirectional backward compatibility.** A new client against any older
   supported traffic-manager, and an old client against a new traffic-manager,
   must both behave exactly as today. Compatibility is discovered the way the
   existing fallback chains discover it — by probing and reacting to
   `Unimplemented` — never by assuming version parity.

Net result per client: manager-side watcher streams go from 4 to 2, internal
manager subscriptions from 5 to 3, `AgentInfo` (and its container payload)
disappears from watch streams entirely, and the agent-pod projection is
computed once, by the manager, in one shape.

## Design

### 1. New manager RPC: `WatchSessionEvents`

```proto
message SessionEventsRequest {
  SessionInfo session = 1;
  // Namespaces in which the client wants agent-pod events in addition to
  // the connected namespace: the mapped namespaces the client is permitted
  // to port-forward to. Validated by the manager exactly like
  // WatchAgentPodsInNamespacesDelta (agentPodNamespaces).
  repeated string namespaces = 2;
}

message SessionEventsDelta {
  // Agent pods in the requested namespaces plus the session's connected
  // namespace. Same projection as WatchAgentPodsInNamespacesDelta,
  // including the per-client intercepted flag and quic_sni.
  AgentPodInfoDelta agent_pods = 1;

  // The watching client's intercepts.
  InterceptInfoDelta intercepts = 2;
}

rpc WatchSessionEvents(SessionEventsRequest) returns (stream SessionEventsDelta);
```

`AgentPodInfo` is amended with one field — `string version = 10` (the agent's
version, needed by `telepresence list`); with that, it is sufficient for every
stream consumer, and every instance on the wire is fully populated. The
`intercepted` flag stays manager-computed (as on the legacy agent-pod streams):
it is derivable from the intercepts on the same stream, but it is one boolean,
and carrying it makes the userd→rootd relay a pure pass-through instead of a
derivation layer whose equivalence with the manager would have to be
maintained by tests. The payload-redundancy requirement is about bulk data,
which this stream no longer carries at all.

The three legacy RPCs (and their non-delta fallbacks) remain served, unchanged,
for older clients.

**Manager implementation** (`cmd/traffic/cmd/manager/service.go`): the
agent-pod projection used by `watchAgentPodsDelta` (AgentSession →
`AgentPodInfo` with inactive-pod filtering, `IsInterceptedBy`,
`quicSNIForAgent`, debounced `cache.Map` dedup — now also filling `version`)
is extracted into a helper shared by the legacy handler and the new one. The
combined handler runs three internal subscriptions per client (down from five
across three streams):

- one `state.WatchAgents` filtered on namespace ∈ (requested ∪ connected),
  feeding the shared projection;
- one `state.WatchIntercepts` with the legacy client-session filter, driving
  the `Intercepted` refresh (exactly `createAgentPodWatchers`' channel);
- one `state.WatchIntercepts` via the existing `watchIntercepts` helper (own,
  not REMOVED, not child), feeding the stream's `intercepts` field.

Ordering bonus: agent-pod and intercept events arrive serialized on one
stream, removing a class of cross-stream races.

### 2. User daemon: one combined watcher loop, on-demand container data

`startServices` runs one `session-events` goroutine (replacing the `agents`
and `intercept-port-forward` pair) plus the relay runner. The combined loop is
a single `watcher.WatchWithRetry` over `WatchSessionEvents`, maintaining an
agent-pod map and an intercept map, dispatching:

- `delta.AgentPods` → fold into the pod map; update the session's internal
  agent-pod cache (below); run the ingest lifecycle; forward the delta as-is
  to the relay.
- `delta.Intercepts` → fold, `handleInterceptSnapshot` (unchanged consumer,
  including the `podAccessTracker`) — **on a dedicated goroutine**, fed
  through a one-slot coalescing mailbox of full snapshots. The intercept
  handler can block in `ensureAccess` waiting for the root daemon to learn
  an agent-pod IP, and that pod's delta may be queued *behind* the intercept
  delta on the same multiplexed stream; handling intercepts inline would
  starve the relay until the wait times out (observed on stream
  re-establishment after a traffic-manager restart, where the initial
  intercepts snapshot precedes the initial agent-pod snapshot). The
  dedicated goroutine restores the legacy concurrency model, where the
  intercept loop only ever blocked itself.

The repair function clears both maps, marks the relay for a reset
(`beginSync`), and calls `s.reconnectManager()`. Final cleanup on exit mirrors
the legacy loops' tails (empty snapshots so port-forwards and mounts are
cancelled), skipped when an immediate `Unimplemented` probe failure hands the
lifecycle to the legacy fallback.

**Internal agent-pod cache.** `session.currentAgents []*manager.AgentInfo` is
replaced by a small domain type holding exactly what its consumers read —
workload, namespace, pod name, agent version, node-agent flag — built from
`AgentPodInfo` in combined mode and projected from `AgentInfo` in legacy
fallback mode. This keeps proto structs fully populated or absent, never
partial. Consumers:

- `WorkloadInfoSnapshot` (list): workload name + agent version.
- `Ingest`: existence and node-agent flag only (see below).
- `ReconnectClient`: **no longer sends agents at all.** The client has no
  full `AgentInfo`s to restore, and restoring pod-shaped stubs into manager
  state would hand other consumers partially populated structs. Traffic-agents
  hold their own manager sessions and re-arrive on their own within seconds of
  a manager restart; intercepts are still restored in full (the client keeps
  complete `InterceptInfo`s). The proto field remains for older clients.

**Ingest reads container data on demand.** `Ingest` always obtains its full
`AgentInfo` (env, mounts, container names, kind, sftp/ftp ports) from the
`EnsureAgent` response — the path cross-namespace ingests already take today.
The pod cache is consulted only for the fast path (an existing ingest for the
same key returns immediately) and for node-agent semantics: a cached
node-agent silently satisfies a plain ingest request (the request to
`EnsureAgent` is made with node-agent set so the manager must not inject a
sidecar on top), and a cached sidecar rejects a `--node-agent` request, both
exactly as today.

**Ingest lifecycle (fixes a pre-existing defect).** The per-snapshot walk that
keeps ingest mounts alive becomes namespace-correct:

- Matching is by workload **and namespace** (today `infosForKey` matches by
  workload name only, so a same-named workload in the connected namespace can
  hijack a cross-namespace ingest's agent identity).
- When the backing pod of an ingest is replaced, the full `AgentInfo` for the
  new pod is refetched via `EnsureAgent` (synchronously, with the standard
  API timeout; pod replacement is rare) and the container environment is
  re-translated. This makes replacement failover work for cross-namespace
  ingests for the first time.
- `cancelUnwanted` is scoped to the namespaces the snapshot actually covers
  (requested ∪ connected in combined mode, connected only in legacy mode), so
  an ingest in an uncovered namespace is no longer killed by unrelated agent
  churn — the pre-existing latent bug that the combined stream would otherwise
  have made deterministic.

**Manager compatibility: the user daemon owns it entirely.** On
`Unimplemented`, the loop falls back to driving the three legacy manager
watchers itself: `WatchAgentsDelta`→`WatchAgents` (feeding the internal pod
cache via projection), `WatchInterceptsDelta`→`WatchIntercepts`, and the
agent-pod chain `WatchAgentPodsInNamespacesDelta`→`WatchAgentPodsDelta`→
`WatchAgentPods` (extracted as `agentpf.WatchPods`, shared with the root
daemon's legacy self-watch mode), feeding the relay as-is. The relay contract
toward the root daemon is identical no matter how old the manager is.
`s.managerVersion` may be consulted to skip a doomed probe, but `Unimplemented`
handling is the authoritative mechanism.

### 3. Relaying agent-pod events to the root daemon

New RPC on the root daemon's `daemon.Daemon` service (user daemon is the
caller — the root daemon is already the gRPC server in this pair):

```proto
message AgentPodsDelta {
  // When true, discard all previously received agent-pod state before
  // applying the upserts/removals in this message. Set on the first message
  // of every relay stream and whenever the user daemon's traffic-manager
  // watcher has been re-established.
  bool reset = 1;
  map<string, manager.AgentPodInfo> upserts = 2;
  repeated string removals = 3;
}

rpc WatchAgentPods(stream AgentPodsDelta) returns (google.protobuf.Empty);
```

The relay is a **pass-through** in both combined and legacy modes: the
manager-computed `AgentPodInfo` is forwarded unchanged, so both modes are
identical by construction. The user daemon accumulates the current pod map so
that a (re)connecting root daemon can be brought to a known state with a
`reset` + full snapshot; subsequent pushes are minimal diffs.

- The user daemon opens this client-streaming call after `connectRootDaemon`
  succeeds (and re-opens it after `reconnectRootDaemon`). Deltas that arrive
  while the relay is down are absorbed into the accumulated map — nothing is
  lost, the next `reset` snapshot covers them.
- In-process mode (`RootSessionInProcess`, docker/pod daemon): no gRPC —
  `InProcSession` exposes `ApplyAgentPodsDelta` and the user daemon calls it
  directly.

**Namespace source of truth.** The user daemon computes the port-forwardable
namespace set once (`GetCurrentNamespaces(true)` filtered through
`k8s.CanPortForward` — the computation the root daemon's `Start` used to do)
and passes it both to the manager (`SessionEventsRequest.namespaces`) and to
the root daemon (`NetworkConfig.agent_pod_namespaces`), so all parties agree
without computing it independently. Non-empty presence of the `NetworkConfig`
field also tells the root daemon that the user daemon owns the agent-pod
watch.

### 4. Root daemon / agentpf changes

`agentpf.Clients` gains a delta-sink entry point alongside the existing
self-watching `WatchAgentPods`:

```go
ApplyPodsDelta(reset bool, upserts map[string]*manager.AgentPodInfo, removals []string) error
RunDeltaSink(rmc manager.ManagerClient) error
```

`ApplyPodsDelta` shares the snapshot-map + `updateClients` path with the
self-watching mode; `RunDeltaSink` takes over the lifecycle duties (manager
client registration for lazy QUIC endpoint fetches, teardown) that previously
rode on the self-watch. In `rootd/session.Start`, the mode is decided solely
by `NetworkConfig.agent_pod_namespaces` — the manager's version plays no part
(the user daemon absorbs manager compatibility, §2). The legacy self-watch
remains, its fallback chain extracted into `agentpf.WatchPods` so the user
daemon reuses the same implementation.

A relay stream that dies while the session lives is re-established by the user
daemon (with `reset=true`); the root daemon never falls back to self-watching
mid-session — if the user daemon is gone, the session is dead anyway.

### 5. Compatibility matrix

| user daemon | root daemon | manager | Behavior |
|-------------|-------------|---------|----------|
| new | new | new | Combined watcher + relay. 2 manager streams per client. |
| new | new | old | `WatchSessionEvents` probe → `Unimplemented` → userd drives the three legacy manager watchers itself and relays pod info as-is. Rootd stays in relay mode, oblivious to manager age. Same manager load as today (4 streams), all on the userd's connection. |
| new | new | very old (pre-delta RPCs) | The per-watcher fallback chains bottom out exactly as today (`WatchAgents`, `WatchIntercepts`, `WatchAgentPods` full snapshots). Relay unchanged. |
| new | old | new/old | Rootd ignores the unknown `NetworkConfig` field and self-watches (legacy manager RPCs still served, with rootd's own fallback chain); userd's relay call returns `Unimplemented` → log and stop relaying. Correct, just one redundant stream. |
| old | new | new | No `agent_pod_namespaces` in `NetworkConfig` → rootd self-watches via its retained legacy code. Today's behavior. |
| old | any | new | Legacy RPCs, unchanged wire behavior — additions only in the proto, no field or RPC removed or renumbered. |

No legacy RPC is removed from the manager, and no legacy fallback chain is
removed from the client. The traffic-agent's use of `WatchInterceptsDelta`
(agent→manager) is untouched. `ReconnectClientRequest.agents` remains served
for old clients; new clients simply leave it empty.

### 6. Normalization constraints going forward

- `AgentPodInfo` is the single streamed shape for everything pod-level that
  clients learn from the manager. Any future pod-level fact goes there.
- Container-level data (environments, mounts, ports) is never streamed; it
  travels only in `EnsureAgent`/`GetAgentConfig` responses and in
  `InterceptInfo`. Any future bulk per-container fact follows the same rule.
- Proto messages on a stream are always fully populated; a consumer that needs
  less defines its own narrower type (as the user daemon's internal agent-pod
  cache does) rather than receiving a partially filled shared shape.

### 7. Out of scope / future work

- **`WatchClusterInfo` stays as-is** (root daemon, own stream). The user
  daemon's `GetClusterSubnets` also opens a short-lived `WatchClusterInfo`;
  request-scoped, unchanged.
- **`WatchWorkloads`** (per-namespace, started lazily by `ensureWatchers` for
  list/status) is not folded in; a natural follow-up once the combined stream
  exists.

### 8. Testing

- **Manager**: unit tests for `WatchSessionEvents`: pod projection per
  namespace scoping (requested ∪ connected, exclusion outside), version
  field, `Intercepted` flip on intercept creation/removal, inactive-pod
  filtering, own-intercepts-only on the intercepts field, session-done
  teardown. Shared-projection refactor must keep `watchAgentPodsDelta` tests
  green.
- **proto**: additions only; `make protoc` + protolint; `proto-rpc-reviewer`
  checks.
- **userd**: pod-cache building from both stream shapes; ingest lifecycle
  (namespace-correct matching, replacement refetch, scoped cancelUnwanted —
  an ingest in an uncovered namespace survives connected-namespace churn);
  Ingest node-agent semantics via the pod cache; relay pass-through and reset
  semantics (fake sink, both stream and in-process); fallback compatibility
  (degrade through legacy chains with identical relay output).
- **agentpf**: existing `ApplyPodsDelta`/`WatchPods` tests unchanged.
- **Integration**: intercept + ingest suites against the new path; verify via
  manager logs that clients ride `WatchSessionEvents`; manager-restart
  reconnect (intercepts restored, agents re-arrive on their own).

### 9. Implementation order

1. proto: amend `AgentPodInfo` (`version`), redefine `SessionEventsDelta`
   (`agent_pods`), `make protoc`. (The previous `AgentInfoDelta`-based shape
   never shipped; this branch is its only user.)
2. Manager: shared agent-pod projection helper (+`version`), reworked
   combined handler, tests.
3. userd: internal pod cache, `Ingest` via `EnsureAgent`, namespace-correct
   ingest lifecycle + scoped `cancelUnwanted`, pass-through relay, reconnect
   without agents, legacy fallback projection; tests.
4. Changelog (feature wording + bugfix entry for the cross-namespace ingest
   mount defect) and verification chain.
