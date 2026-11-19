# Review every client call of the traffic-manager

This plan fixes GHSA-3gq3-5369-x49v and is meant for a patch release.

## The problem

In `enforcing` mode the traffic-manager promises that a caller sees the
workloads, traffic-agents and agent configuration of a namespace only where
the caller's Kubernetes identity holds a Telepresence grant. Three client
calls do not keep that promise:

| Call | What it does today |
|------|--------------------|
| `WatchAgents`, `WatchAgentsDelta` | Stream every traffic-agent of the session's namespace. A client that does not ask for the compact form also gets the container environment. |
| `UninstallAgents` | Removes the named traffic-agents of the session's namespace, or all of them when no name is given. |

All three only check that the caller owns the session. A session can name
any managed namespace, because the connect grant is not tied to a namespace.
Each operation is expected to review the namespace itself, and these three
never did.

The fix in 2.33.1 worked from a list of calls. This is the third report of
the same kind, so this plan also changes how a call gets its review, so that
a forgotten call is too strict, not too open.

## What makes this less simple than "check at the door"

A caller's access to a namespace is not a yes or a no. The read access that
2.33.1 introduced (`state.ReadAccess`) has two levels:

- **The whole namespace**, when the caller holds an unnamed grant there.
- **Single workloads**, when the caller only holds grants that name
  workloads. These are found one workload at a time, when a workload is
  first looked at.

So the session check cannot reject a caller that lacks the namespace grant.
That caller may still hold a grant on one workload, and nothing tells us
that until the workload is known. A call that shows namespace content must
therefore filter per workload, and that logic has to live in the call.

## The design

### 1. Three session checks, named for what they allow

Today there is one check, `ensureClientSession`, and it only checks
ownership. It is replaced by three:

| Check | What it requires | For |
|-------|------------------|-----|
| `ensureClientSession` | Ownership, and read access to the whole session namespace. Otherwise `PermissionDenied`. | The default. Any call that has no reason to use one of the other two. |
| `clientSessionWithAccess` | Ownership. Returns the session's `ReadAccess` to its namespace. | Calls that show or change namespace content and filter per workload. |
| `ownedClientSession` | Ownership only. | Calls that are open to every connected client by design, and calls that only touch the caller's own session. |

The plain name is the strict one. A new call written by copying the usual
first line of a handler gets the strict check. If that is wrong for the
call, a user with single-workload grants reports that the call is refused,
which is an ordinary bug. Choosing one of the other two is a visible
decision in a code review.

Outside `enforcing` mode a review never fails, so all three behave as the
check does today.

### 2. `WatchAgents` and `WatchAgentsDelta`

Both use `clientSessionWithAccess` and send only the agents that the access
allows, with the same rule as the agent pod watches (`mayReadAgent`). An
agent that the caller may not read is never sent, in the compact form or
the full one. When a review cannot be performed, the stream ends with
`Unavailable`, as the agent pod watches do.

### 3. `UninstallAgents`

- **Named agents:** each name is reviewed with the review that `EnsureAgent`
  uses. The call fails with `PermissionDenied` before anything is removed
  if one name is refused.
- **No names (all agents):** requires access to the whole namespace. A
  caller with single-workload grants gets `PermissionDenied` with a message
  that says to name the workloads.

### 4. Every existing client call gets its check by decision

Each call on the in-cluster listener is listed below with the check it
gets. Calls that only the traffic-agents use are not served on that
listener and are left out.

