---
title: How Kafka personal intercepts work
description: "The parts of the Kafka provider and how a record travels from the source topic to the application or to a developer's local consumer."
---

# How Kafka personal intercepts work

A Kafka personal intercept lets a developer consume, on their workstation,
only the records that match a filter, while the application in the cluster
keeps consuming everything else. This page explains the parts that make that
possible and how they fit together. The
[how-to guide](../howtos/kafka-intercepts.md) shows how to set one up, and the
[reference](../reference/kafka-intercepts.md) describes every resource,
setting, and limit.

## The big picture

```mermaid
flowchart LR
  op["Operator"] -- "declares a KafkaSplit<br/>per consumer workload" --> prov
  dev["Developer"] -- "telepresence intercept<br/>creates a KafkaRoute" --> prov
  prov["Kafka provider"] -- "runs the splitter and<br/>owns the shadow topics" --> kafka["Kafka cluster"]
  prov -- "moves the application<br/>onto its shadow topics" --> app["Application"]
  kafka -. "application shadow" .-> app
  kafka -. "personal shadow" .-> local["Developer's local consumer"]
```

An operator declares, once per consumer workload, which consumer group and
topics may be split. Each developer's intercept adds a route with a filter.
The provider runs a splitter that reads the source topics on behalf of the
original consumer group and copies every record to exactly one place: the
personal shadow of the route whose filter matches, or the application shadow
when none does. The application consumes its shadow, and each developer's
local consumer reads their personal shadow.

## Resources and controllers

```mermaid
flowchart LR
  op["Operator"] -- "kubectl apply" --> split
  tm["traffic-manager"] -- "one per intercept" --> route

  subgraph appns["Application namespace"]
    direction TB
    app["Application Pods"]
    split["KafkaSplit<br/>source group and topics,<br/>workload selector, env bindings"]
    route["KafkaRoute<br/>predicate, expiry, status"]
  end

  subgraph provns["Provider namespace"]
    direction TB
    prov["Kafka provider<br/>split and route controllers,<br/>validating and Pod-mutating webhooks"]
    cm["Routing ConfigMap"]
    sts["Splitter StatefulSet"]
    leases["Member Leases"]
  end

  split --> prov
  route --> prov
  prov -- "publishes routes" --> cm
  cm -- "watched" --> sts
  prov -- "creates" --> sts
  sts -- "acknowledges" --> leases
  leases --> prov
  prov -. "evicts and re-admits" .-> app
```

**KafkaSplit.** One split per consumer workload. It names the source consumer
group and topics, selects the workload, and maps the Kafka settings to the
environment variables the container reads them from. The split is the unit
of ownership: while it is enabled, the splitter is the only consumer of the
source group.

**KafkaRoute.** One route per intercept, created and expired by the
traffic-manager, so users rarely handle routes directly. A route carries a
predicate, such as a header value or a key prefix. The provider rejects a
route whose predicate could match the same record as an existing one.

**Kafka provider.** The `tp-kafka` Deployment reconciles splits and routes,
validates them on admission, and mutates application Pods. It creates the
shadow topics and groups on the broker, runs the splitter, and reports
status on both resources. It watches Pods and workloads only in namespaces
that hold a split.

**Splitter.** A StatefulSet whose members join the source group with static
membership. The provider publishes the active routes as a numbered generation
in a ConfigMap that every member watches, and each member records the
generation it runs in a Lease that the provider reads back.

## Where a record goes

```mermaid
flowchart LR
  src[("Source topic T<br/>read as group G")] --> sts["Splitter<br/>one transaction per batch"]
  sts -- "matches alice's filter" --> pa[("Personal shadow<br/>T.tp.G.alice")]
  sts -- "matches bob's filter" --> pb[("Personal shadow<br/>T.tp.G.bob")]
  sts -- "no filter matches" --> ap[("Application shadow<br/>T.tp.G.app")]
  pa --> la["alice's local consumer<br/>group G.tp.alice"]
  pb --> lb["bob's local consumer<br/>group G.tp.bob"]
  ap --> app["Application Pods<br/>group G.tp-app"]
```

The splitter consumes the source topics inside Kafka transactions. For each
batch it publishes every record to exactly one shadow and commits the source
offsets in the same transaction, so an aborted batch neither advances the
source group nor exposes a record to `read_committed` consumers. Records keep
their partition number, key, value, headers, and timestamp. Every shadow
mirrors the source partition count.

Filters of concurrent developers must be disjoint: no record may match two
routes. Without that rule the splitter could not publish a record to exactly
one destination.

## Enabling a split

```mermaid
sequenceDiagram
  participant O as Operator
  participant P as Kafka provider
  participant K as Kafka cluster
  participant A as Application Pod
  participant S as Splitter
  O->>P: KafkaSplit with desiredState Enabled
  P->>K: create the application shadow topic and group
  P->>A: evict each Pod of the selected workloads
  A->>P: replacement Pod hits the mutating webhook
  P->>A: env rewritten: shadow topic, group G.tp-app, read_committed
  A->>K: consume the application shadow
  P->>K: verify the source group G has no members
  P->>S: start the splitter members
  S->>K: join group G and consume the source topics
```

The provider never edits the workload template. It evicts the application's
Pods, and its mutating webhook rewrites each replacement Pod's environment to
point at the application shadow. Only once the source group is provably empty
does the splitter take it over. Disabling a split runs the same steps in
reverse: routes close, the splitter drains and leaves the group, admission
returns to normal, and the Pods are replaced once more. Because a Pod admitted
without the rewrite would consume the source group next to the splitter, the
webhook refuses Pod creation in a labelled namespace while the provider is
unavailable.

## Adding a route

```mermaid
sequenceDiagram
  participant D as Developer
  participant M as traffic-manager
  participant P as Kafka provider
  participant C as Routing ConfigMap
  participant S as Splitter member
  participant L as Member Lease
  D->>M: telepresence intercept --kafka-header x-dev=alice
  M->>P: create KafkaRoute alice
  P->>P: create the personal shadow topic and group
  P->>C: publish generation N with route alice
  C-->>S: watch delivers generation N
  S->>S: adopt N at the next transaction boundary
  S->>L: acknowledged generation N
  L-->>P: every member acknowledges N
  P->>M: route alice is Ready
  M->>D: environment with the personal topic and group
```

`telepresence intercept` attaches every enabled split that selects the
workload. The environment it provides to the local process overrides the
topic, group, and isolation-level variables with the route's personal values,
so a consumer started with that environment reads its personal shadow without
any code change. A route reports ready only once every splitter member
acknowledges the generation that contains it, so a developer never sees a
route that only part of the splitter honors.

Leaving the intercept closes the route: the splitter stops routing to it, the
records the local consumer had not yet read are drained back into the
application shadow, and the managed personal resources are removed.
