# Telepresence data-path performance experiments

Standalone experiments that quantify how the telepresence data path behaves under
adverse conditions — the opt-in QUIC tunnel transport
(`docs/reference/quic-transport-architecture.md`) versus the default port-forwarded
gRPC transport, and the client-side VIF netstack that terminates tunneled TCP/UDP.
They are **not** run by `make check-regression` or `go test ./...` — every
experiment file is behind the `perf` build tag.

```
# kind, head-of-line result (ingress loss on the data path):
PERF_KUBE_CONTEXT=kind-dev PERF_QUIC_EXTERNAL_HOST=<node-ip> \
  PERF_IMPAIR_NODE=<kind-node-container> \
  go test -tags perf -run TestHeadOfLineBlockingUnderLoss -v -timeout 30m ./perf/...
```

`PERF_KUBE_CONTEXT` is required (see Prerequisites). `make perf` forwards the
same environment.

## What these prove (and don't)

QUIC's advantage here is **not** raw throughput on a quiet link — on an
unloaded network the two transports look alike. The wins (and non-wins) are specific:

- **No head-of-line blocking under loss.** gRPC multiplexes every tunneled flow
  onto one HTTP/2 connection, so one lost packet stalls all of them; QUIC
  streams are retransmitted independently. This is the head-of-line experiment,
  **and it holds.**
- **RFC 9221 datagram carriage removing that same effect for tunneled UDP** was
  the datagram-carriage hypothesis — **and it did NOT hold.** For an inner QUIC
  (HTTP/3) connection, datagram carriage measured no better and often worse. See
  "Datagram carriage for tunneled UDP" below.
- **VIF UDP buffer sizing** asks whether the client-side netstack's 32 KiB UDP
  endpoint buffers window-limit bulk UDP traffic. This one is about UDP **in
  general**, not QUIC — every UDP flow that terminates at the VIF shares those
  buffers, and any fix helps both tunnel transports equally. See "VIF bulk
  throughput" below.

So the experiments deliberately induce the adverse condition (packet loss,
concurrency, bandwidth-delay product) rather than measuring a best case.

## Head-of-line blocking under loss

Runs `holWorkers` (10) concurrent workers for `holWindowDur` (30 s) per
(transport, loss) window, each issuing sequential **small** requests (an 8 KiB
Range read of the payload object, `holThinkTime` = 300 ms apart) over its own
persistent connection, at several data-path loss levels (0, 1, 3 %) — once
with the manager installed QUIC-enabled and once gRPC-only — and compares the
per-request latency tail.
The payload is a static file served by nginx (`testdata/payload.yaml`) reached
by cluster DNS name, so the traffic rides the VPN (manager tunnel) — the
shared transport whose behavior we are measuring. Each arm runs a discarded
warm-up first (session establishment, nginx page cache, and
congestion-control ramp would otherwise land in the first measured window).

**Why small requests, and why a mostly idle connection.** Head-of-line
blocking is a *tail-latency* phenomenon: a lost packet stalls TCP's in-order
byte stream, so unrelated small responses behind it wait out the retransmit,
while QUIC releases each stream independently. Two conditions must hold for
that signal to be measurable:

1. A request must fit inside a congestion window; large transfers instead
   measure congestion-control throughput under loss, which is Mathis-bound
   (`~MSS/RTT x 1.22/sqrt(loss)`, under 1 MB/s at 1 % loss and 20 ms RTT) for
   *both* transports. An early bulk-download (50 × 8 MiB) variant of this
   experiment did exactly that, and both arms simply timed out.
2. The offered load (`holWorkers` × `holRequestBytes` / `holThinkTime`)
   must stay well below that Mathis capacity at the top loss level. On a
   saturated connection every request queues behind the shared congestion
   window and queuing delay drowns the blocking signal — a 50-worker,
   64 KiB, no-think-time variant moved both arms' *medians* into seconds and
   showed no difference. On a mostly idle connection the only delays are the
   recovery events themselves, which is precisely what the two transports
   handle differently.

The trace behind the premise: every VPN flow is an HTTP/2 stream on rootd's
single gRPC connection, which rides one SPDY data stream on one TCP+TLS
connection to the apiserver (`pkg/client/portforward`, `podDialers` keyed by
pod UID) — three nested layers of strictly in-order multiplexing.

