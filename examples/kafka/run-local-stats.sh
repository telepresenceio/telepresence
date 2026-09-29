#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'USAGE'
Usage: run-local-stats.sh [--repo PATH]

Run event-statistics in Quarkus dev mode for a Kafka-only intercept. Start this
script after `telepresence intercept ... --` so it inherits the split's topic,
group, isolation level, Kafka bootstrap, and Apicurio settings.
USAGE
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=$script_dir/quarkus-super-heroes
while (($#)); do
  case $1 in
    --repo) (($# >= 2)) || { echo '--repo needs a path' >&2; exit 2; }; repo=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done
[[ -f $repo/event-statistics/pom.xml ]] || { echo "No event-statistics module in $repo" >&2; exit 1; }
[[ -n ${MP_MESSAGING_INCOMING_FIGHTS_TOPIC:-} && -n ${MP_MESSAGING_INCOMING_FIGHTS_GROUP_ID:-} && -n ${MP_MESSAGING_INCOMING_FIGHTS_ISOLATION_LEVEL:-} ]] || {
  echo 'Missing intercept Kafka settings. Run through telepresence intercept -- this-script.' >&2
  exit 1
}
[[ $MP_MESSAGING_INCOMING_FIGHTS_TOPIC != fights &&
   $MP_MESSAGING_INCOMING_FIGHTS_GROUP_ID != event-statistics &&
   $MP_MESSAGING_INCOMING_FIGHTS_ISOLATION_LEVEL == read_committed ]] || {
  echo 'Expected a personal shadow topic/group and read_committed isolation from the intercept.' >&2
  exit 1
}

# These are local runtime settings. The three MP_MESSAGING values above belong
# to the intercept and must stay unchanged.
export QUARKUS_HTTP_PORT=9085
export QUARKUS_DEVSERVICES_ENABLED=false
export QUARKUS_ANALYTICS_DISABLED=true
export QUARKUS_OTEL_SDK_DISABLED=true
export QUARKUS_OTEL_METRICS_ENABLED=false
export QUARKUS_OTEL_LOGS_ENABLED=false
# This sample's ConfigMap uses dotted envFrom keys. Telepresence passes the
# split's explicit variables, so provide regular env names for local Quarkus.
export KAFKA_BOOTSTRAP_SERVERS=${KAFKA_BOOTSTRAP_SERVERS:-fights-kafka:9092}
export MP_MESSAGING_CONNECTOR_SMALLRYE_KAFKA_APICURIO_REGISTRY_URL=${MP_MESSAGING_CONNECTOR_SMALLRYE_KAFKA_APICURIO_REGISTRY_URL:-http://apicurio:8080/apis/registry/v2}
export MP_MESSAGING_CONNECTOR_SMALLRYE_KAFKA_SCHEMA_REGISTRY_URL=${MP_MESSAGING_CONNECTOR_SMALLRYE_KAFKA_SCHEMA_REGISTRY_URL:-http://apicurio:8080/apis/ccompat/v7}
cd -- "$repo/event-statistics"
exec ./mvnw quarkus:dev -Dquarkus.devservices.enabled=false -Dquarkus.analytics.disabled=true
