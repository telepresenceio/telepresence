# Authorize namespace reads in enforcing mode

Fixes the private advisory GHSA-xq23-3fhh-qcrx. Target release: 2.33.1.

This plan and its branch are private until the fixed release is out. The
branch `thallgren/namespace-authorization` must not be pushed to the public
repository.

## Problem

In `enforcing` mode, a client may only act in namespaces where its
Kubernetes identity holds a Telepresence grant. That holds for creating
intercepts and for ensuring agents. It does not hold for reads.

A client that holds only the `connect` grant can read data about any
namespace the traffic-manager manages:

| RPC | What leaks | Check today |
|-----|------------|-------------|
| `GetAgentConfig` | Generated agent config for a workload | Namespace is managed |
| `ResolveServicePort` | ClusterIP and port of a service | Namespace is managed |
| `WatchAgentPods`, `WatchAgentPodsDelta`, `WatchAgentPodsInNamespacesDelta` | Agent pod names, IPs, UIDs, intercepted state | Namespace is managed |
| `WatchSessionEvents` (agent-pod part) | Same as above | Namespace is managed |
| `WatchWorkloads` | Workload list | Reviewed, but only for a namespace named in the request |

The last row is the gap the report missed. The namespace a client connects
to is never reviewed. `ArriveAsClient` reviews `connect` in the manager's
namespace only, and every handler treats the connected namespace as
trusted. A client can connect with `--namespace payments` and read that
namespace with an unmodified client.

The other modes are out of scope. `disabled` and `permissive` never reject
a call, by design.

## What "may read a namespace" means

Two existing reviews define access today:

- **Namespace-wide.** `auth.NamespaceReview`: an unnamed `create` on
  `attachments.telepresence.io`, or the legacy `pods/portforward` grant.
- **Per workload.** `auth.EnsureAgentReview`: `create` or `get` on the
  attachment named after the workload. A Role scoped with `resourceNames`
  passes this one and never passes the unnamed review.

The fix must respect both. A user who is granted two workloads in a
namespace must keep seeing those two, and nothing else.

This plan introduces one concept, the session's **read access** to a
namespace, with three outcomes:

| Outcome | When | Effect |
|---------|------|--------|
| All | The namespace-wide read review passes | Everything in the namespace is visible |
| Filtered | The namespace-wide review is denied | A workload is visible only if the per-workload review passes |
| Unknown | A review could not be performed (`Unavailable`) | Nothing is shown, nothing is cached, the next event asks again |

The namespace-wide read review is a new `auth.NamespaceReadReview`: an
unnamed `create` **or** `get` on `attachments`. `get` is included so that a
user who may only ingest can still list workloads. See decision 1.

Both levels are cached per session, as `AuthorizedNamespace` does today.
The namespace level is one review per session and namespace. The workload
level is one review per session and workload, and only runs for sessions
that failed the namespace level.

## Changes

### 1. Read access on the client session

In `cmd/traffic/cmd/manager/state`:

- Extend the per-session cache next to `AuthorizedNamespace` with a
  per-workload cache, keyed by namespace and workload name.
- Add `ClientSession.ReadAccess(ns, namespaceProbe, workloadProbe)`. It
  returns a value that answers `Allows(workloadName string) bool`.

In `cmd/traffic/cmd/manager/service.go`:

- Add `service.readAccess(ctx, clientInfo, namespace)`, which wires the two
  probes to `authorized()`. Outside `enforcing` mode `authorized()` never
  returns an error, so the outcome is always All and behavior is unchanged.

The connected namespace gets no special treatment. It goes through
`readAccess` like any other.

### 2. `GetAgentConfig`

Call `authorizeEnsureAgent(ctx, namespace, request.Name)` before generating
the config. The request names a workload, so the per-workload review is the
right one. A denied caller gets `PermissionDenied`.

### 3. Agent-pod watches

Applies to `WatchAgentPods`, `WatchAgentPodsDelta`,
`WatchAgentPodsInNamespacesDelta` and the agent-pod part of
`WatchSessionEvents`.

**Filter, do not reject.** Each upsert is passed on only if the session's
read access for the pod's namespace allows the pod's `workload_name`.
Removals pass through unchanged; a pod name alone that the client never saw
is harmless.

Rejecting the stream would be wrong for two reasons:

- `WatchSessionEvents` carries intercept updates on the same stream. A
  denied namespace must not cut those off.
