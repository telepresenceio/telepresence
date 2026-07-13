# Telepresence QUIC performance experiments

Standalone experiments that quantify what the opt-in QUIC tunnel transport
(`docs/plans/quic-transport/design.md`) buys over the default port-forwarded
gRPC transport. They are **not** run by `make check-integration` or
`go test ./...` — every experiment file is behind the `perf` build tag.

```
make perf                                  # runs the whole suite
go test -tags perf -run Experiment1 -v -timeout 30m ./perf/...
```

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

Drives `exp1Streams` (50) concurrent downloads of an 8 MiB payload through the
tunnel at several client-side packet-loss levels (0, 1, 3 %), once with the
manager installed QUIC-enabled and once gRPC-only, and compares the per-stream
completion-time tail. The payload is a static file served by nginx
(`testdata/payload.yaml`) reached by cluster DNS name, so the traffic rides the
VPN (manager tunnel) — the shared transport whose behavior we are measuring.
Each arm runs a discarded warm-up window first (session establishment, nginx
page cache, and congestion-control ramp would otherwise land in the first
measured window and make it incomparable to the later ones).

**Assertion:** at the highest loss level, the gRPC p99 must be at least
`exp1MinP99Ratio` (2×) the QUIC p99. The threshold is a *ratio*, not an
absolute latency, so it is portable across clusters and networks. The full
p50/p95/p99 table is written to `perf/results/experiment1.csv` either way.

## Prerequisites

- A Kubernetes cluster and a `telepresence` binary built from this tree
  (`make build` for the client, `make tel2-image` + load for a local image).
- A **QUIC-reachable UDP path** to the forwarder's Service, and its address in
  `PERF_QUIC_EXTERNAL_HOST` / `PERF_QUIC_NODEPORT`. Without this the quic arm is
  skipped. On kind: the node's InternalIP + the pinned NodePort.
- **Passwordless `sudo tc`** on the runner, and `PERF_NETEM_IFACE` set to the
  host interface that carries tunnel-transport packets *toward the cluster*
  (e.g. the docker bridge for kind, or your NIC/VPN interface for a remote
  cluster). This must NOT be the telepresence VIF (`tel*`), which carries
  decapsulated traffic — impairing it would not exercise the transport. Without
  `PERF_NETEM_IFACE` the experiment runs a clean-network baseline only (recorded,
  not asserted).

> **Why a remote cluster gives the real number.** On kind the "network" between
> client and cluster is loopback-ish docker networking, so injected loss is
> synthetic and the apiserver isn't a realistic bottleneck. A remote cluster
> (e.g. GKE) with the client on an ordinary developer network is where these
> results are meaningful. The harness is cluster-agnostic; point it at either.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `PERF_TELEPRESENCE` | `build-output/bin/telepresence` | client binary under test |
| `PERF_QUIC_EXTERNAL_HOST` | (unset → quic arm skipped) | forwarder's reachable host/IP |
| `PERF_QUIC_NODEPORT` | `30777` | forwarder Service NodePort |
| `PERF_NETEM_IFACE` | (unset → baseline only) | host interface for `tc netem` |
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
- Loss is injected on the client's **egress** only. Download payloads ride the
  *ingress* path, so egress loss cannot produce head-of-line blocking on the
  measured transfers at all — it only degrades ACKs and requests. Until loss is
  injected on ingress too (an `ifb` device) or the workload is an upload, the
  head-of-line ratio assertion is expected to fail; the numbers still document
  each transport's stability under ACK-path loss.
- QUIC bulk throughput is capped by the cluster nodes'
  `net.core.rmem_max`/`wmem_max` (see "Throughput and node tuning" in
  `docs/reference/quic-transport.md`). On nodes with small caps (GKE
  Container-Optimized OS: 208 KiB) the QUIC arm reflects that ceiling, not the
  transport's potential.
- Results depend on client CPU, cluster location, and the workload; treat the
  CSV as a comparison between the two arms of the *same run*, not as an absolute
  benchmark comparable across machines.
