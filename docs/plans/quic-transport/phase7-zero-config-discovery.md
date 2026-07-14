# Phase 7: zero-configuration endpoint discovery

Read `README.md` in this directory first. Design context: `design.md`,
"Zero-configuration endpoint discovery".

## Goal

`--set quicTunnel.enabled=true` alone gives working QUIC transport on any
cluster whose Service type the manager can observe. Today the endpoint is not
advertised until the admin also sets `quicTunnel.externalHost` (see
`GetQuicTunnelEndpoint` in `cmd/traffic/cmd/manager/service.go` ~line 1129:
`if s.quicCA == nil || env.TunnelQuicExternalHost == "" { Enabled: false }`).
After this phase the manager discovers the reachable address(es) from the
forwarder's Service and advertises an ordered candidate list; the client
probes candidates concurrently and keeps the first that completes a handshake.
`externalHost`/`externalPort` remain as overrides that replace discovery.

## Current state (verify before starting)

* Chart: `charts/telepresence-oss/values.yaml` `quicTunnel` block —
  `service.type: LoadBalancer` default, `service.nodePort: 0`,
  `externalHost: ""`. The Service is created by
  `templates/quicforwarder.yaml`; confirm its exact `metadata.name` there
  (the deployment.yaml comment calls it the "traffic-manager-quic" Service)
  and whether it is created only when `quicTunnel.service.create`.
* Manager env: `managerutil.Env` fields `TunnelQuicPort`,
  `TunnelQuicExternalHost`, `TunnelQuicExternalPort`, `TunnelQuicAgentPort`
  populated from `TUNNEL_QUIC_*` in `templates/deployment.yaml` (~line 136).
* Client dial: `pkg/client/rootd/quic.go` `startQuicTunnel` — single
  `ep.Host:ep.Port` dial with `quicDialTimeout` (3 s) budget.
* Agent dials reuse the discovered endpoint: find where
  `pkg/client/agentpf/quic.go`'s `epFor(ctx)` gets its `quicEndpoint` and
  make sure the *chosen* candidate is what flows there.

## Wire change

The descriptor RPC is unreleased on this branch, but stay additive anyway:

```proto
// One dialable address for the QUIC endpoint. Candidates are ordered by
// the manager's preference; the client keeps the first that handshakes.
message QuicEndpointCandidate {
  string host = 1;
  int32 port = 2;
}
// In QuicTunnelEndpoint (next free field number is 9):
repeated QuicEndpointCandidate candidates = 9;
```

Keep filling `host`/`port` (fields 2/3) with the first candidate so any
intermediate build of the client keeps working. `make protoc`; `protolint`
must stay green.

## Implementation steps

1. **Discovery component** — new file
   `cmd/traffic/cmd/manager/quictunnel/discovery.go`:
   `type Discovery struct` with `Start(ctx, client kubernetes.Interface,
   namespace, serviceName string)` and `Candidates() []Candidate` returning an
   atomic snapshot. Behavior:
   * Watch (get + watch with backoff; an informer is fine if the manager
     already has a factory for core/v1 Services — check how the manager
     watches Services elsewhere before adding a new mechanism) the forwarder
     Service.
   * `type: LoadBalancer` — for every `status.loadBalancer.ingress[]` entry
     emit `{ip|hostname, spec.ports[0].port}`. No ingress assigned yet means
     no candidates (endpoint simply not advertised yet; clients pick it up on
     a later connect).
   * `type: NodePort` — port is `spec.ports[0].nodePort`; addresses come from
     listing Nodes, preferring `ExternalIP` addresses and falling back to
     `InternalIP`. Node access may be absent in namespace-scoped installs:
     catch the Forbidden error, log once at info, and return no candidates
     (the documented degradation: NodePort + namespace-scoped needs the
     explicit override).
   * `type: ClusterIP` (or `service.create=false`) — no candidates.
   * Cap the list (say 8) and keep ordering deterministic (LoadBalancer
     ingress order; nodes sorted by name) so reconnects are stable.
