# Short-circuit local traffic to intercepted destinations (#4125)

## Problem

When two services are both handled locally (service A's handler dials service B,
and B is also intercepted by the same client), a request from A to B's cluster
name round-trips through the cluster: TUN → traffic-manager/agent → agent
tunnel back → local handler for B. The result is correct but adds two
network legs and makes local-to-local latency depend on the cluster.

Issue #4125 proposes a manual `local-intercept add <dns>:<port>:<localport>`
command. Since the client already knows exactly which cluster destinations its
own intercepts cover and where their handlers listen, the redirect can instead
be fully automatic: configurable, enabled by default, with the logic in the
root daemon.

## Approach

When the root daemon's stream creator sees an outbound connection to a cluster
destination that is covered by one of this client's active intercepts, it pipes
the connection directly to the intercept's local target instead of opening a
tunnel to the cluster.

This is semantically equivalent to today's behavior for OSS intercepts (they
are global: *all* traffic to the intercepted port ends up at the local handler
anyway) minus the cluster round trip.

### Data flow

1. **userd** already watches its own intercept snapshot
   (`handleInterceptSnapshot`, `pkg/client/userd/trafficmgr/intercept.go`).
   On every snapshot it derives, for each ACTIVE intercept:
   - the workload identity (name, namespace) and the intercepted container
     port + protocol,
   - the service ClusterIP(s) + service port (for service-based engagements),
   - the local target: `spec.TargetHost:TargetPort`, with synthetic-IP
     target hosts resolved via the existing `Resolve()` machinery.
2. **userd → rootd**: a new daemon RPC, `SetInterceptShortcuts`, pushes the
   full list with replace-all semantics (same pattern as `SetDNSMappings`).
   Implemented in `rpc/daemon/daemon.proto`, `pkg/client/rootd/grpc.go`, and
   `pkg/client/rootd/in_process.go`.
3. **rootd** matches outbound destinations against the shortcuts in
   `streamCreator()` (`pkg/client/rootd/stream_creator.go`), after the
   agent-VIP translation has restored the original cluster destination and
   after the `l4PortMap` remapping:
   - an exact hit on a service `AddrPort` from the pushed list, or
   - a hit on (pod IP of any agent pod belonging to the intercepted workload,
     container port), resolved dynamically against the agent-pod snapshot the
     rootd already maintains (`pkg/client/agentpf`, `AgentPodInfo` carries
     `workload_name`). This covers every replica, including pods created
     after the intercept, without re-pushing.

   A hit creates a local pipe (`tunnel.NewPipe` + `tunnel.NewDialerTTL`, the
   same pattern the DNS short-circuit uses) to the local target instead of a
   tunnel stream.

   With `cluster.agentPortForward=false` the rootd has no agent-pod snapshot;
   pod-IP dials then keep taking the cluster path while the service-address
   shortcuts continue to work.

### Configuration

New boolean in the client config `intercept` section, **default true**:

```yaml
intercept:
  localShortcut: true
```

Follows the existing default-true pattern (`mergeNonDefaults`, cf.
`docker.enableIPv4`). Being part of the client config, it is automatically
overridable cluster-wide through the Helm chart's `client.*` values. When
false, userd simply never sends redirect entries.

### Scope and semantics

- Only intercepts with traffic semantics participate: `intercept` and
  `replace`. Ingests don't route traffic and are excluded.
- Only the client's own ACTIVE intercepts are considered (the session-scoped
  intercept watch yields nothing else).
- Both TCP and UDP, keyed by protocol.
- Hairpins are intentional: an intercepted handler dialing its own service
  name reaches itself locally — the same place a cluster round trip would
  deliver it.
- Docker mode needs no special casing: userd and the in-process rootd share
  the daemon container's network namespace, so `spec.TargetHost` is exactly as
  reachable from the redirect dial as it is from today's agent-tunnel dial.
- Multi-replica workloads are fully covered by the workload-based pod
  matching against the rootd's agent-pod snapshot.

## Affected files (anticipated)

- `rpc/daemon/daemon.proto` — `SetInterceptShortcuts` RPC + messages.
- `pkg/client/rootd/grpc.go`, `pkg/client/rootd/in_process.go` — RPC plumbing.
- `pkg/client/rootd/session.go` — shortcut state on the session.
- `pkg/client/rootd/stream_creator.go` — the short-circuit decision.
- `pkg/client/agentpf/clients.go` — lookup of agent pods by workload, if not
  already exposed.
- `pkg/client/userd/trafficmgr/intercept.go` — derive + push shortcut entries
  from the intercept snapshot.
- `pkg/client/config.go` — `Intercept.LocalShortcut` option.
- `charts/telepresence-oss/values.schema.yaml` + README — `client.intercept.
  localShortcut`.
- `CHANGELOG.yml`, docs regen.

## Testing

- **Unit**: stream-creator decision (service-address hit → local pipe,
  workload pod-IP hit → local pipe, miss → tunnel; pushes replace fully;
  disabled config sends nothing).
- **Integration**: engage an echo workload with a local handler, dial the
  service's cluster DNS name (and a replica pod IP) from the workstation, and
  assert (a) the response is served by the local handler, and (b) `daemon.log`
  contains the shortcut log line for that destination — proving the connection
  never took the cluster path. Repeat with `intercept.localShortcut: false`
  and assert the shortcut line is absent while the response still arrives
  (via the round trip).

## Decisions

1. Config name: `intercept.localShortcut`.
2. Pod matching is workload-based against the rootd's agent-pod snapshot
   (covers all replicas; degrades to the cluster path when
   `cluster.agentPortForward=false`).
3. Manual mappings for arbitrary DNS names (the literal `local-intercept add`
   from the issue) are out of scope; the rootd state + RPC introduced here is
   the natural substrate for a follow-up.

## Rollout

Phase 1: proto + config + userd derivation + rootd redirect, unit tests.
Phase 2: integration test, live verification on minikube.
Phase 3: chart/docs, changelog, drop this plan.
