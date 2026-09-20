# Bring `telepresence setup` up to date with the 2.32.0 features

Branch: `thallgren/setup-security-questions`. Status: implemented on this branch; remove before the PR merges.

## Why

`telepresence setup` landed on 2026-07-19. Since then 2.32.0 gained four
Helm settings that decide how much cluster access a client needs, plus the
StatefulSet migration. Setup knows about them only partially:

| Feature (2.32.0)                                   | What setup does today                                                                  | Gap |
|----------------------------------------------------|----------------------------------------------------------------------------------------|-----|
| `security.authentication.mode`                     | Reacts when an `--input` file pins it (x509 privilege check, health, verify notes)      | Never asked or recommended |
| `security.authorization.requiredGrant`             | Nothing                                                                                | Never asked, no validation |
| `clientRbac.legacyAccess`                          | Passes through untouched                                                               | Never recommended; `apiPort` override not considered |
| `externalEndpoint`                                 | Post-apply verification only (`verify_external.go`)                                    | Never asked; prerequisites (TLS Secret / cert-manager) not probed; not in health checks; no pre-apply validation of the chart's two `fail` guards |
| `logStreaming`                                     | Passes through                                                                         | None needed (tunables); document as pass-through |
| StatefulSet with fixed pod name                    | Health reads the StatefulSet                                                           | A pre-2.32 release has a Deployment, so health reports a false "statefulset not found"; upgrade proposal has no migration note |

The result is that the [client permissions ladder](../../howtos/client-rbac.md)
can only be climbed by hand-editing a values file. Setup should be able to
propose it, gated on what the cluster and the caller's own credentials allow.

## Design

Setup's structure stays as it is: probes produce `ClusterFacts`, the interview
fills `Answers`, `RecommendWithInput` turns both into values, `--input` pins
answers and protects keys, `ValidateValues` rejects hard incompatibilities,
and `VerifyInstall` checks the result. Every change below slots into one of
those stages.

### 1. Facts and probes

New facts, all denial-tolerant (denied -> `unknown`):

- `Release.Workload`: `"StatefulSet"` or `"Deployment"`, from which object
  exists. Drives the migration note and the health fallback.
- `External.CertManager Finding`: whether the `certificates.cert-manager.io`
  CRD is served (discovery lookup).
- `External.TLSSecrets []string`: names of `kubernetes.io/tls` Secrets in
  the manager namespace (metadata only).
- `Privileges.CertManagerCertificate Finding`: one SSAR for `create` on
  `certificates.cert-manager.io` in the manager namespace. Consulted only
  when the proposal chooses the cert-manager path, like `X509KubeSystem`.
- `Health.ExternalEndpoint *Finding`: when the release enables the endpoint,
  the Service exists and (for LoadBalancer) has an ingress address. Reuses
  `externalServiceLook`.
- `healthManager` falls back to the `traffic-manager` Deployment when the
  StatefulSet is absent, with evidence saying the upgrade will migrate it.

One new probe phase, "Probing external endpoint prerequisites", added to
`ProbePhases`. The P1 candidate render also enables the external endpoint
(with enforcing mode and a placeholder `tls.secretName`) so the extra
Service is part of the privilege sweep.

### 2. Interview

Four new questions, asked after the managed-scope question and before the
routing-conflict question. Every one is skipped when `--input` pins its key.
The default on an upgrade is always the installed release's current value, so
an upgrade never flips a setting the operator did not answer for.

7. **Enforce authentication** (always): "Enforce caller authentication?
   Required for Telepresence-specific grants and the external endpoint."
   Fresh-install default: yes when this client's own credentials would be
   accepted (a bearer token, or a client certificate with the kube-system
   RoleBinding privilege not denied), otherwise no. Answer -> `EnforceAuth`.
8. **Required grant** (only when enforcing): numbered choice
   `any` / `telepresence` / `portforward`, default `any`, or `telepresence`
   when the external endpoint is chosen. Skipped in permissive mode with no
   note: the setting has nothing to act on there. Answer -> `RequiredGrant`.
9. **External endpoint** (only when enforcing AND cert-manager or a TLS
   Secret is available): "Publish an external control endpoint so clients
   need no Kubernetes API access?" default no. On yes, the certificate
   source is decided by what the probes found:

   | TLS Secrets in manager ns | cert-manager | Endpoint question | Certificate prompt |
   |---------------------------|--------------|-------------------|--------------------|
   | none                      | absent       | skipped, info note names both prerequisites | none |
   | none                      | present      | asked             | cert-manager prompts directly |
   | one or more               | absent       | asked             | list of Secrets; a single one is the default |
   | one or more               | present      | asked             | list of Secrets plus "a new one issued by cert-manager" |

   The Secret list shows one line per `kubernetes.io/tls` Secret with its
   DNS names and expiry, read from `tls.crt` (best effort: a denied `get`
   shows the name alone). A Secret named `traffic-manager-external-tls`,
   the chart's own cert-manager target, is listed first and is the default;
   with several Secrets and no such name there is no default. An expired or
   soon-to-expire certificate is marked and never the default. Example:

       The endpoint needs a TLS certificate that clients can trust. Which one should it use?
         1) Secret "tm-external-tls"   tm.example.com    expires 2027-03-01
         2) Secret "ingress-wildcard"  *.example.com     expires 2026-11-14
         3) a new one issued by cert-manager
       Choose 1-3:

   The cert-manager path asks for the issuer name, the issuer kind (default
   `ClusterIssuer`), and the DNS names clients will use. Non-interactive
   runs with several Secrets and no pinned `externalEndpoint.tls.secretName`,
   or a cert-manager choice without pinned issuer and DNS names, error out
   the way the unpinned label selector does.

   Service type is not asked: `LoadBalancer` when the QUIC probe found it
   viable, `NodePort` otherwise, with a note that the DNS names must then
   resolve to a node address.
   Answer -> `ExternalEndpoint`, `ExternalTLSSecret`, `ExternalCertManager{Issuer, Kind, DNSNames}`.
