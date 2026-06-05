# Migrate off `github.com/docker/docker` to the split Moby SDK modules

## Status

Implemented in this PR — the migration described below has been carried out.

## Motivation

Telepresence imports the monolithic `github.com/docker/docker` module, which is
**frozen at `v28.5.2+incompatible`**. From Docker Engine v29 onward the Moby
project stopped publishing that import path and split the SDK into separate,
independently-versioned modules (engine releases are now tagged
`docker-vX.Y.Z`, e.g. `docker-v29.5.2`):

| New module | Contents | Latest release | Stable? |
|------------------------------|--------------------------------------|----------------|---------|
| `github.com/moby/moby/client`| API client (`client.*`)              | v0.4.1         | Yes     |
| `github.com/moby/moby/api`   | wire types (`api/types/*`)           | v1.54.2        | Yes     |
| `github.com/moby/moby/v2`    | daemon/engine (incl. `daemon/pkg/opts`) | v2.0.0-beta.15 | No (beta) |

Because the legacy path is a dead end, `make update-dependencies` can never move
it past `v28.5.2`. This leaves **15 open Dependabot security alerts** against
`github.com/docker/docker` (#204-#209, #218-#226) that cannot be resolved by a
version bump.

Crucially, those advisories are all **daemon-side** vulnerabilities (AuthZ
plugin bypass, etc.) in code Telepresence never imports — we consume only the
Docker *client* and *api/types*. Dependabot flags at module granularity, so the
fix is not a bump but **removing the `github.com/docker/docker` dependency from
the graph**. Migrating onto `moby/moby/client` + `moby/moby/api` does exactly
that and clears all 15 alerts at once, including the 12 with "no fix available"
(there is no fix to apply — we simply stop depending on the module).

## Goal

Remove `github.com/docker/docker` from the dependency graph of both the main
module and `cmd/cobraparser`, replacing it with the split Moby modules, with no
behavioral change to the Docker integration.

## Non-goals

- Bumping the Docker Engine the daemon talks to. This is purely a client-side
  SDK import change.
- Adopting the beta `github.com/moby/moby/v2` daemon module in shipped binaries
  (see the `opts` decision below).

## Scope — current import sites

Main module (15 import lines across 8 files):

| File | Imports |
|------|---------|
| `pkg/client/docker/context.go` | `client` |
| `pkg/client/docker/container.go` | `api/types/container` |
| `pkg/client/docker/daemon.go` | `api/types/container`, `api/types/filters`, `api/types/network`, `client` |
| `pkg/client/docker/plugin.go` | `api/types` (`dockerTypes.PluginEnableOptions`) |
| `pkg/client/docker/volume.go` | `api/types/volume` |
| `pkg/client/docker/teleroute/network.go` | `api/types/filters`, `api/types/network`, `client` |
| `integration_test/compose_test.go` | `api/types/network`, `client` |
| `integration_test/docker_daemon_test.go` | `api/types/network`, `client` |

`cmd/cobraparser` (separate module, 1 import line):

| File | Imports |
|------|---------|
| `cmd/cobraparser/generate/generate.go` | `opts` (`opts.MemBytes`, `opts.NewListOptsRef`) |

## Import mapping

| Old import | New import | Target version |
|------------|------------|----------------|
| `github.com/docker/docker/client` | `github.com/moby/moby/client` | v0.4.1 |
| `github.com/docker/docker/api/types/container` | `github.com/moby/moby/api/types/container` | v1.54.2 |
| `github.com/docker/docker/api/types/network` | `github.com/moby/moby/api/types/network` | v1.54.2 |
| `github.com/docker/docker/api/types/volume` | `github.com/moby/moby/api/types/volume` | v1.54.2 |
| `github.com/docker/docker/api/types` (`PluginEnableOptions`) | see API delta below | — |
| `github.com/docker/docker/api/types/filters` | see API delta below | — |
| `github.com/docker/docker/opts` (`MemBytes`, `NewListOptsRef`) | see `opts` decision | — |

## Known API deltas (must be reconciled, not mechanical renames)

These packages were relocated/restructured at v29, so the migration is not a
pure find-and-replace. The specifics below are confirmed against the
elastic/beats migration (PR #50300, see Prior art) which targets the exact same
module versions:

1. **`filters`** — the public `api/types/filters` package no longer exists.
   Filter construction moved into the client package. Concretely:
   `filters.NewArgs(...)` becomes `make(client.Filters)`, where
   `client.Filters` is `map[string]map[string]bool`. Audit how `daemon.go` and
   `teleroute/network.go` build `filters.Args` and port them. Highest-risk delta.

2. **Client constructor** — `client.NewClientWithOpts(...)` becomes
   `client.New(...)`; `WithAPIVersionNegotiation()` is removed (it is now the
   default). Re-check every `client.New*` call site.

3. **Options types moved `api/types/*` -> `client`** — e.g.
   `container.ListOptions` -> `client.ContainerListOptions`. This is the same
   relocation that moves `PluginEnableOptions` out of the `api/types` root into
   the client package, so `plugin.go`'s `dockerTypes.PluginEnableOptions`
   becomes the client-side `client.PluginEnableOptions`. Plugin *resource* types
   remain under `api/types/plugin`.

4. **List/inspect methods now return result structs** — `ContainerList` etc.
   return a `Result` you unwrap with `.Items`; `ContainerStats` takes
   `client.ContainerStatsOptions{Stream:...}` instead of a positional bool;
   `Events()` returns a single result exposing `.Messages`/`.Err` channels;
   `ContainerWait` returns a single `ContainerWaitResult`. Audit our call sites
   in `daemon.go`, `container.go`, `volume.go`, `teleroute/network.go`.

5. **`network.EndpointSettings.IPAddress` changed `string` -> `netip.Addr`.**
   We import `api/types/network` in four files; check every read of endpoint
   IP fields.

6. **`opts.MemBytes` / `opts.NewListOptsRef`** — now under
   `github.com/moby/moby/v2/daemon/pkg/opts`, part of the **beta** `/v2` module.
   This is daemon-side (the same class of package elastic/beats had to leave on
   `docker/docker`, see Prior art note). It is the only usage that would pull a
   beta dependency, and only into `cmd/cobraparser`. See the decision below.

## Decision needed: how to handle `cobraparser`'s `opts` usage

`cmd/cobraparser/generate/generate.go` uses exactly two helpers — `opts.MemBytes`
(a `flag.Value` parsing memory sizes) and `opts.NewListOptsRef` (a string-slice
`flag.Value`). Options, in preference order:

- **A. Reimplement the two helpers locally in cobraparser** (~30-40 lines, no new
  dependency). Keeps the entire tree off the beta `/v2` module. Recommended.
- **B. Depend on `github.com/moby/moby/v2` beta in cobraparser only.** Acceptable
  because cobraparser is a build tool, not shipped, and the beta module is not
  named by any current advisory — but it reintroduces a beta dependency and a
  large module for two tiny helpers.

Option A is preferred; it leaves zero Moby-beta surface and zero
`github.com/docker/docker` references anywhere in the repo.

## Execution steps

1. **cobraparser first** (it is consumed by the main module as a pinned
   pseudo-version):
   - Apply the chosen `opts` decision (A or B) in `cmd/cobraparser`.
   - `go mod tidy` in `cmd/cobraparser`; confirm `github.com/docker/docker` is
     gone from its `go.mod`/`go.sum`.
   - Commit, push, and let the main module bump its cobraparser pseudo-version
     (mirrors the existing `2.0.0-<date>-<hash>` reference flow).
2. **Main module client + api/types migration:**
   - Rewrite the 8 files per the import mapping.
   - Reconcile the `filters` and `PluginEnableOptions` deltas by hand.
   - `go get github.com/moby/moby/client@v0.4.1 github.com/moby/moby/api@v1.54.2`
   - `go mod tidy`; confirm `github.com/docker/docker` no longer appears in
     `go.mod`/`go.sum` (also check `cmd/teleroute`, `integration_test/testdata/*`,
     and `pkg/vif/testdata/*` sub-modules don't reintroduce it).
3. **Regenerate** docs/licenses: `make generate` (updates `DEPENDENCIES.md`,
   `DEPENDENCY_LICENSES.md`).
4. **Build + lint:** `make build`, `make lint`.

## Verification

- `go mod why github.com/docker/docker` in every module returns "does not need"
  (or the package is absent from the build list entirely).
- `grep -rn "github.com/docker/docker" --include=go.mod .` returns nothing.
- `make check-unit` passes.
- Docker integration tests pass (require a cluster + Docker):
  - `TEST_SUITE='^DockerDaemon$'` and the compose suites in `integration_test/`.
  - `telepresence connect --docker` smoke test (the `teleroute` network path).
- After merge to `release/v2` and the next Dependabot rescan, confirm all 15
  `docker/docker` alerts move to "no longer in dependency graph".

## Prior art / reference migrations

This is a well-trodden path; several mainstream Go projects have already made
the exact same transition to `moby/moby/client` v0.4.1 + `moby/moby/api`
v1.54.2. Use these as worked examples:

- **elastic/beats #50300** (MERGED) — *"Migrate from deprecated docker/docker to
  moby/moby split modules."* The PR description is effectively a migration
  cheat-sheet (source of the confirmed API deltas above) and includes an
  important note: daemon-side packages (`daemon/logger`,
  `api/types/plugins/logdriver`, `pkg/ioutils`) are **not** in the split
  modules, so beats *kept* `docker/docker` for those. This is the same trap as
  our `opts` usage — confirming that anything daemon-side has no split-module
  home yet.
  https://github.com/elastic/beats/pull/50300
- **elastic/elastic-agent #13727** (MERGED) — the earlier sibling migration that
  beats aligned to. https://github.com/elastic/elastic-agent/pull/13727
- **trufflesecurity/trufflehog** — *"Move to github.com/moby/moby/client from
  docker/docker."*
- **dhui/dktest** — *"migrate from github.com/docker/docker -> github.com/moby/moby."*
  (A container-test helper; usage profile close to ours.)
- **buildpacks/pack** — *"remove direct dependency on github.com/docker/docker."*

Other confirmed adopters importing `github.com/moby/moby/client` in their own
source (not just transitively): docker/cli, docker/compose, docker/buildx,
testcontainers/testcontainers-go, hashicorp/nomad, traefik/traefik,
prometheus/prometheus, tilt-dev/tilt, nektos/act, DataDog/datadog-agent. The
client API we depend on is therefore exercised broadly at v0.4.x.

Key takeaway from the prior art: the client/api migration is mechanical-ish once
the deltas above are understood, and the **only** thing that forced anyone to
retain `docker/docker` was daemon-side packages — which for us is solely the
cobraparser `opts` helper (resolved by Option A).

## Risks

- **`filters` API change** is the most likely source of compile/behavior breaks;
  budget time to port filter construction carefully.
- **v0.4.x client is pre-1.0**: API may still shift. Pin exact versions and
  re-verify on each future bump.
- **Sub-module drift**: `cmd/teleroute` and the testdata modules each have their
  own `go.mod`; ensure none transitively reintroduce `github.com/docker/docker`.

## Out of scope / future

- Tracking a stable `github.com/moby/moby/v2` (daemon) release, only relevant if
  Option B is chosen or if future needs require daemon-side packages.
