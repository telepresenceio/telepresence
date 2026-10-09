#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: prepare-demo.sh --group VERIFIED_GROUP --producer-image IMAGE [options]

  --cluster NAME        kind cluster (default: superheroes)
  --namespace NAME      App and Telepresence manager namespace (default: superheroes)
  --telepresence PATH   Branch-built client (default: ../../build-output/bin/telepresence)
  --version VERSION     Branch image tag (default: 2.33.0-kafka-demo.1)
  --manager-installed   Skip Helm install/upgrade (provider already installed)
  -h, --help            Show this help

The verified group must be the original event-statistics consumer group.
Build the branch tel2 and telepresence-kafka images, and build/load the patched
producer image before running this script. This script changes the active host
Telepresence connection and replaces the statistics and producer Pods.
USAGE
}

die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "Missing $1"; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root_dir=$(cd -- "$script_dir/../.." && pwd)
cluster=superheroes
namespace=superheroes
tp=$root_dir/build-output/bin/telepresence
version=2.33.0-kafka-demo.1
group=
producer_image=
manager_installed=false
while (($#)); do
  case $1 in
    --cluster) (($# >= 2)) || die '--cluster needs a value'; cluster=$2; shift 2 ;;
    --namespace) (($# >= 2)) || die '--namespace needs a value'; namespace=$2; shift 2 ;;
    --telepresence) (($# >= 2)) || die '--telepresence needs a value'; tp=$2; shift 2 ;;
    --version) (($# >= 2)) || die '--version needs a value'; version=$2; shift 2 ;;
    --group) (($# >= 2)) || die '--group needs a value'; group=$2; shift 2 ;;
    --producer-image) (($# >= 2)) || die '--producer-image needs a value'; producer_image=$2; shift 2 ;;
    --manager-installed) manager_installed=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done
[[ $cluster =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && $namespace =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die 'Invalid cluster or namespace'
[[ $group =~ ^[a-zA-Z0-9._-]+$ ]] || die 'Pass the verified original consumer group with --group'
[[ -n $producer_image ]] || die 'Pass the loaded producer image with --producer-image'
version=${version#v}
[[ -x $tp ]] || die "Telepresence client not executable: $tp"
for tool in kind kubectl docker python3; do need "$tool"; done
client_version=$("$tp" version --format=json | python3 -c 'import json,sys; print(json.load(sys.stdin)["client"])') ||
  die 'Could not read the branch Telepresence client version'
[[ ${client_version#v} == "$version" ]] ||
  die "Client version $client_version does not match image tag $version"

state_dir=$script_dir/.demo-state
[[ ! -e $state_dir ]] || die "Existing demo state in $state_dir; run cleanup-demo.sh or inspect it first"
mkdir -p -- "$state_dir"
trap 'if [[ ! -f $state_dir/prepared ]]; then echo "Preparation stopped. State and original Deployment snapshots remain in $state_dir" >&2; fi' EXIT
kind get kubeconfig --name "$cluster" > "$state_dir/kubeconfig"
export KUBECONFIG=$state_dir/kubeconfig
context=kind-$cluster
k() { kubectl --context "$context" --namespace "$namespace" "$@"; }
k get deployment/event-statistics deployment/rest-fights >/dev/null
k get service/fights-kafka service/apicurio >/dev/null
docker image inspect "local/tel2:$version" "local/telepresence-kafka:$version" "$producer_image" >/dev/null ||
  die 'Build all three demo images before preparation'
kind load docker-image --name "$cluster" "local/tel2:$version" "local/telepresence-kafka:$version" "$producer_image"

k get deployment/event-statistics -o json > "$state_dir/event-statistics.json"
k get deployment/rest-fights -o json > "$state_dir/rest-fights.json"
printf '%s\n' "$group" > "$state_dir/group"
printf '%s\n' "$producer_image" > "$state_dir/producer-image"
printf '%s\n' "$context" > "$state_dir/context"
printf '%s\n' "$namespace" > "$state_dir/namespace"

export TELEPRESENCE_REGISTRY=local
if [[ $manager_installed == false ]]; then
  # An existing manager is upgraded; a missing manager is installed.
  if k get statefulset/traffic-manager >/dev/null 2>&1; then
    "$tp" helm upgrade --manager-namespace "$namespace" --set image.registry=local --set kafka.enabled=true --set routeController.enabled=false
  else
    "$tp" helm install --manager-namespace "$namespace" --set image.registry=local --set kafka.enabled=true --set routeController.enabled=false
  fi
fi
"$tp" quit -s || true
"$tp" connect --context "$context" --namespace "$namespace" --manager-namespace "$namespace"

k set image deployment/rest-fights "rest-fights=$producer_image"
k patch deployment/rest-fights --type=strategic -p '{"spec":{"template":{"spec":{"containers":[{"name":"rest-fights","imagePullPolicy":"IfNotPresent"}]}}}}'
k rollout status deployment/rest-fights --timeout=5m
k set env deployment/event-statistics \
  MP_MESSAGING_INCOMING_FIGHTS_TOPIC=fights \
  "MP_MESSAGING_INCOMING_FIGHTS_GROUP_ID=$group" \
  MP_MESSAGING_INCOMING_FIGHTS_ISOLATION_LEVEL=read_committed
k rollout status deployment/event-statistics --timeout=5m

python3 - "$script_dir/kafka-split.yaml" "$state_dir/kafka-split.yaml" "$namespace" "$group" <<'PY'
from pathlib import Path
import json
import sys
source, dest, namespace, group = sys.argv[1:]
template = Path(source).read_text()
assert 'namespace: superheroes' in template
assert 'REPLACE_WITH_VERIFIED_GROUP' in template
Path(dest).write_text(template.replace('namespace: superheroes', f'namespace: {namespace}', 1).replace('REPLACE_WITH_VERIFIED_GROUP', json.dumps(group), 1))
PY
k apply -f "$state_dir/kafka-split.yaml"
if ! k wait ksplit/hero-stats --for=jsonpath='{.status.phase}'=Enabled --timeout=10m; then
  k describe ksplit/hero-stats >&2 || true
  k get pods -l app=event-statistics -o wide >&2 || true
  die "KafkaSplit did not reach Enabled. Keep $state_dir and run cleanup-demo.sh after the provider issue is resolved. Do not start a personal intercept while the split is transitioning."
fi
touch "$state_dir/prepared"
trap - EXIT
printf 'Demo ready. Start: %s intercept hero-stats --workload event-statistics --namespace %s --kafka-only --kafka-header winner-team=heroes -- %s/run-local-stats.sh\n' "$tp" "$namespace" "$script_dir"
