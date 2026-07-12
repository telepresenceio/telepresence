---
title: Attachments
description: "The four ways Telepresence attaches to a workload: replace, intercept, wiretap, and ingest - and how to choose between them."
---

import Animation from '@site/src/components/InterceptAnimation';

# Attachments

An *attachment* couples your workstation to a container in a cluster workload.
While it is active, your locally running code can receive the workload's
traffic, use its environment variables, and read (or write) its volumes — the
combination depends on which of the four modes you choose: **replace**,
**intercept**, **wiretap**, or **ingest**. Each mode has a CLI command of the
same name; `telepresence list` shows what you can attach to, and
`telepresence detach` ends an attachment.

The animations on this page follow requests through a small demo cluster in
which an API service fans out to the Users and Orders services. Without an
attachment, all requests are served inside the cluster:

<Animation className="mode-regular" style={{width: "100%", maxWidth: "600px", aspectRatio: "580 / 405"}} />

All four modes are served by a [traffic-agent](architecture.md#traffic-agent)
that the traffic-manager either injects into the workload's pods as a sidecar
(the default) or runs as a node-hosted pod that attaches to an existing pod
without modifying it. See
[Choose between the sidecar and the node-agent](../howtos/agent-modes.md).

## Replace

Replace removes the targeted container from the workload's pods and reroutes
everything intended for it to your workstation.

* **How it works:**
  - The remote container is removed and all traffic intended for it is
    rerouted to your local workstation.
  - The container's environment is made available to your workstation.
  - Its volumes are mounted locally with read-write access.
  - The container is restored when the attachment ends.
* **Use it when:**
  - The remote container must not keep running — message queue consumers are
    the typical case, where two active consumers would compete for messages.
  - The container receives no incoming traffic at all, so there is nothing to
    intercept.

## Intercept

Intercept reroutes requests destined for a service port to your workstation,
while the remote container keeps running.

<Animation className="mode-global" style={{width: "100%", maxWidth: "600px", aspectRatio: "580 / 405"}} />

Here the Orders service is intercepted without a filter: every request to it
is rerouted to the developer's workstation, and the users notice no
difference.

* **How it works:**
  - Requests to a specific service port (or ports) are rerouted to your local
    workstation. With [filters](#filtering-intercepted-traffic), only matching
    requests are rerouted.
  - The container's environment is made available to your workstation.
  - Its volumes are mounted locally with read-write access.
  - All containers keep running.
* **Use it when:**
  - Your focus is the service API rather than the pod itself.
  - The remote container should continue serving other requests or background
    work while you handle a selected part of the traffic.

## Wiretap

Wiretap sends a *copy* of the traffic to your workstation. The cluster is
undisturbed: the remote service receives and answers everything, and the
responses from your local service are discarded.

* **How it works:**
  - A copy of the requests to a specific service port (or ports) is sent to
    your local workstation. With [filters](#filtering-intercepted-traffic),
    only matching requests are copied.
  - The container's environment is made available to your workstation.
  - Its volumes are mounted locally with read-only access.
  - All traffic still reaches the remote service, which keeps producing the
    responses.
* **Use it when:**
  - Several developers want to receive the same service's traffic
    simultaneously.
  - Breakpoints in your local service must not hold up the cluster.
  - You care about observing requests, not about answering them.
  - You want the smallest possible impact on the cluster.

## Ingest

Ingest involves no traffic at all: it gives your workstation the container's
environment and volumes.

* **How it works:**
  - The container's environment is made available to your workstation.
  - Its volumes are mounted locally with read-only access.
  - No traffic is rerouted; all containers keep running.
* **Use it when:**
  - Your local service needs the remote configuration and data to run, but no
    cluster traffic — for example when you drive it with local tests.
  - You want the smallest possible impact on the cluster.

## At a glance

| | Replace | Intercept | Wiretap | Ingest |
|---|---|---|---|---|
| Traffic to your workstation | All of the container's | Matching requests | A copy of matching requests | None |
| Responses come from | Your local service | Your local service | The remote service | - |
| Remote container | Removed while attached | Keeps running | Keeps running | Keeps running |
| Volume access | Read-write | Read-write | Read-only | Read-only |
| Several developers per workload | No | Yes, with filters | Yes | Yes |

## Filtering intercepted traffic

By default, an intercept takes all traffic on the targeted service port, and a
wiretap copies all of it. Both modes accept HTTP filters that narrow this down
to selected requests:

```console
$ telepresence intercept orders --http-header x-dev=alice
```

Only requests carrying the header `x-dev: alice` reach your workstation; all
other traffic is served by the cluster as usual. This is how a team shares one
environment: each developer intercepts the same service with their own header
value and injects that header in the requests they make (browser extensions
that add request headers make this convenient), without interfering with
teammates or ordinary traffic.

<Animation className="mode-personal" style={{width: "100%", maxWidth: "600px", aspectRatio: "580 / 405"}} />

Above, two developers intercept the Orders service simultaneously: orange
requests carry Developer 2's header value, green ones Developer 1's, and the
blue user requests are served by the cluster as usual.

Filtering can also target specific endpoints of a service:

| Flag                          | Meaning                                                          |
|-------------------------------|------------------------------------------------------------------|
| `--http-path-equal <path>`    | Only requests for this exact path                                |
| `--http-path-prefix <prefix>` | Only requests with a matching path prefix                        |
| `--http-path-regex <regex>`   | Only requests whose path matches the regular expression          |

Multiple `--http-header` flags combine with AND logic; only one `--http-path-`
flag can be used per attachment. Filtering encrypted traffic is possible too;
see [Intercepting TLS/mTLS Applications](../howtos/mtls.md).

## Learn more

- [Code and debug an application locally](../howtos/attach.md) — hands-on
  walkthroughs of all four modes.
- [Configure attachments using CLI](../reference/attachments/cli.md) —
  namespaces, ports, environment import, and other details.
- [Dealing With Conflicting Attachments](../reference/attachments/conflicts.md) —
  what happens when two clients target the same container.