**Assertion:** at the highest loss level, the gRPC p95 request latency must be
at least `holMinP95Ratio` (1.4×) the QUIC p95, and only in `PERF_IMPAIR_NODE`
mode (data-path loss). The threshold is a *ratio*, not an absolute latency, so
it is portable across clusters and networks; p95 rather than p99 because p99
rests on a handful of samples at these window sizes. Measured on kind: 1.75×
at 3 % loss and 20 ms RTT (1.8× at 1 %), 1.62× at 3 % and 80 ms RTT (with
think time scaled ×5 to keep the offered load below the RTT-reduced Mathis
capacity — see the knob comments). At both RTTs the QUIC *median* stays at
the clean baseline through 3 % loss while the gRPC median degrades. The full
p50/p95/p99 table is written to `perf/results/head-of-line.csv` either way.

**A second effect observed at 80 ms RTT** (worth its own experiment): with
sparse traffic (1.5 s think time), the gRPC transport's clean-network median
is 2×RTT versus QUIC's 1×RTT — every request after a pause pays an extra
round trip. With 300 ms think time both transports sit at 1×RTT. That is the
signature of kernel TCP's congestion-window collapse after idle
(`tcp_slow_start_after_idle`, RFC 2861, default-on in Linux and not tunable
on managed nodes), which quic-go does not do. Interactive, pause-heavy
traffic — the typical dev-loop pattern — therefore pays +1 RTT per
interaction on the shared TCP transport at WAN RTTs.

## Datagram carriage for tunneled UDP (negative result)

Runs `datagramWorkers` (10) concurrent HTTP/3 requests, **all sharing one inner
QUIC connection** (one `http3.Transport`) to an in-cluster HTTP/3 server
(`testdata/h3server`), through the tunnel, at the same loss levels — once with
the manager opting into RFC 9221 datagram carriage for tunneled UDP
(`TELEPRESENCE_QUIC_ENABLE_DATAGRAMS`) and once left at the default of stream
carriage. Both arms run over the QUIC transport; only the carriage of the single
UDP flow differs. The
hypothesis was that unreliable datagram carriage would let the inner QUIC
recover loss per-stream, while reliable stream carriage would stall the whole
inner connection on any carrier loss — so datagrams should win the tail.

**The hypothesis did not hold.** Datagram carriage was never better and was
often substantially worse. p95 request latency (ms), three runs on kind
(`dev-control-plane`, 20 ms RTT):

| loss | datagram p95 (run 1 / 2 / 3) | stream p95 (run 1 / 2 / 3) |
|------|------------------------------|----------------------------|
| 0 %  | 22 / 60 / 61                 | 22 / 23 / 22               |
| 1 %  | 62 / 84 / 84                 | 52 / 61 / 61               |
| 3 %  | 103 / 169 / 153              | 81 / 87 / 96               |

The stream arm is stable across runs; the datagram arm is not. Run 1's datagram
numbers match the stream arm (its 0 %-loss p95 is a clean 22 ms), consistent
with datagrams not having engaged that run — a silent fall-back to stream
carriage — while runs 2 and 3 show datagram carriage's true cost: a ~40 ms p95
penalty **at 0 % loss**, where there is no loss for the hypothesized mechanism
to act on at all, and roughly 1.7× the stream p95 at 3 % loss. (The stream
arm's stability across all three runs is what rules out machine-wide contention
as the cause: contention would have moved both arms.)

**Most plausible explanation (QUIC-in-QUIC):** with stream carriage the outer
reliable tunnel stream recovers a lost carrier packet over the short
client↔forwarder↔manager hop; datagram carriage instead forces the *inner* QUIC
connection to detect and recover the loss over the full end-to-end path, which
is slower — so the "unreliable is faster under loss" intuition inverts.

The **0 %-loss penalty** (datagram p95 ~60 ms vs stream ~22 ms with no loss at
all) was investigated separately. The obvious suspect — the per-flow receive
channel silently dropping datagrams on overflow, which a reliable inner QUIC
would then retransmit — was **ruled out**: with a `dropped-full` counter added
and the channel enlarged to depth 1024, a re-run showed `dropped-full` = 0 and
the penalty **unchanged** (datagram p95 still ~62 ms). So the penalty is not
overflow-driven. Its exact mechanism was not pinned down; it is consistent with
a send-side pacing cost or the inner QUIC reacting to datagram reordering, and
it is buffer-independent — the channel depth was left at its modest default and
the `dropped-full` counter retained for field visibility.

