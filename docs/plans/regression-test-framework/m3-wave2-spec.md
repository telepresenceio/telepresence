# M3 wave 2 spec (install, injector, namespaces)

## Framework extensions (first, one agent)

1. **managers.Values additions** (with Merge support, key names verified
   against charts/telepresence-oss/values.schema.yaml):
   - `AgentInjector` (`agentInjector`): `Enabled *bool`, `InjectPolicy
     string`, `Certificate{Regenerate bool, AccessMethod string}`,
     `Webhook{ReinvocationPolicy string}`.
   - `Namespaces []string` (`namespaces`) — mutually exclusive with
     NamespaceSelector; Merge must null the selector when a static list is
     set.
   - `PodCIDRs []string`, `PodCIDRStrategy string`.
2. **managers catalog entries**: `InjectorDisabled()`, `InjectPolicy(p
   string)`, `CertRegen(accessMethod string)`, `StaticNamespaces(ns
   ...string)` (parameterized Keys like "inject-policy/OnDemand" so hashes
   differ per parameter).
3. **workloads.Template**: `Annotations map[string]string` (pod template
   annotations — for telepresence.io/inject-* tests) and `Resources` (CPU/
   memory requests/limits) fields, threaded through both templates and the
   fixture hash.
4. **rt**: `Conn.List` variant taking a namespace (`list -n <ns>`), if not
   already expressible; `Suite` accessor or helper for a workload in an
   arbitrary namespace (WorkloadFixture is exported — suites can use it with
   Get directly; verify and document rather than add API if it suffices).
5. **Carried-over gaps from wave 1** (fix all):
   - `rt.ConnManagerNamespace(ns string)` ConnOpt so a connection can target
     a SecondaryManager's namespace (ConnectMulti currently drives its
     second connection through raw CLI calls).
   - `Conn.InterceptNamed(t, name, wl, opts...)` for custom-named intercepts
     on a shared workload (suites/intercept/helpers.go has a local
     namedIntercept to retire; keep the helper file compiling by delegating
     or removing it and updating its callers).
   - `workloads.Template.AppProtocol string` — sets `appProtocol` on the
     service port (unskips intercept/Routing's h2c test; implement the
     local h2c server + unskip in the SAME pass, in suites/intercept/
     routing.go, using golang.org/x/net/http2 h2c).
   - `cli.InterceptInfo`: add `PortID`/`TargetPort`/`ContainerPort` fields
     (retire the local detailedInfo embedding in suites/intercept/flags.go).
   - `cli.ListEntry`: add the attachment-presence fields (intercept_info/
     ingest_info names only) so "detach removes it from list" is assertable;
     update suites/attach to use them where the agent noted the gap.
   - `LocalService`: expose a request-observation hook (count or last-N
     paths) so the wiretap copy assertion can land; update the wiretap
     cells to assert the local copy.

## Suites

### install area (suites/install/, area "install")
- HelmLifecycle: SecondaryManager in a PrivateNamespace: install ->
  uninstall -> reinstall works; second install into the SAME namespace
  collides with a clear error; a broken install (bogus image.tag=9.9.9,
  install failure or pod-not-ready surfaced with the pod reason) followed by
  a corrected upgrade succeeds. All against the secondary release —
  never the shared one.
- HelmValues: upgrade with `--set logLevel=info` then plain upgrade -f
  (framework's reset-values) restores; validates --reuse-values vs
  --reset-values semantics on the secondary release via the CLI.
- PodCIDRs: shared release spec switch: podCIDRStrategy=environment +
  explicit podCIDRs values; connect; `status --format json` root daemon
  subnets contain the configured CIDRs.
- Setup: the `telepresence setup` verb (read pkg/client/cli/setup and
  integration_test/setup_test.go for semantics): --apply idempotence
  (second run makes no changes), --output/--input lossless round trip
  (no cluster mutation), `--output -` streaming, validation-only run
  reports would-install without mutating. Non-admin handoff (--as) only if
  expressible without extra RBAC fixtures; else skip with reason.

### injector area (suites/injector/, area "injector")
- AutoInject: workload with annotation telepresence.io/inject="enabled" in
  a private namespace: agent present after rollout without any intercept
  (list --agents or pod container inspection); annotation removal + rollout
  removes it.
- InjectPolicies: for each policy value (read the valid set from
  values.schema.yaml): switch the shared release to InjectPolicy(p), fresh
  private workload, assert observable behavior (OnDemand: agent only after
  intercept; WhenEnabled: agent only with the annotation; etc. — derive
  exact semantics from integration_test/inject_policy_test.go).
- CertRegen: for accessMethod in {watch, mount}: spec switch, intercept
  works, then delete the injector's TLS secret and assert re-injection
  still works after regeneration (mirror injector_test.go).
- LimitRange: private namespace with a LimitRange (apply via kubectl in
  the test); annotated workload with resources; agent container must carry
  defaulted resources (inspect pod JSON).
- Disabled: InjectorDisabled() spec: intercept fails mentioning the
  disabled injector; version/list still work.
- ManualAgent: genyaml-produced agent added to a workload manually, then
  intercept works without the webhook (mirror manual_agent_test.go); skip
  with reason if genyaml output cannot be applied cleanly.

### namespaces area (suites/namespaces/, area "namespaces")
- SelectorSemantics: shared release default (label selector): a namespace
  gains the managed label -> becomes attachable after webhook sync; label
  removed -> connect to it refused.
- StaticList: spec switch to StaticNamespaces(nsA, nsB) over two private
  namespaces: both attachable, a third (labeled but unlisted) is refused;
  switch back to the selector spec afterward (Mutate so later areas
  re-provision).
- MappedNamespaces: connect --mapped-namespaces on a subset: list shows
  only the mapped namespace's workloads; DNS for unmapped services does
  not resolve (loose assertion; skip DNS part if flaky).

Area wiring: new blank imports + TestInstall/TestInjector/TestNamespaces
entries in main_test.go — the ORCHESTRATOR adds these before dispatching
suite agents (never the suite agents themselves).

Ordering note: install and injector churn the shared release's spec
repeatedly; both areas must restore nothing manually — the engine's
spec-grouping sort plus Mutate discipline handles it. Any test that
switches the shared release to a non-default spec acquires it with
rt.Mutate so later areas re-provision Default.
