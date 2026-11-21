#!/usr/bin/env bash

set -euo pipefail

readonly kafka_port="${TP_KAFKA_CONFORMANCE_PORT:-19092}"
readonly kafka_38="apache/kafka@sha256:c89f315cff967322c5d2021434b32271393cb193aa7ec1d43e97341924e57069"
readonly kafka_current="apache/kafka@sha256:77e3df9054047a88b520d0cc46e16696d3b22022e1d580aeccd2632df6532837"

run_line() (
    local line="$1"
    local image="$2"
    local name="tp-kafka-conformance-${line//./-}-$$"
    local status

    # shellcheck disable=SC2329 # Invoked by the EXIT trap.
    cleanup() {
        docker rm --force "$name" >/dev/null 2>&1 || true
    }
    trap cleanup EXIT

    docker run --detach --name "$name" --publish "127.0.0.1:${kafka_port}:9092" \
        --env CLUSTER_ID=MkU3OEVBNTcwNTJENDM2Qk \
        --env KAFKA_NODE_ID=1 \
        --env KAFKA_PROCESS_ROLES=broker,controller \
        --env KAFKA_LISTENERS=EXTERNAL://:9092,INTERNAL://:29092,CONTROLLER://:9093 \
        --env "KAFKA_ADVERTISED_LISTENERS=EXTERNAL://127.0.0.1:${kafka_port},INTERNAL://localhost:29092" \
        --env KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
        --env KAFKA_INTER_BROKER_LISTENER_NAME=INTERNAL \
        --env KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,EXTERNAL:PLAINTEXT,INTERNAL:PLAINTEXT \
        --env KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 \
        --env KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
        --env KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
        --env KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
        --env KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS=0 \
        "$image" >/dev/null

    for _ in $(seq 1 90); do
        if docker exec "$name" /opt/kafka/bin/kafka-broker-api-versions.sh \
            --bootstrap-server localhost:29092 >/dev/null 2>&1; then
            break
        fi
        sleep 1
    done
    if ! docker exec "$name" /opt/kafka/bin/kafka-broker-api-versions.sh \
        --bootstrap-server localhost:29092 >/dev/null 2>&1; then
        docker logs "$name" || true
        echo "Kafka ${line} did not become ready" >&2
        return 1
    fi

    echo "Running Kafka conformance against ${line} (${image})"
    set +e
    TP_TEST_KAFKA_BROKERS="127.0.0.1:${kafka_port}" \
        go test -v -count=1 -timeout=12m ./pkg/kafkaintercept/...
    status=$?
    set -e
    if [ "$status" -ne 0 ]; then
        docker logs "$name" || true
    fi
    return "$status"
)

run_line "3.8.0" "$kafka_38"
run_line "4.3.1" "$kafka_current"
