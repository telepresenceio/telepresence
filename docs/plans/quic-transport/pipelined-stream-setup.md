# Pipelined stream setup (verification-gated)

Read `README.md` in this directory first. Design context: `design.md`,
"Pipelined stream setup".

## Goal — and the gate in front of it

`NewClientStream` (`pkg/tunnel/client_stream.go`) sends `streamInfo` and then
**blocks** waiting for `streamOK` before returning. The improvement removes
that wait so a new flow's setup costs one tunnel round trip (the semantically
unavoidable `DialOK`) instead of two.

**However: this plan begins with a verification step that can kill it.** The
server side (`state.Tunnel` on the manager) may already start dialing the
destination the moment `streamInfo` arrives, in which case the client's wait
for `streamOK` overlaps the server's dial and removing it saves nothing
observable: the local application's `connect()` completes only on `DialOK`
regardless (see the state-machine comments on `DialOK` in
`pkg/tunnel/message.go`). Do not skip the verification and do not implement
the optimization if verification shows full overlap. A finding of "already
overlapped, no win" is a valid outcome — record it in `design.md` (flip the
improvement entry to a decided/struck state) and stop.

## Step 0: verify the cost model

1. Read the manager-side flow: `tunnel.NewServerStream`
   (`pkg/tunnel/server_stream.go`) and the code path from
   `state.Tunnel` to the dialer. Establish precisely: does the destination
   dial start (a) on receipt of `streamInfo`, before `streamOK` is sent, or
   (b) only after some later client message?
2. Measure: on a kind cluster with `PERF_NETEM_DELAY`-style added RTT (see
   `perf/README.md`; 80 ms makes the round trips legible), time
   new-TCP-connection establishment through the tunnel (e.g. repeated
   `curl --no-keepalive` TTFB against the perf payload, or a small Go probe
   that times `net.Dial` + first byte). Compare against the theoretical
   1×RTT-plus-dial floor. If establishment is already ~1 RTT + dial over the
   QUIC transport, stop (record the negative result).
3. Only proceed if there is a demonstrated extra round trip.

## Implementation (only after step 0 shows a win)

* The `streamOK` wait exists to learn the peer stream version
  (`s.peerVersion`, set in `NewClientStream` from the `streamOK` payload)
  before framing further messages. That is a per-session property, not
  per-stream: every tunnel stream of a session terminates in the same
  manager (or agent) process.
* Change `NewClientStream` to send `streamInfo` and return immediately,
  with `peerVersion` in a "pending" state. In `stream.Receive`
  (`pkg/tunnel/stream.go`), on a `streamOK` when pending: record the version
  and continue receiving (do not surface it to the caller). All messages the
  client sends before learning the version must be version-independent —
  audit what actually differs by `peerVersion` today (grep its uses); if the
  current code sends nothing version-dependent before the first data
  message, pipelining is safe. If something does depend on it, gate the
  optimization on a session-level version fetched once (the session already
  knows the manager's semver from `VersionInfo2`; map it to the tunnel
  version).
* Failure paths: a server that rejects the stream (handshake failure,
  session mismatch — see `quictunnel/listener.go` `handleStream` error
  paths) cancels the stream; the client's pipelined messages die with a
  stream reset, which the existing error handling on `Send`/`Receive`
  already surfaces. Verify `NewClientStream`'s current three error paths
  (`client_stream.go:22-34`) have equivalents in the new shape.
* Both transports benefit; there is nothing QUIC-specific in the change.
  Keep it in `pkg/tunnel` only.

## Tests

* `pkg/tunnel` unit tests: existing stream tests must pass; add one that
  delays `streamOK` and asserts data flows correctly once it arrives, and
  one where the server rejects after `streamInfo` (client sees the error on
  first Receive/Send, not a hang).
* Version-compat test: pipelined client against a server stream speaking the
  oldest supported tunnel version (see how `peerVersion` consumers branch).
* Integration: full `QuicTunnel` suites plus a standard (gRPC transport)
  suite touching intercepts, since this changes the shared protocol.
* Re-run the step-0 measurement and record before/after in the commit
  message.

## Acceptance criteria

* Either: a recorded negative result in `design.md` and no code change; or:
  measured reduction of new-connection establishment by ~1×RTT at emulated
  WAN RTT, all suites green, no protocol change visible to old peers beyond
  reordered-but-valid message flow.
