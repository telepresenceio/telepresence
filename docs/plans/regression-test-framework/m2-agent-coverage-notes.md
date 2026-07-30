# Agent coverage: investigation notes (milestone 2)

Question: can the agent-injector be made to add a `GOCOVERDIR` env var and a
hostPath volume to injected traffic-agent containers without changing
production code?

## Verdict

No. There is no existing passthrough of arbitrary env vars or volumes into
the generated traffic-agent container. Wiring agent coverage requires
production-code changes in several places, so milestone 2 covers client and
manager coverage only; agent coverage is left for a later milestone.

## Evidence

The traffic-agent container spec is not rendered from the Helm chart at all
(unlike the traffic-manager Deployment, which templates/deployment.yaml
renders from `.Values` on `helm install`). It is built server-side, per pod,
by the traffic-manager at mutation time, from a fixed set of Go structs:

- `pkg/agentconfig/sidecar.go:220-302` -- `type Sidecar struct`, the
  config baked into the `AGENT_CONFIG` annotation the injector reads back.
  Its fields are a closed set (image, resources, security contexts, mount
  policies, mesh subnets, containers/intercepts, timeouts, feature flags).
  There is no `ExtraEnv`/`ExtraVolumes` field, nor any generic
  map/passthrough field.

- `pkg/agentconfig/container.go:26-207` -- `(*ContainerBuilder).AgentContainer`
  builds the agent container's `Env` (lines 73-118) and `VolumeMounts`
  (lines 120-142) from that fixed struct plus a handful of computed
  values (API port, QUIC port, pod IP/UID/name via downward API). Adding an
  arbitrary env var or volume mount here means editing this function.

- `pkg/agentconfig/volumes.go:15-74` -- `AgentVolumes` returns a hardcoded
  list (`pod-info`, `export-volume`, `tel-agent-tmp`,
  `traffic-manager-token`) plus TLS secret volumes matched through two
  specific annotations (`DownstreamTLSSecret`/`UpstreamTLSSecret`). No
  generic hostPath (or any other volume kind) passthrough exists.

- `pkg/agentmap/generator.go:30-56` -- `type GeneratorConfig struct`, what
  the traffic-manager assembles from its own environment to produce a
  `Sidecar`. Also a closed field set; no `Env`/`Volumes` fields.

- `cmd/traffic/cmd/manager/managerutil/envconfig.go:34` (`type Env struct`)
  and `:180` (`(*Env).GeneratorConfig`) -- the traffic-manager's own
  environment is parsed by `github.com/caarlos0/env` into this fixed
  struct; there is no `AGENT_EXTRA_ENV`/`AGENT_EXTRA_VOLUMES` var today, and
  the library has no wildcard/passthrough mode -- every var the agent might
  need has to be declared as a named field here, threaded through
  `GeneratorConfig()`, into `agentmap.GeneratorConfig`, into
  `agentconfig.Sidecar`, and consumed in `container.go` and `volumes.go`.

- `cmd/traffic/cmd/manager/mutator/agent_injector.go:316`
  (`addAgentVolumes`) and `:441` (`addAgentContainer`) -- the injector's
  patch-building calls `agentconfig.AgentVolumes` and
  `ContainerBuilder.AgentContainer` directly; there is no extension point
  between them and the pod patch.

- `charts/telepresence-oss/templates/deployment.yaml` -- the new
  `extraEnv`/`extraVolumes`/`extraVolumeMounts` chart values added in this
  milestone apply only to the traffic-manager container/pod (the Deployment
  this template renders). There is no wiring from those chart values to the
  traffic-agent container spec, because that spec is not templated by Helm
  at all (see above) -- so the chart addition has no path to reach agents
  even indirectly.

The node-agent Job path (node-hosted traffic-agent mode) builds its
container from the same `agentconfig.Sidecar`/`ContainerBuilder`, so it has
the same gap.

## What it would take

A minimal implementation would need, all in production code:

1. `agentconfig.Sidecar`: add `ExtraEnv []core.EnvVar` and
   `ExtraVolumes []core.Volume` (+ matching VolumeMounts) fields.
2. `agentconfig.container.go` (`AgentContainer`) and `volumes.go`
   (`AgentVolumes`): append the new fields to the built `Env`/`VolumeMounts`
   and to the pod's `Volumes`.
3. `pkg/agentmap.GeneratorConfig`: add matching fields.
4. `cmd/traffic/cmd/manager/managerutil/envconfig.go`: new `AGENT_EXTRA_ENV`/
   `AGENT_EXTRA_VOLUMES` (or similar JSON-encoded) env vars, parsed into
   `Env`, threaded through `GeneratorConfig()`.
5. `charts/telepresence-oss/templates/deployment.yaml` +
   `values.schema.yaml`: chart values (e.g. `agent.extraEnv`/
   `agent.extraVolumes`) rendered into the above manager env vars.

Per the task's instructions, none of this was implemented here. Client and
manager coverage (this milestone) do not depend on it: the manager's own
container is chart-templated directly, which is why
`extraEnv`/`extraVolumes`/`extraVolumeMounts` on the traffic-manager
Deployment was a clean, non-production-code-adjacent addition (chart-only),
while the same thing for the agent is not.
