# Follow-up: netstack buffer tuning (VIF)

Engineering note, not user documentation. Filed 2026-07-15, discovered while
investigating the QUIC datagram experiment (`perf/README.md`, "Experiment 2").
Not yet acted on — this records the finding and the *measured* way to act on it.

## What was found

The gVisor netstack in `stack.go` tunes TCP buffers but leaves UDP at defaults:

- **UDP endpoints use gVisor's 32 KiB default** send and receive buffers
  (`udp.DefaultSendBufferSize` / `udp.DefaultReceiveBufferSize`, both `32 << 10`;
  max is `udp.MaxBufferSize` = 4 MiB). `setUDPHandler` / `forwardUDP` set no
  buffer options at all, and gVisor has no UDP receive auto-tuning.
- **TCP endpoints get 1 MiB defaults, a 4 MiB max, and receive auto-tuning**
  (`setTCPHandler`: `TCPSendBufferSizeRangeOption`,
  `TCPReceiveBufferSizeRangeOption`, `TCPModerateReceiveBufferOption`).

So tunneled UDP flows run with buffers ~30x smaller than TCP and with no
auto-tuning. 32 KiB is ~25 wire-sized packets; a bursty UDP flow overflows it
and netstack drops the excess silently.

Two smaller TCP issues in the same code:

- **Copy-paste:** the receive-buffer `Default` is set to
  `tcp.DefaultSendBufferSize` (`stack.go`, in `setTCPHandler`). Harmless today
  because gVisor sets both send and receive defaults to 1 MiB, but it is a
  latent bug the moment those diverge — it should be
  `tcp.DefaultReceiveBufferSize`.
- **BDP-blind values:** the TCP min/default/max are gVisor's stock constants,
  not derived from the tunnel's bandwidth-delay product. The 4 MiB max is a hard
  ceiling that would window-limit a single flow on a very-high-BDP (fast + high
  latency) path.

## Why it matters

An app flow terminates at the VIF's netstack, so its throughput is bounded by
the smaller of its VIF socket buffer window and the tunnel's capacity. An
undersized VIF buffer window-limits the app flow *regardless of the tunnel
transport underneath* — so a well-tuned tunnel (QUIC or gRPC) goes underused.
Fixing the VIF buffers lets app traffic realize the tunnel's capacity. This is
**not** QUIC-specific; it helps both transports equally.

## What this is NOT

It is **not** the cause of experiment 2's datagram-carriage latency penalty.
That penalty was investigated here and is a tail-latency effect inherent to
carrying a reliable inner protocol over unreliable, unordered datagrams
(raising both the tunnel channel buffer to 1024 and the VIF UDP buffer to 2 MiB
left it unchanged; datagram and stream share the same median, only the datagram
tail diverges). See `perf/README.md`, "Experiment 2". Do not conflate the two.

## How to do it (measured, not guessed)

The current values are guesswork; do not replace them with new guesses.

1. Add explicit UDP send/receive buffer sizing (in `forwardUDP` via
   `ep.SocketOptions().SetSendBufferSize` / `SetReceiveBufferSize`, or a
   transport-level option in `setUDPHandler`), sized from the expected BDP up to
   gVisor's 4 MiB UDP ceiling. Since gVisor has no UDP receive auto-tuning, a
   fixed generous value is the lever.
2. Fix the TCP receive-`Default` copy-paste.
3. Revisit the 4 MiB TCP max for high-BDP WAN tunnels (raise the ceiling, or
   confirm auto-tuning reaches it).
4. **Validate with a measurement**, not intuition: a bulk-app-through-the-VIF
   throughput experiment in the `perf/` harness, before vs after, on both
   transports, at a representative RTT and bandwidth. Only commit values that
   move a measured number.
