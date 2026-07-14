# Relay hardening: GRO, buffer observability, and the QUIC re-probe

Read `README.md` in this directory first. Design context: `design.md`,
"Relay hardening left on the table" and the phase-4 hardening note about
recovering QUIC after a manager CA rotation.

Three independent items; they can be three commits on one branch. The
re-probe is the important one (it is also the prerequisite for
`session-resumption.md`).

## 1. Re-probe: a session that fell back to port-forward retries QUIC

### Current behavior (verify)

* Manager path: `pkg/client/rootd/quic.go` `quicFallbackProvider` — the
  first `Tunnel()` error latches `dead`, closes the QUIC conn, and every
  later call uses the gRPC fallback **forever**. `startQuicTunnel` runs once
  per session.
* Agent path: `pkg/client/agentpf/quic.go` — per-agent `quicDead` latch;
  `closeQuicConn` mentions a refresh path that "gives a dead QUIC path a
  fresh chance following an AgentPodInfo update" — read `refresh` and
  confirm when that actually fires (pod update, not timer).
* The documented failure (integration test
  `Test_ZManagerOutageAttachmentSurvival` in
  `integration_test/quic_test.go`): after a manager restart, the new manager
  process has a new CA; the client's cert no longer verifies; QUIC dies;
  fallback latches; QUIC never returns until `telepresence quit`/`connect`.

### Design

* Replace the permanent latch with a re-probe loop: when the provider trips
  to fallback, start (once) a goroutine that every `quicReprobeInterval`
  (60 s; constant next to `quicDialTimeout`) calls the same logic as
  `startQuicTunnel`: `GetQuicTunnelEndpoint` over the (healthy, gRPC)
  manager connection → fresh CA bundle + fresh session-scoped client cert →
  dial. On success: atomically swap the provider back to QUIC
  (`quicTunnelProvider.Store`), reset transport status
  (`setTransportStatus(TransportQUIC, addr)`), emit the usage report with a
  `reason: "reprobe"` key, and stop the loop until the next trip.
* Key detail that makes CA rotation work: the re-probe must call
  `GetQuicTunnelEndpoint` **again** (fresh `ca_pem`/client cert from the new
  manager process), not re-dial with the cached TLS config. The RPC is
  session-scoped and the userd/rootd session survives manager restarts by
  reconnecting — confirm the manager conn heals (rootd session.go handles
  reconnect; see the comment near "managerClient will reconnect
  automatically").
* In-flight semantics stay per-stream: existing fallback streams are never
  migrated; only new `Tunnel()` calls use the recovered QUIC path (matches
  the design doc's "never migrate live ones" lean).
* Agent path: on re-probe success of the *manager* endpoint, also clear
  per-agent `quicDead` latches (new dials re-attempt QUIC); additionally
  re-fetch happens naturally per AgentPodInfo update — verify and, if the
  latch survives pod updates, clear it there too.

### Tests

* Unit: `quicFallbackProvider` gets a fake prober; trip it, advance the
  probe, assert provider swaps back and `onFallback`/recovery callbacks fire
  exactly once each per transition.
* Integration: **upgrade `Test_ZManagerOutageAttachmentSurvival`** — after
  the manager comes back, assert `telepresence status` returns to
  `quic (...)` within ~2× reprobe interval (bound the wait; the suite
  helpers `requireQuicTransportWithin` exist). Remove the "documented
  limitation" comment. Keep the Z-prefix (runs last; it disrupts the
  manager).

## 2. Receive-side GRO on the forwarder

* Mirror of the existing GSO write path. Files:
  `cmd/traffic/cmd/quicforwarder/gso_linux.go` (write side, `gsoSupported`
  honoring `QUIC_GO_DISABLE_GSO`) and `batchio.go` (read batches).
* Enable `unix.UDP_GRO` on the front socket and every backend socket
  (`sockbuf.go`'s `raiseSocketBuffers` call sites are exactly the right
  hook points — add a `enableGRO(conn)` beside them, probed like
  `gsoSupported`, gated by the same `QUIC_GO_DISABLE_GSO` env for
  experiment symmetry).
* Reading: with GRO on, one `ReadBatch` message may carry a coalesced
  super-datagram plus a control message (`UDP_GRO` cmsg holding the segment
  size). `runIngress` (forwarder.go) and the backend-side reader (find the
  per-flow backend→client pump in flow.go) must split payloads into
  wire-sized datagrams before routing/forwarding: extend `batchio.go` with
  `splitGRO(msg, oob) [][]byte`. **Ordering guarantee**: `runIngress` is
  single-goroutine by contract (handshake cache ordering); splitting in
  place preserves it.
* Do not forward coalesced buffers onward as-is: the write path may GSO
  them again, but each logical datagram must remain an individual QUIC
  packet boundary — split first, then let the existing `writeMsgsBatch`
  re-coalesce.
* Benchmarks: `bench_test.go` already decomposes the stack. Measure
  before/after (`BenchmarkThroughputQuicForwarded*`); the previous
  batching+GSO work took the forwarded path 187 → ~495 MB/s; set the new
  gate ~10% below whatever GRO measures (do not guess a number in advance).
  Run with `-benchtime 3x` minimum and on an idle machine.

## 3. Buffer observability

* The forwarder already logs granted socket buffer sizes at `Serve` start
  (`forwarder.go`, `frontRcvBuf`/`frontSndBuf`) and warns nothing further.
  Add the granted sizes and the GRO/GSO enablement to the periodic
  `metrics.LogSnapshot` line (metrics.go) so they appear in long-lived logs,
  not just startup.
* Optional (only if cheap): the manager's `GetQuicTunnelEndpoint` response
  or the client usage report could carry a "relay constrained" hint, but the
  forwarder has no channel to the manager today (it only consumes
  `WatchQuicBackends`). Do NOT build a new control channel for this; if a
  field cannot ride an existing message, leave observability at the log
  level and note it in `docs/reference/quic-transport.md` ("Throughput and
  node tuning" already tells operators which log lines to read).

## Acceptance criteria

* CA-rotation test: manager restart → transport returns to `quic` without
  reconnect; suite green.
* GRO: measured improvement recorded in the commit message (or a measured
  "no gain" and the code dropped — GRO gains depend on kernel/NIC; a
  negative result is acceptable, silence is not).
* Lint (`golangci-lint`, `gofumpt`), unit tests, and both `QuicTunnel`
  integration suites green per `README.md` workflow.