**Scope caveat.** This is a think-time request/response workload. It does *not*
exercise the sustained, buffer-filling inner-UDP regime (media, bulk HTTP/3)
that the datagram design originally targeted, where a backpressured tunnel
stream drops in bursts; that regime remains unmeasured. So this refutes the
general "datagrams remove head-of-line blocking for tunneled UDP" claim and
shows a case where they hurt, but does not prove they never help.

The experiment therefore **records** rather than asserts — there is no benefit
to gate on; the p50/p95/p99 table is written to `perf/results/datagram-carriage.csv`.
Datagram carriage is disabled by default (opt-in via
`TELEPRESENCE_QUIC_ENABLE_DATAGRAMS`), following this result.

## VIF bulk throughput (UDP buffer sizing)

Unlike the two experiments above, this one is **not about QUIC** — it is about the
client-side VIF netstack that terminates *every* tunneled UDP flow (DNS, media,
QUIC/HTTP-3, any UDP app), regardless of which tunnel transport carries it. gVisor's
UDP endpoints default to 32 KiB send/receive buffers with no receive auto-tuning,
~30× smaller than the netstack's tuned TCP buffers; the question is whether that
window-limits bulk UDP throughput once the bandwidth-delay product exceeds 32 KiB,
and whether raising the buffers helps.

`TestVIFBulkThroughput` drives a bulk UDP flow through the VIF at a representative RTT
(netem *delay*, not loss — this is window-limiting, not loss recovery) and measures
delivered throughput (MB/s), on both tunnel transports, before and after a candidate
buffer change in `pkg/vif/stack.go`. Following the note co-located here
(`netstack-tuning.md`), a buffer size is only committed if it moves a measured
number; otherwise the change is dropped and the negative result recorded, the same
way the datagram experiment above is.

**Result (kind, 20 ms RTT, 200 Mbit/s offered): the VIF UDP buffer is not the
bottleneck — the transport is.** The gRPC arm is window-limited to ~20 Mbit/s because
it rides one SPDY port-forward stream through the apiserver (~64 KiB window / 20 ms RTT
≈ 26 Mbit/s), while QUIC's per-flow streams deliver **~173 Mbit/s — an 8.8× bulk-UDP
throughput win**, the same shared-stream root cause as the head-of-line result. Sizing
the VIF UDP endpoint buffer from 32 KiB to 2 MiB moved no number on either transport
(QUIC: 13.6%→12.6% loss at ~175 Mbit/s), so the buffer change was reverted; only the
TCP receive-`Default` copy-paste fix in `pkg/vif/stack.go` was kept. The residual ~13%
QUIC loss is upstream of the client VIF (raising it 64× didn't touch it; most likely
the netem/GSO-off impairment harness at that rate). See `netstack-tuning.md`.

## Prerequisites

- A Kubernetes cluster and a `telepresence` binary built from this tree
  (`make build` for the client, `make tel2-image` + load for a local image).
- **`PERF_KUBE_CONTEXT` set to the target kubeconfig context** (e.g.
  `kind-dev`). This is required and every kubectl/telepresence command is
  pinned to it with `--context`. The experiment installs and uninstalls a
  traffic-manager, so it must never inherit the ambient `current-context`: on a
  shared machine a concurrent `gcloud container clusters get-credentials`
  rewrites `~/.kube/config` and silently repoints `current-context` — which has
  aimed a kind run at a production cluster. The harness fails fast if the
  context does not exist.
- A **QUIC-reachable UDP path** to the forwarder's Service, and its address in
  `PERF_QUIC_EXTERNAL_HOST` / `PERF_QUIC_NODEPORT`. Without this the quic arm is
  skipped. On kind: the node's InternalIP + the pinned NodePort.
