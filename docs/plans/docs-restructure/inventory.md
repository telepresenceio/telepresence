# Documentation inventory

## Decision: umbrella term is "attachment"

The umbrella term for replace/intercept/ingest/wiretap is **attachment**
(verb: attach, opposite: detach). It replaces "engagement" everywhere.
Rename map:

| Old | New |
|-----|-----|
| engagement(s) | attachment(s) |
| engage (verb) | attach (note: takes "to" - "attach to a workload") |
| engaged container/workload | attached container/workload |
| disengage | detach |
| telepresence leave | telepresence detach (new command; leave kept as a deprecated, hidden alias) |
| docs/reference/engagements/ | docs/reference/attachments/ (with redirects.yml entries) |
| docs/howtos/engage.md | docs/howtos/attach.md (with redirects.yml entry) |

Scope: hand-written docs, doc-links.yml labels/links, CLI help and status
strings, internal Go identifiers (EngagementType and friends), the one
proto comment in rpc/manager/manager.proto. Out of scope: CHANGELOG.yml and
release-notes (historical), generated files (regenerated instead).

## Progress

| Step | Status |
|------|--------|
| 1. Inventory | Done |
| 2. Terminology sweep (attachment; detach command) | Done |
| 3. Quick-start tutorial rewrite | Done |
| 3b. Reframe compare/mirrord.md as an architecture trade-off (added to scope) | Done |
| 4. Concept pages (architecture moved, attachments page, glossary; devloop/faster/intercepts removed) | Done |
| 5a. Reference re-sort: moves and merges (monitoring, inside-container, docker-run, tun-device, dns, upgrade slim) | Done |
| 5b. Dedup rewrites (attachments/cli split, docker-compose vs compose, RBAC spread, mtls protocol section, faqs/troubleshooting pruning) | Done |
| 6. Vale + link checking in CI | Not started |

Step 1 of the docs restructure: every page under `docs/`, classified by the
Diataxis type it should serve (tutorial, how-to, reference, explanation) versus
what its content actually is today, with a verdict. Later steps (terminology
sweep, quick-start rewrite, concept pages, reference re-sort, CI linting) will
consume this list.

Verdict legend:

| Verdict | Meaning |
|---------|---------|
| keep    | Content and location are right; only minor touch-ups needed |
| rewrite | Right location, content needs substantial rework |
| move    | Content is fine but belongs in a different section |
| merge   | Fold into another page (target named in Notes) |
| delete  | Retire; add a redirect if the URL is public |

## Generated files - never hand-edit

These are produced by `make generate` / `make docs-files`. Any restructuring
must change the *source*, not the output. They are otherwise out of scope.

