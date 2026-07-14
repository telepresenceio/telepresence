# TLS session resumption for QUIC reconnects

Read `README.md` in this directory first. Design context: `design.md`,
"0-RTT session resumption".

## Goal and scope decision

Cut a round trip from QUIC reconnects by enabling TLS 1.3 session resumption
(ticket-based). **Plain resumption only — no 0-RTT early data**
(`Allow0RTT` stays false everywhere): that keeps the anti-replay analysis
trivial while still removing the expensive part of the handshake.

## Prerequisite — read this before writing code

Resumption only pays when a reconnect actually happens **within the same
telepresence session against the same manager process**, because:

* The trust material is per-manager-process (ephemeral CA minted at manager
  start) and per-session (client cert CN = session ID, verified per stream in
  `cmd/traffic/cmd/manager/quictunnel/listener.go` `handleStream`). A ticket
  resumed across a *new* telepresence session presents the old session's
  client identity; the per-stream `SessionID == cert CN` check then rejects
  every stream. Resumption across sessions is therefore useless by design —
  do not try to make it work; make sure it *fails closed* (it does, via that
  check, but add a test).
* The re-probe (`pkg/client/rootd/quic.go`'s `quicReprobeLoop`, `quicReprobeInterval`
  = 60s) is what gives this item something to resume: `startQuicTunnel` still dials
  once at session start, but a session that trips `quicFallbackProvider` to
  fallback now re-dials on that interval via `probeQuicTunnel`/`attemptReprobe`
  until it succeeds, re-fetching the endpoint descriptor (fresh CA bundle, fresh
  session-scoped client certificate) on every attempt. That re-dial is the
  reconnect this plan's `tls.ClientSessionCache` should attach to — thread it
  through both call sites (`startQuicTunnel` and the re-probe path share
  `quicTLSConfig`, so one cache instance on the session object covers both).

## Implementation

1. **Client** (`pkg/client/rootd/quic.go` `quicTLSConfig`, and
   `pkg/client/agentpf/quic.go` `ep.tlsConfig(sni)`): attach a
   `tls.ClientSessionCache` — `tls.NewLRUClientSessionCache(16)` — owned by
   the session object (rootd `session`, agentpf `client`), NOT a global:
   the cache must die with the telepresence session for the identity reasons
   above. One cache per peer identity (manager; each agent SNI) — an LRU
   keyed by `ServerName` handles this automatically since crypto/tls keys
   entries by server identity; a single per-session cache instance is
   enough.
2. **Manager/agent listeners**: crypto/tls issues session tickets by default
   for TLS 1.3 servers (rotating in-process ticket keys). Verify nothing in
   `quictunnel/listener.go` / `agent/quicserver/listener.go` disables
   tickets (`SessionTicketsDisabled`); the agent's
   `GetConfigForClient`-returned config is rebuilt per handshake — confirm
   crypto/tls still manages ticket keys sanely in that pattern (it does via
   the parent config's auto-rotation only if the parent is reused; if each
   handshake gets a brand-new `tls.Config`, ticket keys differ per handshake
   and resumption never succeeds — this is the one real bug candidate in
   this plan. Fix by caching the constructed `tls.Config` per Material in
   `SetMaterial` instead of building it in `getConfigForClient`).
3. **Verification hook**: `quic.Conn.ConnectionState().TLS.DidResume` — log
   at debug on the client after dial, and count it in the transport usage
   report (`reportTransport` in rootd/quic.go: add a `resumed` key when
   true) so field data exists.

## Tests

* `pkg/tunnel/quic_test.go` or a new `quictunnel` test: dial listener, close
  conn, re-dial with the same `ClientSessionCache`, assert
  `DidResume == true` and streams work. Repeat against the agent listener
  pattern (Material-swapped `GetConfigForClient`) — this is the test that
  catches the per-handshake-config ticket-key bug.
* Negative test: resume-attempt with a cache from session A while presenting
  session B's context — the per-stream session check must reject streams
  (assert the client sees the stream error, not silent success).
* Integration: after the re-probe work exists, the fallback-recovery test
  reconnects; assert the second connection resumed (grep the debug log or
  extend status). If that is brittle in CI, the unit-level DidResume
  assertions suffice; do not build flaky integration assertions.

## Acceptance criteria

* Re-dial within a session resumes (DidResume true) against both manager and
  agent listeners.
* No 0-RTT: `Allow0RTT` unset/false everywhere; grep proves it.
* Cross-session resumption attempt fails closed with a stream-level error.
* Lint/tests green per `README.md` workflow.