- **Loss injection on the download data path** for the head-of-line result:
  `PERF_IMPAIR_NODE=<kind-node-container>` (e.g. `dev-control-plane`). The
  netem qdisc goes on `eth0` *inside* the node container — whose egress is the
  cluster→client direction, i.e. genuine ingress loss on the measured
  downloads — together with a constant `PERF_NETEM_DELAY` (default `20ms`) of
  one-way delay in *every* window, 0 % included: head-of-line blocking is a
  function of RTT, and windows are only comparable at the same RTT. The
  harness also disables tso/gso on that interface and sets
  `QUIC_GO_DISABLE_GSO=true` on the manager and forwarder so both arms lose
  wire-sized packets. Everything runs via `docker exec`; no host interface or
  `sudo` is involved.
- Alternatively `PERF_NETEM_IFACE` (host egress interface, passwordless
  `sudo tc`) injects client-**egress** loss. That only degrades ACKs and
  requests — download payloads ride ingress — so it documents transport
  stability under ACK-path loss but can never produce head-of-line blocking,
  and the ratio assertion is skipped. It must NOT be the telepresence VIF
  (`tel*`), which carries decapsulated traffic.
- With neither set, the experiment records a clean-network baseline
  (not asserted).

> **What a remote cluster adds.** The head-of-line experiment is a
> protocol-level property of RTT × loss and is best run against kind, where
> the impairment is controlled and reproducible. What kind cannot tell you:
> real ISP queueing, middlebox treatment of UDP, the apiserver as a TLS/
> concurrency bottleneck, and node `rmem_max` ceilings — those need a remote
> cluster (and beware: sustained UDP egress from a cloud VM to a single IP
> can trip the provider's DoS heuristics; keep the client inside the
> provider's network).

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `PERF_KUBE_CONTEXT` | (required) | kubeconfig context; every command is pinned to it |
| `PERF_TELEPRESENCE` | `build-output/bin/telepresence` | client binary under test |
| `PERF_QUIC_EXTERNAL_HOST` | (unset → quic arm skipped) | forwarder's reachable host/IP |
| `PERF_QUIC_NODEPORT` | `30777` | forwarder Service NodePort |
| `PERF_IMPAIR_NODE` | (unset) | kind node container; ingress loss + delay on the data path |
| `PERF_NETEM_DELAY` | `20ms` | one-way netem delay in `PERF_IMPAIR_NODE` mode |
| `PERF_NETEM_IFACE` | (unset) | host interface for egress-only `tc netem` (no HoL assertion) |
| `PERF_MANAGER_NAMESPACE` | `ambassador` | traffic-manager namespace |
| `PERF_APP_NAMESPACE` | `default` | perf workload namespace |
| `PERF_IMAGE_REGISTRY` / `PERF_IMAGE_TAG` / `PERF_IMAGE_PULL_POLICY` | `local` / client version / `Never` | manager image |
| `PERF_OUT_DIR` | `perf/results` | where CSVs are written |

## Caveats

- The experiment **reinstalls the traffic-manager twice** (once per transport
  arm) and installs/removes a `perf-payload` workload. Do not run it against a
  cluster whose traffic-manager you care about.
- The harness **disables TSO/GSO on the netem interface** for the duration of
  each loss window (restoring them afterwards). With them on, netem sits above
  the segmentation step and each "packet" it drops from a TCP flow is a
  pre-segmentation super-packet of up to 64 KB, while QUIC datagrams are
  wire-sized — the two arms would see wildly different effective loss rates.
  (quic-go's own UDP GSO still coalesces its egress, so even with this fix the
  arms are not perfectly symmetric under netem.)
- In `PERF_NETEM_IFACE` mode, loss is injected on the client's **egress** only.
  Download payloads ride the *ingress* path, so egress loss cannot produce
  head-of-line blocking on the measured transfers at all — it only degrades
  ACKs and requests, which is why the ratio assertion only runs in
  `PERF_IMPAIR_NODE` mode.
- QUIC bulk throughput is capped by the cluster nodes'
  `net.core.rmem_max`/`wmem_max` (see "Throughput and node tuning" in
  `docs/reference/quic-transport.md`). On nodes with small caps (GKE
  Container-Optimized OS: 208 KiB) the QUIC arm reflects that ceiling, not the
  transport's potential.
- Results depend on client CPU, cluster location, and the workload; treat the
  CSV as a comparison between the two arms of the *same run*, not as an absolute
  benchmark comparable across machines.
