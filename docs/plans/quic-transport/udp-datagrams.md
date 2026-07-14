# Unreliable datagrams for tunneled UDP (RFC 9221)

Read `README.md` in this directory first. Design context: `design.md`,
"Unreliable datagrams for tunneled UDP".

## Goal

A UDP payload tunneled between client and traffic-manager travels as an
unreliable, unordered QUIC DATAGRAM frame when it fits, and over the flow's
stream when it does not. This removes the reliable-ordered carriage that
today re-imposes head-of-line blocking and spurious retransmission inside
every UDP flow — worst when the inner protocol is itself QUIC (HTTP/3 through
the VIF).

**Scope: the client↔manager path only.** The agent path
(`cmd/traffic/cmd/agent/quicserver`) serves the agent's gRPC server over
QUIC streams, so tunnel messages there live *inside* gRPC and a datagram
path needs its own dispatch registry on the agent — sketch it in the design
doc as follow-up, do not build it in this change. The gRPC fallback
transport keeps stream carriage unchanged, always.

## Current state (verify before starting)

* Framing: `pkg/tunnel/quic.go` — `quicStream.Send/Recv` frame
  `TunnelMessage`s onto one `*quic.Stream` per flow;
  `quicProvider.Tunnel` = `OpenStreamSync` per flow.
* Flow bookkeeping: `pkg/tunnel/connid.go` (`ConnID` is a binary-layout
  string identifying proto/src/dst), `pkg/tunnel/pool.go` (handler registry
  keyed by ConnID — study how the rootd session routes an incoming message to
  a flow handler; the datagram receive loop must reuse that lookup, not
  invent a parallel one).
* Message codes: `pkg/tunnel/message.go`. Only `Normal` payload messages of
  UDP flows are datagram-eligible; `streamInfo`/`DialOK`/`Disconnect`/
  `KeepAlive`/`closeSend` keep stream delivery semantics.
* QUIC configs to touch: client `pkg/client/rootd/quic.go` (`qCfg`), manager
  `cmd/traffic/cmd/manager/quictunnel/listener.go` (`qCfg`). (Agent
  `quicserver` and `agentpf` configs are out of scope per above.)
* The forwarder needs **no change**: DATAGRAM frames ride ordinary QUIC
  packets and the forwarder routes packets by connection ID without looking
  deeper. State this in the PR description; do not add code there.

## Wire format

New file `pkg/tunnel/datagram.go`:

```go
// EncodeDatagram frames one UDP payload for QUIC datagram carriage:
// uvarint(len(ConnID)) | ConnID bytes | payload.
func EncodeDatagram(id ConnID, payload []byte) []byte
func DecodeDatagram(b []byte) (ConnID, []byte, error)
```

No version byte: support is negotiated per-connection by QUIC itself
(`quic.Config{EnableDatagrams: true}` on both ends;
`conn.ConnectionState().SupportsDatagrams` reports the peer). A peer that
never negotiated datagrams never receives one.

## Implementation steps

1. **Negotiate**: add `EnableDatagrams: true` to the two `quic.Config`s
   above. This alone changes nothing observable.
2. **Sender side, client**: where the rootd UDP flow handler currently
   `Send`s a `Normal` message on the flow's stream, add a datagram fast
   path: if the session's QUIC conn exists, `SupportsDatagrams`, and the flow
   is UDP, `conn.SendDatagram(EncodeDatagram(id, payload))`; on quic-go's
   too-large error, fall back to the stream for **that message** (do not
   latch — MTU can change). Locate the exact send site by following
   `pkg/tunnel/dialer.go`'s UDP write path; plumb the `*quic.Conn` in from
   `pkg/client/rootd/quic.go` next to where `NewQuicProvider(conn)` is
   built (a small interface `DatagramSender { SendDatagram([]byte) error;
   SupportsDatagrams() bool }` keeps `pkg/tunnel` free of quic-go types —
   check first whether pkg/tunnel already imports quic-go (it does, for the
   stream adapter), in which case use `*quic.Conn` directly and skip the
   abstraction).
3. **Receive loop, client**: one goroutine per QUIC conn:
   `for { b := conn.ReceiveDatagram(ctx); id, payload := DecodeDatagram(b);
   look up the flow handler by id (pool.go); hand the payload over as a
   Normal message }`. Unknown ConnID → drop silently and count (that is UDP
   semantics; a datagram racing flow teardown is normal). Start/stop it with
   the session; exit on conn close.
4. **Manager side, both directions**: symmetric — the
   `quictunnel.Listener` owns the `*quic.Conn` in `handleConn`; add the
   receive loop there, dispatching into the manager's flow machinery (find
   the manager-side ConnID→handler equivalent used by `state.Tunnel`
   streams), and give the manager's UDP dial-side the same send fast path.
   The `TunnelHandler` signature may need the conn (or a DatagramSender)
   passed alongside the stream; extend `quictunnel.Listen`'s handler type
   rather than reaching through globals.
5. **Ordering/teardown**: no ordering guarantees between stream-carried
   control and datagrams. `Disconnect` still arrives on the stream; late
   datagrams after teardown are dropped by the unknown-ConnID path. Document
   this in the datagram.go package comment.
6. **Observability**: count datagrams sent/received/fallback-to-stream/
   unknown-conn per session; log the totals at session end (client) and at
   the manager's existing stats cadence. Without this the feature is
   unverifiable in the field.
7. **Docs**: `docs/reference/quic-transport.md` — replace the "Tunneled UDP
   is carried over reliable QUIC streams" limitation bullet with the hybrid
   description + the agent-path exception; note the gRPC fallback still uses
   streams.

## Tests

* Unit: Encode/Decode round-trip, truncated input, oversized handling.
* `pkg/tunnel/quic_test.go`: extend the e2e test — UDP-flavored ConnID,
  assert payloads arrive via the datagram path (count them), assert the
  oversized message falls back to the stream and still arrives, assert a
  datagram for a dead ConnID is dropped without error.
* Integration (`integration_test/quic_test.go`): the existing suites include
  UDP coverage through the tunnel (verify — if not, add a UDP echo check);
  they must pass unchanged, plus one assertion that the manager log reports a
  nonzero datagram count for a UDP-exercising test.
* Perf (stretch, separate commit): an inner-QUIC experiment — HTTP/3 client
  through the VIF against an in-cluster HTTP/3 server, stream-carriage vs
  datagram-carriage arms. Design it load-first per the methodology section of
  `design.md`; expect the win to appear under data-path loss
  (`PERF_IMPAIR_NODE`), not on a clean network.

## Acceptance criteria

* With client and manager from this branch: UDP flows show nonzero datagram
  counters; oversized UDP payloads still arrive (stream fallback).
* Mixed versions (one side without `EnableDatagrams`): behavior identical to
  today, zero datagram frames on the wire.
* `QuicTunnel` suites green; `pkg/tunnel` tests green; lint clean.
* DNS through the tunnel works unchanged (it may legitimately stay on
  streams if its payloads flow through a non-UDP-flow path — do not force
  it; just verify no regression).