2. **Gate change** in `service.go`: `GetQuicTunnelEndpoint` returns
   `Enabled: true` when `s.quicCA != nil` AND (env override set OR discovery
   has candidates). With the override set, candidates =
   `[{TunnelQuicExternalHost, TunnelQuicExternalPort or TunnelQuicPort}]` and
   discovery is not even started. Start the Discovery in `NewService` next to
   the existing `if env.TunnelQuicPort != 0` block (service.go ~line 119).
   The manager needs the forwarder Service's name and namespace: pass them via
   two new env vars (`TUNNEL_QUIC_SERVICE_NAME`, set from the chart where
   quicforwarder.yaml names the Service; namespace is the manager's own).
3. **RBAC**: confirm the manager's Role/ClusterRole covers `services`
   get/list/watch in its namespace and `nodes` get/list in cluster-scoped
   installs (`charts/telepresence-oss/templates/` RBAC files). Add the minimal
   missing verbs; do NOT add nodes access to the namespace-scoped role.
4. **Client concurrent probe** in `pkg/client/rootd/quic.go`:
   * Build the candidate list: `ep.Candidates`, falling back to
     `[{ep.Host, ep.Port}]` when empty (older manager on this branch).
   * Dial all candidates concurrently inside the existing single
     `quicDialTimeout` budget, staggered by ~250 ms in list order
     (happy-eyeballs style, so the preferred candidate usually wins without
     burst). First successful handshake wins; `CloseWithError(0, "")` the
     losers and cancel the rest.
   * Record the winning `host:port` in the session (it already flows to
     `setTransportStatus` for `telepresence status`); make the agentpf
     endpoint use the same winner.
5. **Chart + docs**:
   * `values.yaml`: rewrite the `externalHost` comment — it is now an
     override, not a prerequisite. Mention discovery and the namespace-scoped
     NodePort caveat.
   * `docs/howtos/quic-transport.md`: the happy path becomes exactly
     `--set quicTunnel.enabled=true`; move externalHost to a "when discovery
     cannot see your topology" subsection (NAT in front of the LB, port
     remapping, VPN-only DNS names).
   * `docs/reference/quic-transport.md`: document the discovery rules per
     Service type and the degradation matrix.
   * Remember: chart changes require `make build` before they reach a
     cluster.

## Tests

* Unit (`cmd/traffic/cmd/manager/quictunnel/discovery_test.go`): candidate
  derivation from fake Service/Node objects for all three Service types,
  LB-with-hostname, LB-unassigned, node ExternalIP preference and InternalIP
  fallback, Forbidden nodes → empty + logged once.
* `service_test.go` already has `getTestClientConn(... e.TunnelQuicPort =
  7778)` plumbing; add cases for the new gating (override set / discovery
  candidates present / neither).
* Integration (`integration_test/quic_test.go`): a kind cluster cannot assign
  LoadBalancers, so the discovery path to exercise is NodePort: install with
  `quicTunnel.enabled=true --set quicTunnel.service.type=NodePort` and **no
  externalHost**; assert the client reaches `quic (…)` transport via a
  discovered node InternalIP (kind nodes have no ExternalIP — this exercises
  the fallback branch). Keep an explicit-override test to protect the
  existing path. Both `QuicTunnel` suites must stay green.

## Acceptance criteria

* Fresh kind install with only `quicTunnel.enabled=true` +
  `service.type=NodePort`: `telepresence status` reports `quic (<nodeip>:<nodeport>)`
  with zero further configuration.
* Explicit `externalHost` still wins over discovery.
* Older-manager compatibility: a client built from this branch against a
  manager without candidates (fields empty) behaves exactly as before.
* `make check-integration TEST_SUITE='^QuicTunnel$$'` green; `protolint`,
  `golangci-lint`, `gofumpt` clean.
