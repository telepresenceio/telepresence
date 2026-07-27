# Fix #4222: stream-pair close corrupts the shared per-pod stream connection

## Root cause (established empirically)

Issue #4222 attributed the corruption to closing a stream pair. That framing
is wrong: closing a pair — data FIN, error FIN, `RemoveStreams` — is clean.
Reproduction against a live kind cluster (real API server → kubelet →
containerd chain) shows a pair close under a live sibling pair is harmless on
both transports. The actual defect is a **write-deadline leak across streams
that share one connection**:

1. `moby/spdystream` does not implement per-stream deadlines.
   `Stream.SetWriteDeadline` sets the deadline on the **underlying network
   connection**, shared by every stream pair on that pod connection (the
   spdystream source marks this with
   `// TODO set per stream values instead of connection-wide`).
2. Our `portConn.SetWriteDeadline` (`pkg/client/portforward/streamconn.go`)
   forwards deadline calls to the data stream whenever it implements
   `net.Conn`.
3. Whether it implements `net.Conn` depends on the transport:
   - WebSocket tunneling (the default): streams are native
     `*spdystream.Stream` → the deadline propagates connection-wide.
   - Direct SPDY: client-go's `streamingStreamAdapter` (from
     `NewDialerForStreaming`) does *not* implement `net.Conn` → deadline
     calls are already silent no-ops.
4. `crypto/tls.Conn.Close()` (`closeNotify`) always ends with
   `SetWriteDeadline(time.Now())` — a deadline in the past, by design, so
   that subsequent writes on the TLS conn fail. Run over a `portConn`, that
   poison lands on the shared connection: **every write by every other
   stream pair fails with `i/o timeout` from then on**. The gRPC channel's
   next frame dies, the transport redials onto the same poisoned connection,
   and the RPC hangs until its deadline — the exact 60-second failure that
   led to #4222. The x509 auth handshake (a TLS client over a port-forward
   stream) is a deterministic trigger; any future TLS-over-stream use would
   be too.

Evidence (probe tests, kept as part of this change where CI-viable):

- In-process harness — production `podDialer` against a faithful
  kubelet/containerd server chain — shows pair close is clean, and the
  original x509 sequence is clean, over plain byte exchanges.
- Live-cluster probe with a socat pod: pair close under a live pair is clean
  on both transports; adding only the closeNotify deadline sequence breaks
  the WebSocket transport (sibling pair write: `i/o timeout`) while direct
  SPDY stays healthy (adapter no-op).
- Live-cluster probe against a real x509-enabled traffic-manager: real gRPC
  channel + real TLS auth exchange reproduces the original hang on
  WebSocket, passes on SPDY, and pinpoints `tls.Conn.Close()` as the
  trigger.

## Fix

Two parts, both in `portConn`:

1. Make `SetDeadline`, `SetReadDeadline`, and `SetWriteDeadline` no-ops
   (return nil). Per-stream deadlines are unimplementable on top of
   spdystream's connection-wide semantics, and forwarding them turns one
   consumer's deadline into whole-connection corruption. This makes the two
   transports consistent: the direct-SPDY path has always no-opped these
   calls, and every existing consumer (gRPC transports manage their own
   timers) already tolerates that. Callers that need to bound an operation
   must use a context/watchdog that closes the conn — which is what the
   x509 token source already does, for exactly this reason.

2. Call `Reset()` on both streams in `portConn.Close`, after the regular
   `Close()`. A local stream close in spdystream does not unblock local
   reads pending on that stream, so `portConn` violated the `net.Conn`
   contract that Close unblocks pending Reads. Consumers depended on that
   contract through the deadline hole: gRPC's transport teardown interrupts
   its blocked reader by setting a past *read* deadline on the conn — the
   same connection-wide poison as the crypto/tls case, and with part 1
   alone, teardown of the last conn on a WebSocket tunnel stalls ~10s (the
   tunnel's ping period) waiting for the remote to wind down. `Reset` after
   a sent FIN closes the local channels and removes the streams from the
   spdystream connection's stream map without putting anything on the wire.
   It also ends the error-stream reader goroutine (previously leaked until
   the remote closed its side) and lets the connection-level shutdown
   proceed without waiting on stream-map stragglers.

`LocalAddr`/`RemoteAddr` keep their current best-effort delegation; they are
informational and harmless.

Not chosen:

- Fixing spdystream upstream (real per-stream deadlines): right long-term,
  wrong vehicle — it is vendored transitively via k8s staging modules, and
  the fix must work with every already-shipped version. An upstream issue is
  worth filing separately.
- Emulating read deadlines per-stream in `portConn`: no consumer needs it.

Measured teardown of the manager gRPC channel over the WebSocket tunnel:
~1s before the change (via the read-deadline poison), ~10s with part 1
alone, ~0.4ms with both parts.

## Tests

- Keep the in-process reproduction harness (kubelet handler + containerd
  copy-loop semantics + production `podDialer`) as a regression test:
  - pair close under a live pair leaves the sibling and future dials healthy;
  - the crypto/tls closeNotify sequence (write, past write-deadline, close)
    through one pair leaves the sibling pair healthy. To be *red before the
    fix*, this test dials through the k8s.io/streaming native SPDY client
    (streams implement `net.Conn`, like the WebSocket path) rather than the
    client-go adapter.
- The live-cluster probes (socat pod, traffic-manager) are investigation
  scaffolding: env-gated (`PROBE_KUBECONFIG`), skipped in CI. Dropped before
  merge; the in-process regression covers the mechanism.
- Manual verification: the traffic-manager live probe (WebSocket) passes
  with the fix applied.

## Follow-ups (separate changes)

- Update #4222 with the root cause; close it when this merges.
- x509 PR #4223: `OneShotDialer` was added to work around this defect. With
  the fix in, the handshake can safely share the pod connection again —
  evaluate dropping `OneShotDialer` on that branch once this lands.
- Optional upstream issue against moby/spdystream (connection-wide
  deadlines) and k8s.io/streaming (adapter/native `net.Conn` inconsistency).

## Release

Client-only, small, safe: candidate for 2.31.1 alongside the x509 work.
