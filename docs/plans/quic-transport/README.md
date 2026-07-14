# QUIC transport: plan index and shared handoff context

This directory contains the QUIC tunnel transport design (`design.md`) and one
implementation plan per remaining work item. Each plan is self-contained enough
to hand to an agent that has this repository but not the conversation history
behind it. Read this file first; it holds the context every plan assumes.

## Plans

| File | Work item | Depends on |
|------|-----------|------------|
| `phase8-pr-assembly.md` | Final verification, docs pass, PR creation | **everything above (all complete)** |

## How the work lands

**Every plan is implemented as commits directly on `thallgren/quic`.** There
are no per-plan branches and no per-plan PRs: one PR ships the whole QUIC
transport, and it is assembled only when everything else is complete
(`phase8-pr-assembly.md`). Consequences for anyone executing a plan:

* Findings, measurements, and negative results that a per-plan PR
  description would have carried go into the **commit message** and — when
  they change the design's claims — into `design.md`.
* Keep commits logical and individually buildable; the branch history *is*
  the review narrative until phase 8.
* Do not push and do not touch draft PR #4204; phase 8 owns both.
* A work item's plan file stays in this directory until the orchestrator's
  review of that work item completes; the reviewer then deletes it in the
  review-acceptance commit (owner's instruction, superseding the "concluding
  commit deletes its own plan" convention for this branch). Phase 8's final
  commit deletes whatever remains (`design.md`, this README, and
  `phase8-pr-assembly.md` itself) — only after its own review.

## Branch and state

Work happens on `thallgren/quic`, based on `thallgren/v2.30.0`. The branch is
intentionally **not pushed** beyond its first commit (draft PR #4204 holds only
the design doc); do not push without being asked to. Phases 1-6 of `design.md`
are implemented and their integration suites are green; the perf findings that
motivated most of these plans are recorded in `design.md` under "What
measurement taught us".

## Where the QUIC code lives

* `pkg/tunnel/quic.go` — TunnelMessage framing over QUIC streams (4-byte
  length prefix, `maxFrameSize` 4 MiB), `quicStream`/`quicClientStream`,
  `NewQuicProvider` (one `OpenStreamSync` per tunneled flow), `QuicALPN`,
  `QuicMaxIncomingStreams`.
* `pkg/tunnel/stream.go`, `client_stream.go`, `server_stream.go`,
  `message.go`, `dialer.go` — the transport-agnostic tunnel protocol
  (streamInfo/streamOK/closeSend/DialOK/DialReject/Disconnect/KeepAlive).
  `stream.Receive` maps a closeSend message to `net.ErrClosed` and calls the
  optional `RecvCloser` so QUIC receive directions terminate.
* `pkg/client/rootd/quic.go` — client-side dial (`startQuicTunnel`,
  `GetQuicTunnelEndpoint`, `quicTLSConfig`, `quicFallbackProvider` which is
  currently **permanently** dead after the first failure).
* `pkg/client/agentpf/quic.go` — per-agent QUIC dialer with port-forward
  fallback (`dialAgentQUIC`, `quicStreamConn`, per-agent `quicDead`).
* `cmd/traffic/cmd/manager/quictunnel/` — manager CA (`ca.go`) and listener
  (`listener.go`; `handleStream` must fully terminate streams — see the defer).
  Manager wiring: `cmd/traffic/cmd/manager/service.go` (`TunnelQuicPort` etc.
  from `managerutil.Env`, `GetQuicTunnelEndpoint` around line 1123).
* `cmd/traffic/cmd/agent/quicserver/` — agent QUIC listener serving the
  agent's `grpc.Server` over QUIC streams via a `net.Listener` adapter.
* `cmd/traffic/cmd/quicforwarder/` — the stateless packet forwarder
  (`forwarder.go` single-goroutine `runIngress`, `flow.go` flow table keyed by
  client source `netip.AddrPort`, `router.go` SNI + connection-ID routing,
  `batchio.go`/`gso_linux.go` batched I/O + GSO honoring
  `QUIC_GO_DISABLE_GSO`, `sockbuf*.go` socket buffers, `bench_test.go`
  throughput decomposition benchmarks).
* `pkg/quicfwd/` — CID codec (`NewCIDGenerator`, `DecodeCID`), Initial-packet
  parsing and SNI extraction, `ManagerSNI`/`AgentSNI`.
* `rpc/manager/manager.proto` — `QuicTunnelEndpoint` (fields 1-8),
  `QuicBackend`, `AgentPodInfo.quic_sni = 9`, `AgentInfo.quic_port = 16`,
  RPCs `GetQuicTunnelEndpoint`, `GetQuicAgentCert`, `WatchQuicBackends`.
* `charts/telepresence-oss/` — `values.yaml` `quicTunnel` block (port 7778,
  agentPort 7787, `service.type: LoadBalancer` default, `forwarder` block),
  `templates/deployment.yaml` (TUNNEL_QUIC_* env), `templates/quicforwarder.yaml`.
* `integration_test/quic_test.go` — `QuicTunnel`/`QuicTunnelDisabled` suites.
* `perf/` — the experiment harness (build tag `perf`); `perf/README.md`
  documents the hard-won methodology. `docs/howtos/quic-transport.md` and
  `docs/reference/quic-transport.md` are the user docs (Diátaxis: howto vs
  reference; keep them in their lanes).

## Workflow facts that cost time when unknown

* **The Helm chart is embedded in the client binary.** Any chart change
  requires `make build` before it reaches a cluster installed via
  `telepresence helm install`; installing with a stale binary silently
  installs the old chart.
* Client binary: `make build TELEPRESENCE_VERSION=<v>`. Cluster image for a
  kind cluster: `make load-tel2-image TELEPRESENCE_VERSION=<v>
  TELEPRESENCE_REGISTRY=local` (builds and `kind load`s `local/tel2:<v>`).
  The integration harness pins version/registry in `itest.yml` (next to the
  telepresence `config.yml`); build with the version it names or the tests
  run stale code.
* Integration tests: always `make check-integration
  TEST_SUITE='^QuicTunnel$$' TEST_LOG_OUTPUT=/tmp/itest-quic.log` — note the
  **doubled `$`** (make eats one), fresh log path, and run it in the
  background; read only `grep -E '^(---|    ---) (PASS|FAIL|SKIP):' <log>`.
* Proto changes: edit the `.proto`, run `make protoc`, keep `protolint`
  green, update **both** sides of every touched RPC, and stay additive —
  never renumber or reuse field numbers. The repo has a `proto-rpc-reviewer`
  agent definition for exactly this review.
* Lint: `golangci-lint run <dirs>`; for `perf/` you MUST pass
  `--build-tags perf` or the files are silently skipped. `gofumpt` (stricter
  than gofmt) is enforced.
* The perf harness **requires `PERF_KUBE_CONTEXT`** and pins every kubectl/
  telepresence command with `--context` (a concurrent
  `gcloud container clusters get-credentials` once hijacked the ambient
  current-context onto a production cluster mid-run). Data-path loss
  injection is `PERF_IMPAIR_NODE=<kind-node-container>`; think time must
  scale with emulated RTT (see `perf/README.md`).
* Cluster-side packages (`quicforwarder`, `agentinit`, nft) are Linux-only
  by intent; don't chase darwin/windows builds of them.
* Do not claim performance wins in docs or PR text that are not backed by a
  measurement run with the methodology in `design.md`.