- The client retries a failed watch. A permanent `PermissionDenied` would
  turn into a retry loop in every released client.

The filter sits where the handlers build the delta, so all four share one
function.

### 4. `WatchWorkloads`

- Run the review for the connected namespace too, not only for a namespace
  named in the request.
- Replace the reject-on-denial with the same filter as the agent-pod
  watches. A user with grants on specific workloads then sees those in
  `telepresence list`, where today a named namespace is refused outright.
  See decision 2.

### 5. `ResolveServicePort`

Require namespace-wide read access. A denied caller gets
`PermissionDenied`.

The request names a service, not a workload, so there is no per-workload
review to fall back on. See decision 3.

### 6. Documentation and changelog

- `docs/reference/authentication.md`: add reads to "What is protected", and
  describe the two levels in the authorization section.
- `docs/reference/rbac.md`: state what a `resourceNames`-scoped user sees.
- `CHANGELOG.yml`: one `security` entry in 2.33.1 that links the advisory.
  Then `make docs-files`.

The changelog entry is written on the private branch and becomes public
only with the release.

## Not in this change

- **Network reach.** A connected client can open connections to any pod or
  service the traffic-manager can reach. That is what the `connect` grant
  gives, and NetworkPolicy is the control for it.
- **DNS.** A connected client can resolve service names in any namespace.
  Restricting that would break normal use of a connected workstation.
- **Revocation.** A cached verdict lasts for the session. Removing a grant
  takes effect when the client reconnects. This is how
  `AuthorizedNamespace` already behaves.
- **Making `enforcing` the default.** Planned for 2.34.0, as a separate
  change. This fix must be released first.

## Decisions for review

1. **Does `get` on attachments grant read access to a namespace?**
   Recommended: yes. Today `WatchWorkloads` requires `create`, so a user
   who may only ingest cannot list a namespace by name. Counting `get`
   fixes that and loosens nothing else, since `get` already allows
   ensuring an agent.

2. **Should `WatchWorkloads` filter instead of reject?**
   Recommended: yes, for one rule across all reads. The cost is a behavior
   change: a fully denied caller gets an empty list instead of an error.
   The alternative is to keep the error when no workload at all passes,
   which needs the complete workload list before the first answer.

3. **How strict is `ResolveServicePort`?**
   Recommended: namespace-wide access only. A user with grants on specific
   workloads then cannot resolve a named service port through the manager
   and must give a numeric port. The leak it closes is small, since the
   ClusterIP is available through DNS anyway. The alternative is to leave
   this RPC as it is and say so in the advisory.

4. **CVE.** Recommended: request one from GitHub when the draft is ready.
   The reporter will expect it, and the two earlier advisories have one.

## Tests

Unit tests, in `cmd/traffic/cmd/manager`, next to `service_grant_test.go`
and `service_namespaces_test.go`:

- Each affected RPC, in `enforcing` mode, for a caller with no grant, with
  a namespace-wide grant, and with a grant on one workload.
- The connected namespace is reviewed like a named one.
- `WatchSessionEvents` keeps delivering intercept updates when the
  agent-pod part is filtered to nothing.
- A review that returns `Unavailable` is not cached and is asked again.
- `permissive` and `disabled` modes behave as before.

Regression test, in `regression_test/suites/auth/enforcing.go`:

- A developer identity with a grant in one namespace connects to another
  and gets an empty `telepresence list`.
- A developer identity scoped to one workload sees and can intercept that
  workload, and does not see its neighbors.

## Rollout

GitHub Actions does not run in an advisory's temporary private fork, so the
checks run locally:

1. Implement on the local branch, in this worktree.
2. `make lint-go`, `make lint-docs`, the manager unit tests, and the `auth`
   regression area on `kind-dev`.
3. Create the advisory's temporary private fork, push the branch there and
   open the pull request in the fork for review.
4. At release time, merge the fork's pull request from the advisory page,
   cut 2.33.1 through the normal release procedure, and publish the
   advisory with patched version 2.33.1.
5. Remove this plan in the last commit before the merge.

## Compatibility

- Older clients work unchanged. They receive fewer agent pods and
  workloads in `enforcing` mode when they lack a grant, and never a new
  error on a watch stream.
- `GetAgentConfig` and `ResolveServicePort` can now return
  `PermissionDenied` in `enforcing` mode. Released clients already surface
  manager errors from both calls to the user.
- No change to `rpc/manager`. No new chart values.