| Call | Check after this change | Note |
|------|-------------------------|------|
| `WatchAgents`, `WatchAgentsDelta` | With access, filtered | Fixed here |
| `UninstallAgents` | With access | Fixed here |
| `WatchAgentPods`, `WatchAgentPodsDelta`, `WatchAgentPodsInNamespacesDelta` | Owned, plus the review of each namespace in the request, filtered | As in 2.33.1 |
| `WatchWorkloads`, `WatchSessionEvents` | Owned, plus the review of the namespace in the request, filtered | As in 2.33.1 |
| `GetAgentConfig`, `ResolveServicePort` | Owned, plus the review of the namespace in the request | As in 2.33.1 |
| `EnsureAgent`, `PrepareIntercept`, `CreateIntercept` | Owned, plus their own workload review | Unchanged |
| `StreamLogs` | Owned, plus its own namespace review | Unchanged |
| `RemoveIntercept`, `ReleaseAgent` | Owned | Only act on what the caller's own session holds |
| `WatchIntercepts`, `WatchInterceptsDelta` | Owned | Only the caller's own intercepts |
| `Remain`, `Depart`, `ReconnectClient` | Owned | Session upkeep |
| `Tunnel`, `GetQuicTunnelEndpoint`, `GetSessionCredential` | Owned | Network access comes with the connect grant |
| `Lookup`, `LookupDNS` | Owned | Network access comes with the connect grant |
| `WatchClusterInfo` | Owned | Routing information that every client needs |
| `GetKnownWorkloadKinds`, `GetAgentImageFQN`, `GetClientConfig` | Owned | Configuration, no namespace content |
| `WatchNamespaces` | Owned | Open question 3 |
| `SetLogLevel` | Owned | Open question 4 |
| `Version`, `ArriveAsClient` | No session yet | Unchanged |

`ReleaseAgent`, `GetSessionCredential` and the tunnel calls are marked from
a first read. The implementation confirms each one before it is given the
owned check, and anything that does not hold up comes back to this table.

### 5. A test that keeps the table true

A unit test walks the gRPC service description of the traffic-manager and
requires every call to appear in a table in the test, with its class and,
for an open call, the reason. A call that is added later fails the test
until it is classified. The test also reads the handler's source and
checks that the session check it calls matches its class in the table.

## What users will notice

- Nothing outside `enforcing` mode.
- In `enforcing` mode, a caller without a grant in the connected namespace
  no longer sees its traffic-agents and can no longer uninstall them.
- A caller with grants on single workloads sees and can uninstall only
  those, and must name them when uninstalling.
- Older clients work unchanged. They receive fewer agents where they held
  no grant. There is no change to the gRPC API.

## Tests

- **Unit**, next to the tests from 2.33.1
  (`service_readaccess_test.go`): the agent watches without a grant, with a
  namespace grant and with a single-workload grant, in the compact and the
  full form; `UninstallAgents` for named and unnamed requests in the same
  three cases, checking that nothing is removed on a refusal; the three
  session checks; unchanged behavior outside `enforcing` mode.
- **The table test** from section 5.
- **Regression**, in the `auth` area next to `namespace_read.go`: an
  identity without a grant in a second namespace connects to it, and
  `telepresence uninstall --all-agents` is refused while the agent pods
  stay; an identity with a grant on one workload can uninstall that one
  only.
- **The reporter's runbook**, if the reporter sends it, run against a build
  with the fix.

## Documentation

- `docs/reference/authentication.md`: the paragraph on what a caller sees
  now also covers uninstalling, and a new short section lists what every
  connected client may do without a namespace grant: reach the cluster
  network, resolve names, and the outcome of open questions 3 and 4.
- `CHANGELOG.yml`: one security entry under the next patch version, with
  the date left open. It credits the reporter under the name the reporter
  asks for.
- The advisory gets the patched version when the release is out.

## Decisions

Decided in the plan review on 2026-11-19:

1. **The strict default stays**, although no existing call uses it yet. It
   is the part that makes a forgotten call fail closed.
2. **Uninstalling all agents with single-workload grants is refused.**
   "All" always means all, and the message says to name the workloads.
3. **`WatchNamespaces` stays open** and is documented. It shows only the
   names of the managed namespaces, which the client needs for name
   resolution.
4. **`SetLogLevel` stays as it is in this fix** and is documented. Whether
   it should need a grant of its own is decided separately.
5. **The agent watches keep the container environment in the full form**,
   for callers that may read the workload. A current client asks for the
   compact form, which has no environment, and fetches the environment of
   the one workload it attaches to with `EnsureAgent`, which reviews that
   workload. Only clients from before the compact form read the
   environment from the watch. After this fix the watch shows a caller
   nothing that `EnsureAgent` would refuse.

## Order of work

1. The three session checks, with every existing call moved to the check in
   the table, and the table test. No change in behavior yet for the calls
   that were correct.
2. The agent watches.
3. `UninstallAgents`.
4. Regression tests.
5. Documentation and the changelog entry.
6. Remove this plan in the last commit.
