# Product findings surfaced by the regression suite

Issues in the product (not the tests) that the new suite exposed while being
brought up on the kind dev cluster, 2026-07-30. Each was verified manually
before being recorded. None are fixed by this initiative except (1).

1. **Chart: matchLabels-only namespaceSelector breaks subsequent installs**
   (FIXED in this branch). A manager installed with a namespaceSelector that
   has only `matchLabels` made every later `telepresence helm install` of
   another manager in the cluster fail: the overlap-validation template in
   `agentInjectorWebhook.yaml` called sprig `append` on the nil
   `matchExpressions`. One-line fix (`| default list`) included in the wave-1
   commit. Old integration tests never hit it because they always used
   `namespaces` lists, which the chart converts to matchExpressions.

2. **Client-side intercept timeout leaks the manager-side intercept.** When
   `telepresence intercept` times out waiting for activation
   (`timeouts.intercept`), the intercept it created stays ACTIVE on the
   manager (observed held for >10 minutes), blocking every later intercept
   on that workload with "conflict with intercept ...". The CLI should
   remove the intercept it created when it gives up. Reproduced via a
   wounded-session activation stall; manager log evidence in the session
   notes (CreateIntercept-2024 ACTIVE server-side in 7ms while the client
   timed out, RemoveIntercept only arriving minutes later from cleanup).

3. **Agent config does not converge on pod-template annotation changes.**
   Adding `telepresence.io/inject-container-ports` (and likely any
   config-affecting annotation) to an already-agented workload never takes
   effect: the manager preserves the existing sidecar config when it
   regenerates after a template change. A fresh workload with the identical
   manifest works immediately. Verified live both ways. The framework works
   around it by recreating a workload whenever its manifest changes
   (rt/fixture_workload.go), but users editing annotations on live
   workloads hit this silently.

4. **Dynamic namespaceSelector does not pick up new namespaces without a
   manager restart.** A newly created (or newly labeled) namespace matching
   the manager's label selector is not injected into or listed until the
   traffic-manager pod restarts ("Refreshed namespaceSelector" only fires
   at startup; verified: a labeled namespace was invisible for 96s, then
   appeared in mappedNamespaces immediately after an unrelated restart).
   The old suite masked this by reinstalling/upgrading the manager around
   every namespace change. The framework encodes current behavior via
   rt.RestartManager, with pointers back to this finding.

Also worth a look (not product bugs, but sharp edges the suite documents in
code): `quit -s` ignores `--use` and stops all daemons; bare `list`/`detach`
with no daemon implicitly connects to namespace "default"; `genyaml
volume`'s `--agent`/`--input` flags are accepted but unused.
