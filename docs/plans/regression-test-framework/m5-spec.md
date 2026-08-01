# M5 spec (docker-run lifecycle, compose, state manifests, quic depth)

Goal: close the four biggest untested-feature gaps the 2026-08-01 coverage
run exposed (54.2% total): docker-run handoff (`pkg/client/cli/docker*`
35-45%), compose specs (`pkg/client/cli/docker/compose` 3.9%),
apply/delete state manifests (`pkg/client/cli/manifest` 0%), and the QUIC
forwarder (`cmd/traffic/cmd/quicforwarder` 0%). Everything except the
forwarder coverage wiring supersedes existing `integration_test/` files;
"manifest" here is the `telepresence apply/delete -f` WorkstationState
surface, not the RPC manifest guard from m4. Estimated total-coverage
yield: +7-9 points.

## Production/chart change (first, separate agent)

1. **quicforwarder GOCOVERDIR wiring.** The forwarder already runs in the
   quic area (deployed by `quicTunnel.enabled` via
   `charts/telepresence-oss/templates/quicforwarder.yaml`; outage.go even
   scales it) but is chart-rendered without the top-level
   `extraEnv`/`extraVolumes`/`extraVolumeMounts` values the manager
   Deployment gets, so its counters are lost. Render those three values
   into the quicforwarder Deployment too — zero new chart keys, and the
   framework's existing `applyCoverManagerValues` then reaches the
   forwarder with no framework change (decided at review).
   Verify with a quic-area cover run: `cmd/traffic/cmd/quicforwarder` and
   the forwarder side of `pkg/quicfwd` must go nonzero.

## Framework extensions (one agent)

1. **`cli.TP.Start(ctx, args...) (*cli.Proc, error)`** — start a CLI
   invocation without waiting; `Proc` exposes `Signal(os.Signal)`,
   `Wait(timeout) (stdout, stderr, err)`, and the harness's usual logging.
   Needed by docker/RunLifecycle's four-way teardown matrix (ctrl-C =
   SIGINT). Read `integration_test/docker_run_test.go:17` (`runDockerRun`)
   for the shape being replaced.

2. **Compose project builder** — `framework/compose` (or `rt` helper):
   compose a `docker-compose.yml` from a top-level `x-tele.connections`
   block plus per-service `x-tele.type` entries, written under
   `ArtifactDir("compose")`. Field reference:
   `pkg/client/cli/docker/compose/{toplevelextension,extension}.go` and
   `docs/reference/compose.md`. Assertions shell out to the `docker` CLI
   (`docker inspect --format`, `docker volume inspect`) — no moby SDK
   dependency in the framework.

3. **State manifest builder** — helper producing
   `kind: WorkstationState` YAML (`apiVersion: telepresence.io/v1alpha1`,
   optional `connection`, ordered `attachments[]`) under
   `ArtifactDir("state")`. Schema:
   `pkg/client/cli/manifest/state.schema.yaml`. Prefer asserting on
   `--output json` (`manifest/report.go`) over text lines.

4. **`rt.UserCacheDir()`** — expose the run's user-cache location (the
   daemons run under `build-output/rtest/home`) so the state/Handler test
   can read `handlers/<daemon-info>/<name>.json` for pid/argv assertions
   without importing client internals. Read
   `integration_test/state_manifest_test.go` (`Test_ApplyHandlerCommand`).

5. **`workloads.UDPEcho(name)`** — UDP echo Deployment+Service template
   for the quic/Datagrams port. Read `integration_test/quic_test.go`
   (`Test_AUDPEchoDatagrams`) for the image and port it replaces.

6. No new manager catalog entries: datagrams use an inline overlay on
   `managers.QuicNodePort()` adding `ExtraEnv:
   [{TELEPRESENCE_QUIC_ENABLE_DATAGRAMS, "true"}]` (the sanctioned
   inline-Spec pattern from fallback.go/restapi.go).

## Suites

### docker area additions (suites/docker/, area "docker", Requires(Docker), On("linux"))

- RunLifecycle (NeedsManager(Default)): the four-way teardown matrix from
  `docker_run_test.go:Test_DockerRun_HostDaemon` — `intercept
  --docker-run` on the host daemon, assert list + response identity
  (env-file injection proven via `TELEPRESENCE_INTERCEPT_ID` reaching the
  handler), then tear down via SIGINT / `detach` / `disconnect` / `quit`,
  asserting process exit ≤10s and intercept gone each way. Use `-i`,
  never `-t` (TTY changes signal handling).
- DockerConnRun (NeedsManager(Default)): the containerized-daemon side,
  on a named `rt.ConnDocker` connection. Ports
  `Test_DockerRunCommand` (bare `telepresence docker-run` joins the
  daemon network: `docker inspect` shows network membership + daemon DNS),
  `Test_DockerRunExternalDNS` (nslookup of an external name from the
  handed-off container), and `Test_DockerRun_VolumePresent` (telemount
  volume carries the remote mount; assert the serviceaccount path lists
  `namespace`). Supersedes the `dockerDaemonSuite` docker-run tests.