| File | Generated from | Via |
|------|----------------|-----|
| README.md | doc-links.yml | tools/tocgen |
| release-notes.md, release-notes.mdx | CHANGELOG.yml | tools/relnotesgen |
| variables.yml | CHANGELOG.yml | tools/relnotesgen |
| licenses.md | DEPENDENCY_LICENSES.md | rsync in main.mk |
| helm/values.schema.json | charts/telepresence-oss/values.schema.yaml | tools/y2j |
| reference/cli/*.md (87 files) | Go source (cobra commands) | telepresence man-pages |

Consequences for the restructure:

- Navigation changes are made in `doc-links.yml`; `README.md` follows on the
  next `make docs-files`.
- CLI page wording is fixed in the cobra command definitions under
  `pkg/client/cli/`, then regenerated.
- The CLI section dominates `reference/` by page count (87 of ~110 pages) but
  needs no restructuring work beyond nav grouping.

## Navigation and infrastructure files

| File | Role | Issues | Verdict |
|------|------|--------|---------|
| doc-links.yml | Nav source of truth (website + README) | Missing howtos/agent-modes and howtos/istio; label "Configure intercept using CLI" does not match page title "Configure workload engagements using CLI" | keep (fix entries) |
| redirects.yml | URL redirects for the website | NO CONSUMER: nothing in this repo or in telepresence.io reads this file. Real redirects are the hand-maintained Netlify static/_redirects in telepresence.io (entries for all restructure moves added there 2026-07-08). Decide: wire up a generator, or drop this file | decide |
| CONTRIBUTING.md | How docs flow into the telepresenceio.io site | Not in nav (intentional) | keep |
| common/quantity.md | Shared snippet included by config.md and cluster-config.md | Not a standalone page; fine | keep |

Nav desync found: `howtos/agent-modes.md` is listed in the generated
`README.md` but absent from `doc-links.yml`, so one of them was produced from
a different state of the other. Regenerating README from the current
doc-links.yml would silently drop the page. Fix doc-links.yml first.

## Top-level pages

| Page | Lines | Intended -> actual | Notes | Verdict |
|------|-------|--------------------|-------|---------|
| quick-start.md | 25 | tutorial -> link stub | Rewritten as an end-to-end echo-server tutorial (step 3) | done |
| faqs.md | 106 | FAQ -> FAQ | Modes answer now covers all four and defers to concepts/attachments.md; terminology fixed (step 5b) | done |
| troubleshooting.md | 313 | troubleshooting -> troubleshooting | Description fixed, sections grouped by symptom area with anchors preserved (step 5b) | done |
| community.md | 13 | meta -> meta | Fine | keep |
| compare/mirrord.md | 84 | explanation -> comparison | Reframed as an architecture trade-off with a balanced table (step 3b) | done |

## install/ (how-to guides; location is correct)

| Page | Lines | Intended -> actual | Notes | Verdict |
|------|-------|--------------------|-------|---------|
| install/client.md | 324 | how-to -> how-to | Per-platform install. Manual-download steps duplicated almost verbatim in upgrade.md | keep (dedupe) |
| install/upgrade.md | 96 | how-to -> how-to | Slimmed to "reinstall via install page" plus the Apple-silicon caveat (step 5a) | done |
| install/manager.md | 249 | how-to -> how-to + reference | Overlap was smaller than feared: chart values (how-to) vs raw permissions (reference) are complementary; pages now cross-link (step 5b) | done |
| install/cloud.md | 59 | how-to -> how-to | GKE/EKS prerequisites; current | keep |

## concepts/ (explanation; mostly legacy content)

| Page | Lines | Intended -> actual | Notes | Verdict |
|------|-------|--------------------|-------|---------|
| concepts/devloop.md | 55 | explanation -> marketing essay | Deleted; redirect to quick-start (step 4) | done |
| concepts/faster.md | 29 | explanation -> marketing essay | Deleted; redirect to quick-start (step 4) | done |
| concepts/intercepts.md | 147 | explanation -> partial | Replaced by concepts/attachments.md covering all four modes; redirect added (step 4) | done |

Missing concept pages the restructure should create (plan step 4):

- Engagement modes: replace vs intercept vs ingest vs wiretap (salvage the
  comparison currently living at the top of howtos/engage.md and the
  animation from concepts/intercepts.md).
- Architecture (move from reference/, see below).
- Glossary: engagement, traffic-manager, traffic-agent, sidecar vs node-agent,
  VIF, wiretap.

## howtos/ (how-to guides)

| Page | Lines | Intended -> actual | Notes | Verdict |
|------|-------|--------------------|-------|---------|
| howtos/engage.md | 310 | how-to -> explanation + how-to | Split done: comparison lives in concepts/attachments.md, tasks stay (steps 4/5b) | done |
| howtos/agent-modes.md | 154 | how-to -> decision guide + how-to | Recent, good shape. Missing from doc-links.yml | keep (add to nav) |
| howtos/docker.md | 123 | how-to -> how-to | Absorbed reference/docker-run.md (step 5a) | done |
| howtos/docker-compose.md | 372 | how-to -> reference + walkthrough | Extension table replaced with a pointer to reference/compose.md (step 5b) | done |
| howtos/cluster-in-vm.md | 198 | how-to -> how-to + explanation | Networking theory up front, then Vagrant/k3s example; acceptable for the audience | keep |
| howtos/istio.md | 86 | how-to -> how-to | Orphaned: in neither doc-links.yml nor README | keep (add to nav) |
| howtos/large-clusters.md | 50 | how-to -> how-to | Fine | keep |
| howtos/mtls.md | 121 | how-to -> how-to + reference | Protocol-selection section moved to reference/attachments/protocols.md (step 5b) | done |

## reference/ (the grab-bag; plan step 5)

True reference - correct as-is:

| Page | Lines | Notes | Verdict |
|------|-------|-------|---------|
| reference/config.md | 509 | Laptop-side config reference; canonical | keep |
| reference/cluster-config.md | 269 | Cluster-side config reference; canonical | keep |
| reference/compose.md | 172 | x-tele extension spec; canonical once howtos/docker-compose.md defers to it | keep |
| reference/environment.md | 49 | TELEPRESENCE_* env vars | keep |
| reference/volume.md | 42 | Volume mount behavior | keep |
| reference/restapi.md | 107 | API endpoints; no frontmatter (only page missing it) | keep (add frontmatter) |
| reference/rbac.md | 280 | Roles/permissions; receives depth from install/manager.md | keep |
| reference/plugins.md | 119 | Teleroute/telemount plugin reference | keep |
| reference/dns.md | 43 | Merged into routing.md DNS section; redirect added (step 5a) | done |

Explanation pages parked in reference - move or fold (concepts/ is the
natural home; a "networking internals" cluster is an option if concepts/
should stay small):

| Page | Lines | Notes | Verdict |
|------|-------|-------|---------|
| reference/architecture.md | 50 | Moved to concepts/architecture.md with redirect (step 4) | done |
| reference/routing.md | 57 | Now the networking reference: absorbed the VIF material from tun-device.md and the DNS intro/query types from dns.md (step 5a) | done |
| reference/tun-device.md | 32 | Folded into routing.md (VIF section) and volume.md (sshfs); SSH-era framing dropped; redirect added (step 5a) | done |
| reference/vpn.md | 329 | Explanation of VNAT conflict resolution + troubleshooting; content is current and valuable | keep (location debatable) |
| reference/agent-packet-routing.md | 220 | Recent, deep internals (nftables); good reference | keep |
| reference/node-agent.md | 177 | Recent; node-agent internals and limitations | keep |
| reference/route-controller.md | 132 | Recent; explanation + enable instructions | keep |

How-to pages parked in reference - move to howtos/:

| Page | Lines | Notes | Verdict |
|------|-------|-------|---------|
| reference/docker-run.md | 126 | Merged into howtos/docker.md; redirect added (step 5a) | done |
| reference/inside-container.md | 78 | Moved to howtos/ with redirect (step 5a) | done |
| reference/monitoring.md | 432 | Moved to howtos/ with redirect (step 5a) | done |

reference/engagements/ subsection:

| Page | Lines | Notes | Verdict |
|------|-------|-------|---------|
| reference/attachments/cli.md | 365 | Walkthroughs dropped (attach howto covers them), service-less tutorial condensed to the annotation contract, deprecated intercept --replace section rewritten (step 5b) | done |
| reference/engagements/sidecar.md | 79 | Injection semantics, annotations; true reference | keep |
| reference/engagements/container.md | 48 | --container targeting semantics | keep |
| reference/engagements/conflicts.md | 24 | Active-client semantics | keep |

## Cross-cutting findings

1. Terminology drift (plan step 2). Three generations coexist:
   "intercept" (concepts/intercepts.md, faqs.md, mtls.md title), "engage /
   engagement" (howtos/engage.md, reference/engagements/, compose.md), and
   the mode names replace/intercept/ingest/wiretap. Decide the canonical
   vocabulary (current code says engagement = umbrella, intercept = one
   mode), write the glossary, then sweep titles, frontmatter descriptions,
   doc-links.yml labels, and body text.
2. Nav desync and orphans: agent-modes (README only), istio (nowhere).
   doc-links.yml label vs page-title mismatches.
3. Duplication clusters to collapse:
   - docker: howtos/docker.md + reference/docker-run.md (+ overlap with
     reference/inside-container.md and plugins.md)
   - compose: howtos/docker-compose.md + reference/compose.md
   - install/upgrade: install/client.md + install/upgrade.md
   - RBAC/namespaces: install/manager.md + reference/rbac.md +
     reference/cluster-config.md
   - DNS: reference/dns.md + reference/routing.md (+ vpn.md fragments)
4. Stale-era content: concepts/devloop.md, concepts/faster.md,
   reference/tun-device.md (SSH-era comparisons), Ambassador Cloud mention in
   troubleshooting.md frontmatter.
5. Every move/delete needs a redirect. NOTE: redirects.yml turned out to have
   no consumer; the effective redirects are Netlify static/_redirects in the
   telepresence.io repo (unversioned /docs/ URLs only; versioned snapshots
   keep their old trees and need none).

## Tally

| Verdict | Pages |
|---------|-------|
| keep (incl. minor fixes) | 30 |
| rewrite | 5 (quick-start, intercepts concept, engage, docker-compose, engagements/cli) |
| move | 3 (architecture, inside-container, monitoring) |
| merge | 4 (upgrade, docker-run, tun-device, dns) |
| delete | 2 (devloop, faster) |
