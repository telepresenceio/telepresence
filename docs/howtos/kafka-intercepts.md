---
title: Intercept Kafka consumers
---

# Intercept Kafka consumers

Kafka personal intercepts let a developer run a consumer locally while the
in-cluster application continues to receive messages that do not match the
developer's filter. Kafka support is an optional provider and is disabled by
default.

## Prerequisites

You need:

- Kubernetes 1.27 or newer with `PodSchedulingReadiness` enabled;
- a Kafka cluster with transactions enabled;
- a consumer configured through explicit topic, group, and isolation-level
  environment variables in its Pod template;
- permission to install the Kafka provider and create `KafkaSplit` resources;
  and
- Kafka credentials with the broker permissions described in the
  [Kafka intercept reference](../reference/kafka-intercepts.md#broker-permissions).

The application must use the original group and topics before the split is
enabled. It must accept `read_committed`, replacement topics, and a replacement
group through the declared environment variables.

## Install the provider

Enable the provider when installing or upgrading the traffic-manager:

```console
$ telepresence helm upgrade --set kafka.enabled=true
```

This installs the `KafkaSplit` and `KafkaRoute` CRDs and the separate
`tp-kafka` controller Deployment. Its separate image and binary remain named
`telepresence-kafka`. Kafka client libraries are not added to the
traffic-manager or traffic-agent image.

## Declare a split

The following example selects every supported workload in `shop` with the
label `app: checkout`. The selected `app` container is expected to contain the
literal values shown for `ORDERS_TOPIC` and `KAFKA_GROUP`, plus an explicit
`KAFKA_ISOLATION_LEVEL` variable.

```yaml
apiVersion: kafka.telepresence.io/v1alpha1
kind: KafkaSplit
metadata:
  name: orders
  namespace: shop
spec:
  desiredState: Enabled
  workloadSelector:
    matchLabels:
      app: checkout
  container: app
  connection:
    bootstrapServers:
      - kafka.shop.svc:9092
  source:
    group: checkout
    topics:
      - orders
    offsetReset: earliest
  application:
    topicEnv: ORDERS_TOPIC
    topicSeparator: ","
    groupEnv: KAFKA_GROUP
    isolationLevelEnv: KAFKA_ISOLATION_LEVEL
    transactionalIDEnv: KAFKA_TRANSACTIONAL_ID
  splitter:
    replicas: 1
    batchSize: 100
  shadows:
    mode: Managed
    managed: {}
```

Apply it and wait for the split to become enabled:

```console
$ kubectl apply -f orders-split.yaml
$ kubectl wait -n shop ksplit/orders \
    --for=jsonpath='{.status.phase}'=Enabled --timeout=5m
```

Enabling a split replaces the selected Pods through the Eviction API. A
PodDisruptionBudget can deliberately delay this operation. The controller does
not change the workload template or desired replica count.

## Start a personal intercept

Use an ordinary intercept when the local process also handles network traffic:

```console
$ telepresence intercept checkout-alice \
    --workload checkout \
    --namespace shop \
    --port 8080:http \
    --kafka-header tenant=blue \
    --env-file ./checkout-alice.env
```

Use `--kafka-only` when the local process only consumes Kafka:

```console
$ telepresence intercept checkout-alice \
    --workload checkout \
    --namespace shop \
    --kafka-only \
    --kafka-header tenant=blue \
    --env-file ./checkout-alice.env
```

Start the local consumer with the generated environment. Its topics and group
identify personal shadow resources and its isolation level is
`read_committed`. If the application declares a transactional-ID binding, the
environment also contains a route-unique transactional ID.

Kafka filters are independent of HTTP filters. Available Kafka predicates are:

```console
--kafka-header tenant=blue
--kafka-key order-123
--kafka-key-prefix order-
```

Repeat `--kafka-header` to form an AND expression. Prefix a value with
`base64:` for binary header values or keys. Use `text:base64:...` when the
literal value itself begins with `base64:`. Multiple developers can attach to
the same split when their predicates are provably disjoint.

An ordinary intercept discovers matching Kafka splits automatically. Add
`--no-kafka` to request network traffic without Kafka routes.
Successful intercept output contains a `Kafka splits` line (and a
`kafka_splits` field in JSON/YAML) whenever routes were attached.

## Stop or disable

Detach normally:

```console
$ telepresence leave checkout-alice
```

The route stops receiving new records, waits for the local consumer group to
become empty, and transactionally returns unconsumed personal records to the
application shadow before managed personal resources are removed.

To return the source group and topics to the normally configured application:

```console
$ kubectl patch -n shop ksplit orders --type=merge \
    -p '{"spec":{"desiredState":"Disabled"}}'
```

Wait for `.status.phase` to become `Disabled` before removing the resource or
reusing its original group elsewhere.

See the [Kafka intercept reference](../reference/kafka-intercepts.md) for
authentication, shadow provisioning, lifecycle status, limitations, and
recovery procedures.