- Compose (NeedsManager(Default)): replaces the `Test_Deferred`
  placeholder. One test per `x-tele` verb, each a builder-generated
  project + `telepresence compose up -d` + polled assertion + `down`:
  connect (cluster DNS from the container), proxy, ingest (cluster env
  inherited), intercept (cluster traffic reaches the compose container),
  replace, wiretap (original keeps serving; `compose logs` shows copies),
  dns (`nslookup` of a cluster service from a replace container).
  Supersedes `compose_test.go:Test_Compose{DNS,Connect,Proxy,Ingest,
  Intercept,Replace,Wiretap}`.
- ComposeLifecycle (NeedsManager(Default), WithLabels(Slow)): teardown
  semantics — named volume survives `compose down`, removed by `down -v`
  (`docker volume inspect`); and the three-connection project's default
  network subnet does not overlap any cluster/also-proxy subnet from
  `status --output json`. Supersedes
  `Test_ComposeDownPreservesNamedVolumes`,
  `Test_ComposeDefaultNetworkNoSubnetConflict`.

### state area (suites/state/, area "state", NeedsManager(Default)) — NEW area

`telepresence apply/delete -f` over WorkstationState manifests; supersedes
`state_manifest_test.go` (no suite-catalog row existed for this surface).
The area connects and disconnects via manifests, so it must not share the
default connection: manifests declare their own connection block, and the
area runs late in main_test.go (after connection-churning areas), with a
doc comment justifying the position.

- Apply: dry-run on a clean workstation (`would-connect`/`would-create`,
  daemons stay down), create → reuse/unchanged on re-apply, local-port
  drift → `re-created` while the sibling ingest stays `unchanged`,
  connection drift → `has drifted` error with the live session untouched.
  Supersedes `Test_ApplyDryRunConnectionMissing`,
  `Test_ApplyCreatesReusesUnchanged`, `Test_ApplyAttachmentDrift`,
  `Test_ApplyConnectionDrift`.
- Delete: reverse-order teardown, `absent` for a manually detached
  attachment, `not connected`/`not running; nothing to tear down`
  variants, connection-less manifest requires and preserves an existing
  session. Supersedes `Test_Delete`, `Test_DeleteAbsentAttachment`,
  `Test_ApplyDeleteNoConnectionRequiresSession`,
  `Test_ApplyDeleteWithoutConnectionKeepsSession`.
- Handler (NotOn("windows")): attachment `command:` lifecycle — handler
  sees `TELEPRESENCE_INTERCEPT_ID`, pid stable across a no-op apply,
  restarted on argv change, dead after delete; pid read via
  `rt.UserCacheDir()`. Supersedes `Test_ApplyHandlerCommand`.

### quic area additions (suites/quic/)

- Datagrams (inline QuicNodePort+ExtraEnv spec, WithLabels(Slow)): UDP
  echo round trip with datagram carriage enabled; assert manager log
  reports nonzero `datagram counters`. Supersedes
  `quic_test.go:Test_AUDPEchoDatagrams`.
- ForwarderRestart (NeedsManager(QuicNodePort)): delete all
  `quic-forwarder` pods; traffic recovers and `tunnel_transport` never
  leaves `quic` (stateless-router property). Reuses the scale helpers in
  quic/helpers.go. Supersedes `Test_ForwarderRestartSurvival`.
- ManagerOutage (NeedsManager(QuicNodePort), WithLabels(Slow)): scale the
  manager to 0 with a live intercept in an `rt.PrivateNamespace`; an
  in-cluster curl still reaches the local handler (agent→forwarder→laptop
  is manager-independent), then full recovery. Manager churn goes through
  `rt.Mutate`. Supersedes `Test_ZManagerOutageAttachmentSurvival`.
- Deferred, catalog-noted: NodePort endpoint discovery
  (`Test_ZZDiscoveryNodePort` — needs a nodes ClusterRole grant plus a
  no-externalHost install variant) and the node-agent quic transport
  cross-check (belongs to the nodeagent area). Explicit `t.Skip`
  placeholders are not needed; add suite-catalog rows referencing
  telepresenceio/telepresence#4227, which tracks both.

Area wiring: orchestrator adds `TestState` (+ blank import) to
main_test.go with an ordering doc comment, and updates suite-catalog.md:
new state rows, the docker/Run and docker/Compose rows marked implemented,
and the two deferred quic rows.

Notes: all fixtures via lazy accessors, never `SetupSuite`; connections
other than the shared one are named (`rt.ConnDocker`, manifests'
`trn-<ns>`); no hard-coded ports except externally-defined ones with a
comment; every suite/const doc comment names the superseded
integration_test test and the production source that justifies it;
`--output json` over text scraping wherever the CLI offers it.

## Verification plan (orchestrator)

- `go vet` + `golangci-lint` on touched packages; chart render golden
  matrix still green after the quicforwarder template change.
- Scoped live runs per area on kind-dev
  (`-run 'TestDocker|TestState|TestQuic'`), then one full
  `RTEST_COVER=1` run: expect `cli/docker/compose`, `cli/manifest`,
  `cmd/quicforwarder`, and `pkg/quicfwd` all well above zero and total
  coverage ≥60%.
- Runtime budget: the new suites must not push the full serial run past
  the plan's wall-clock target; Slow-labeled tests carry the overrun.