10. **Legacy client access** (always): "Do clients older than 2.32 need to
    connect to this traffic-manager?" Fresh-install default no, which
    renders `clientRbac.legacyAccess: false`. Forced to yes with a note when
    `apiPort` is overridden in the input or the release values.
    Answer -> `LegacyAccess`.

Questions 8 to 10 are ordered so that the external-endpoint answer can
influence the required-grant default; the code asks 9 before 8.

Prompt wording rule: prompts use the words the person already knows from
the question they answered ("endpoint", "clients", "traffic-manager") and
the Helm value name when one is being set. Never chart or code vocabulary
such as "listener", "surface", "review", or "grant" without saying what it
is. Example for the certificate choice:

    The endpoint needs a TLS certificate that clients can trust. Which one should it use?
      1) existing TLS Secret "tm-external-tls"
      2) a new one issued by cert-manager
    Choose 1-2 [1]:

### 3. Pins (`--input`)

`DerivePins` gains pins for `security.authentication.mode`,
`security.authorization.requiredGrant`, `externalEndpoint.enabled` (with
`tls.secretName` / `tls.certManager.*` carried along), and
`clientRbac.legacyAccess`. `ReconcileWithInput` already protects every
pinned key, so a hand-written values file keeps winning over the engine.

### 4. Recommend

- Emit `security.authentication.mode` from the answer.
- Emit `security.authorization.requiredGrant` only when enforcing.
- Emit the `externalEndpoint` block when chosen; warn when QUIC ends up
  disabled ("attachments need the QUIC endpoint alongside the external
  endpoint"); run the cert-manager privilege check like the x509 one.
- Emit `clientRbac.legacyAccess`.
- Info note on upgrade when `Release.Workload` is Deployment: the upgrade
  migrates to a StatefulSet with a brief window with no ready manager;
  sessions re-establish.

### 4b. Verify

For the cert-manager path the certificate may not be issued when
verification starts, so `verifyExternalEndpoint` waits for the Secret to
appear within the existing verification timeout and notes when it does not.

### 5. Validate

`ValidateValues` mirrors the chart's two `fail` guards with clear messages
before Helm runs: `externalEndpoint.enabled` needs enforcing mode, and
`externalEndpoint.tls` needs exactly one of `secretName` /
`certManager.enabled`. Both apply to hand-written `--input` files too.

### 6. Report and docs

- Findings gain `authentication` (credential kinds, kube-system privilege),
  `external endpoint` (cert-manager, TLS Secrets), and the new health line.
- `docs/reference/setup.md`: probe table (StatefulSet, new probe), interview
  questions 7 to 10, pin list, non-interactive defaults, a "Minimize client
  permissions" flow linking the how-to, health section says StatefulSet.
- `docs/howtos/client-rbac.md`: closing paragraph says setup can now
  propose each step.
- Command `Long` text in `pkg/client/cli/cmd/setup.go`.
- Changelog: extend the existing unreleased "Guided traffic-manager setup"
  entry body with one sentence about the security and client-access
  questions. No new entry.

### 7. Tests

- Unit: `facts_test`, `probe_health_test` (Deployment fallback),
  `interview_test` (gating and defaults for each new question, upgrade
  keeps release values), `input_test` (new pins), `recommend_test` (emitted
  keys, QUIC warning, migration note, apiPort forcing), `input_test`
  validation cases for the two chart guards.
- Regression (`regression_test/suites/install/setup.go`): one
  non-interactive `--input` run that enforces authentication with
  `requiredGrant: telepresence` and `legacyAccess: false`, applies, and
  connects; one validation case for the external-endpoint guard messages.
  The external endpoint itself is already covered by `suites/auth/external*`.

## Work items

Each runs as a fresh sonnet subagent with a compact brief. Main model reviews
each diff, then runs `make lint` and `make check-unit`.

1. Facts, probes, health fallback, report lines (+ unit tests).
2. Answers, interview, pins, recommend, validate (+ unit tests).
3. Docs, command text, changelog sentence.
4. Regression suite additions.

## Decisions (agreed 2026-09-16)

1. On a fresh install, the "Enforce caller authentication?" question defaults
   to yes when the caller's own credentials would pass under enforcing mode,
   otherwise to no.
2. The certificate choice follows the table under question 9: an existing
   Secret is always preferred, cert-manager is offered only as an addition
   or when no Secret exists, and the question is skipped when neither
   exists.
3. A fresh install defaults to `clientRbac.legacyAccess: false`.
4. The work lands on its own branch from `release/v2`, ahead of the 2.32.0
   release preparation.

## Out of scope

- `logStreaming` tunables: no interview question.
- The ambiguous-workload-kind change and the `--replace` removal: not
  install-time concerns.
