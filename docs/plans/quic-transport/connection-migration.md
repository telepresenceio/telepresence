# Connection migration: prove it, harden it, then advertise it

Read `README.md` in this directory first. Design context: `design.md`,
"Validate and advertise connection migration".

## Goal

Demonstrate that a client whose source address changes mid-session (Wi-Fi →
hotspot, NAT rebinding) keeps every QUIC tunnel stream alive, fix whatever
breaks, and only then claim the capability in the docs. The forwarder should
already be migration-proof **by construction**: established-flow routing keys
on server-issued connection IDs that encode the backend pod
(`pkg/quicfwd/` CID codec), never on the client 4-tuple.

## What actually happens today (trace first, then test)

Walk this chain and note where assumptions live:

1. **Forwarder, new source address**: `cmd/traffic/cmd/quicforwarder/
   forwarder.go` `runIngress` looks up flows by client source
   `netip.AddrPort` (`flow.go` `flowTable`). A migrated client is a lookup
   miss → cold path → `router.go` `Route`: a short-header packet's CID is
   decoded (`quicfwd.DecodeCID`) to the backend pod IP and
   `CreateAndForward` builds a **new** flow entry and a new backend socket.
   Expected result: the backend sees the same connection from a new
   forwarder source port and runs QUIC path validation. Verify `Route`
   really handles short-header packets this way (not only Initials) — that
   is the crux of the whole design.
2. **Backend (manager/agent listener)**: quic-go handles the peer address
   change with PATH_CHALLENGE/PATH_RESPONSE and anti-amplification limits.
   Nothing of ours sits in the way; the challenge frames traverse the new
   forwarder flow in both directions.
3. **Stale flow entry**: the old `flowTable` entry idles out via
   `sweepIdle` (`defaultIdleExpiry` 2 min). Harmless but verify no
   double-delivery weirdness if the old path briefly still carries packets
   (packets on the old flow still route to the same backend — QUIC dedups).
4. **The client side is the real risk, and it is above QUIC**: rootd reacts
   to network changes (routing table updates, VIF reconfiguration — look at
   the network-monitoring code in `pkg/client/rootd`, e.g. where the session
   reconnects on interface changes). If rootd tears down the session or the
   daemon on a network change, migration never gets a chance. Establish what
   rootd does on (a) source-IP change with default route intact, (b) default
   route flapping to another interface. Document findings in the commit
   message and, if they change what migration can deliver, in `design.md`;
   fixing an aggressive teardown may be a separate follow-up if it is
   invasive.

## The test that gates everything: NAT-rebind simulation, pure Go

No netns or root needed. In `cmd/traffic/cmd/quicforwarder` tests, build a
small UDP NAT-rebind proxy:

* `natProxy` listens on a UDP socket (the "client-facing" side is the test's
  QUIC client dialing it), forwards every datagram to the forwarder from an
  *outbound* socket, and relays returns back. After N forwarded datagrams
  (or on a `Flip()` call), it **replaces the outbound socket** with a fresh
  one (new source port) — exactly what NAT rebinding or an interface change
  looks like to the forwarder.
* Test: client ↔ natProxy ↔ forwarder ↔ bench QUIC server (reuse
  `bench_test.go`'s `startBenchQuicServer`/`startBenchForwarder`
  scaffolding, which already runs the production forwarder and CID
  generator). Start an 8 MiB payload transfer, `Flip()` at ~50%, assert the
  transfer completes without stream error and (via the forwarder's metrics
  or flow-table inspection) that a second flow entry was created for the new
  source.
* Variants: flip during the handshake (expected: connection fails cleanly —
  Initials route by SNI + handshake cache keyed by source; that is
  acceptable and worth asserting so the limitation is known and documented);
  flip twice; flip and then idle past keep-alive to confirm keep-alives
  ride the new path.

## Client-path integration check (best effort)

A full interface-roam test needs an environment the integration harness does
not provide. A pragmatic middle step: run `telepresence connect` inside a
network namespace or container whose SNAT rule can be flipped mid-transfer
(iptables MASQUERADE with `--to-ports` change). Treat this as a manual
verification script under `perf/` or a procedure documented in the commit
message, not a CI test. The Go-level NAT-rebind test above is the merge gate.

## Hardening that may fall out

* If `Route` does not yet handle short-header packets for unknown sources,
  that is the bug to fix (routing by `DecodeCID`, allowlist-checked backend,
  then `CreateAndForward` — mirror the established-flow write path).
* Rate-limit the cold path: a migration storm (mobile clients) must not turn
  the single-threaded `runIngress` into a dial-per-packet loop; ensure
  `CreateAndForward`'s existing dedup (`flows.LoadOrCompute`-style check)
  covers concurrent packets from the same new source.
* quic-go tuning: check `MaxIdleTimeout`(1 min)/keep-alive(15 s) interact
  sanely with a path change (validation happens within the idle budget).

## Docs (only after the tests pass)

* `docs/reference/quic-transport.md`: new section on migration — what
  survives (established connections through the forwarder), what does not
  (mid-handshake changes), and that the port-forward fallback requires a
  reconnect by nature.
* `design.md`: mark the migration item measured/decided.

## Acceptance criteria

* NAT-rebind Go test in `cmd/traffic/cmd/quicforwarder` passing in CI
  (no root, no netns).
* A written trace (commit message, plus `design.md` if claims change) of
  rootd's behavior on real interface changes, with follow-up issues filed if
  teardown preempts migration.
* No docs claim beyond what the tests demonstrate.
