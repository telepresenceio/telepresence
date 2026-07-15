# Netstack UDP buffers (VIF): measured, not the bottleneck

Engineering note, not user documentation. Filed and measured 2026-07-15 while
investigating the datagram-carriage experiment. Co-located with the experiment that
tested it (`vifthroughput_test.go`, `TestVIFBulkThroughput`).

## The observation

gVisor's netstack (`pkg/vif/stack.go`) tunes TCP buffers but leaves UDP endpoints at
the 32 KiB default -- send and receive; max `udp.MaxBufferSize` = 4 MiB -- with no UDP
receive auto-tuning, while TCP gets 1 MiB defaults, a 4 MiB max, and receive
auto-tuning (`setTCPHandler`). 32 KiB is ~25 wire-sized packets. The worry: this
window-limits tunneled UDP throughput.

## Measured result: it does not

`TestVIFBulkThroughput` drives a bulk raw-UDP download through the VIF (200 Mbit/s
offered, 1200-byte datagrams, 20 ms injected RTT on kind) and measures delivered
goodput/loss, before and after sizing `forwardUDP`'s UDP endpoint send+receive buffers
32 KiB -> 2 MiB, on both tunnel transports:

| transport | VIF UDP buffer | goodput     | loss  |
|-----------|----------------|-------------|-------|
| gRPC      | 32 KiB         | ~20 Mbit/s  | ~90%  |
| gRPC      | 2 MiB          | ~20 Mbit/s  | ~90%  |
| QUIC      | 32 KiB         | 173 Mbit/s  | 13.6% |
| QUIC      | 2 MiB          | 175 Mbit/s  | 12.6% |

The VIF UDP buffer never moved the number -- not under gRPC, and not under QUIC even
at ~175 Mbit/s, where a 32 KiB buffer had every chance to bind. The buffer change was
**not committed**.

## What the experiment actually found: gRPC is window-limited, QUIC is not

The real story is the transport, not the buffer. The gRPC transport rides the
Kubernetes port-forward -- rootd's one gRPC connection over a single SPDY data stream
through the apiserver. SPDY's per-stream flow-control window is ~64 KiB and k8s does
not tune it, so at 20 ms RTT the flow is window-limited to ~64 KiB / 20 ms ~= 26 Mbit/s
(matching the measured ~20). QUIC's native per-flow streams have no such cap and
deliver ~173 Mbit/s -- an **8.8x bulk-UDP throughput win at 20 ms RTT**, from the same
single-shared-SPDY-stream root cause that also causes head-of-line blocking under
loss. That is a throughput argument for QUIC in its own right, independent of this
buffer question.

The ~13% loss on the QUIC arm is upstream of the client VIF buffer (raising it 64x did
not touch it) and is most likely the netem qdisc / disabled segmentation offload at
~175 Mbit/s inside the impairment harness (`PERF_IMPAIR_NODE` disables tso/gso and adds
the delay on the node's eth0), not a telepresence buffer. It was not pinned down, but
it is demonstrably not the VIF UDP endpoint buffer. These numbers are with GSO disabled
(the harness disables it in impairNode mode), so QUIC's 173 Mbit/s is a conservative
lower bound.

## What was fixed anyway

The TCP receive-buffer `Default` copy-paste in `setTCPHandler` -- set to
`tcp.DefaultSendBufferSize`, should be `tcp.DefaultReceiveBufferSize` -- is a latent
bug (harmless today: both 1 MiB). Fixed in a separate commit, a correctness improvement
independent of any measured number. The BDP-blind 4 MiB TCP max is left as-is:
unmeasured, and TCP was never the bottleneck in these runs.

## What this is NOT

Not the cause of the datagram-carriage experiment's latency penalty (a tail-latency
effect of carrying a reliable inner protocol over unreliable datagrams; raising this
buffer to 2 MiB left it unchanged). See `perf/README.md`, "Datagram carriage".
