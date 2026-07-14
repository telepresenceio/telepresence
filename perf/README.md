# Telepresence QUIC performance experiments

Standalone experiments that quantify what the opt-in QUIC tunnel transport
(`docs/reference/quic-transport-architecture.md`) buys over the default port-forwarded
gRPC transport. They are **not** run by `make check-integration` or
`go test ./...` — every experiment file is behind the `perf` build tag.

```
# kind, head-of-line result (ingress loss on the data path):
PERF_KUBE_CONTEXT=kind-dev PERF_QUIC_EXTERNAL_HOST=<node-ip> \
  PERF_IMPAIR_NODE=<kind-node-container> \
  go test -tags perf -run Experiment1 -v -timeout 30m ./perf/...
```

`PERF_KUBE_CONTEXT` is required (see Prerequisites). `make perf` forwards the
same environment.

## What these prove (and don't)

QUIC's advantage here is **not** raw throughput on a quiet link — on an
unloaded network the two transports look alike. The wins are specific:

- **No head-of-line blocking under loss.** gRPC multiplexes every tunneled flow
  onto one HTTP/2 connection, so one lost packet stalls all of them; QUIC
  streams are retransmitted independently. **This is experiment 1.**
- **No apiserver bottleneck under concurrency** (future experiment 2).
- **Survives the client changing networks** (future experiment 3).

So the experiments deliberately induce the adverse condition (packet loss,
concurrency) rather than measuring a best case.

## Experiment 1: head-of-line blocking under loss

Runs `exp1Workers` (10) concurrent workers for `exp1WindowDur` (30 s) per
(transport, loss) window, each issuing sequential **small** requests (an 8 KiB
Range read of the payload object, `exp1ThinkTime` = 300 ms apart) over its own
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
2. The offered load (`exp1Workers` × `exp1RequestBytes` / `exp1ThinkTime`)
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
at least `exp1MinP95Ratio` (1.4×) the QUIC p95, and only in `PERF_IMPAIR_NODE`
mode (data-path loss). The threshold is a *ratio*, not an absolute latency, so
it is portable across clusters and networks; p95 rather than p99 because p99
rests on a handful of samples at these window sizes. Measured on kind: 1.75×
at 3 % loss and 20 ms RTT (1.8× at 1 %), 1.62× at 3 % and 80 ms RTT (with
think time scaled ×5 to keep the offered load below the RTT-reduced Mathis
capacity — see the knob comments). At both RTTs the QUIC *median* stays at
the clean baseline through 3 % loss while the gRPC median degrades. The full
p50/p95/p99 table is written to `perf/results/experiment1.csv` either way.

**A second effect observed at 80 ms RTT** (worth its own experiment): with
sparse traffic (1.5 s think time), the gRPC transport's clean-network median
is 2×RTT versus QUIC's 1×RTT — every request after a pause pays an extra
round trip. With 300 ms think time both transports sit at 1×RTT. That is the
signature of kernel TCP's congestion-window collapse after idle
(`tcp_slow_start_after_idle`, RFC 2861, default-on in Linux and not tunable
on managed nodes), which quic-go does not do. Interactive, pause-heavy
traffic — the typical dev-loop pattern — therefore pays +1 RTT per
interaction on the shared TCP transport at WAN RTTs.

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
