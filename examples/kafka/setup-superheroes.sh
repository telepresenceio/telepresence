#!/usr/bin/env bash
# Deploy Quarkus Super Heroes to kind with the fixes verified in September 2026.
# Requires Bash, kind, kubectl, git, and a working kind container runtime.
# Uses upstream prebuilt images; no local Java/Maven installation is required.
# This sets up the working sample application, not Telepresence or a KafkaSplit.
set -Eeuo pipefail

usage() {
  cat <<'USAGE'
Usage: bash setup-superheroes.sh [options]

  --cluster NAME    Create/reuse this kind cluster (default: superheroes)
  --namespace NAME  Application namespace (default: superheroes)
  --repo PATH       Existing checkout, or destination for a new clone
                    Default: current directory if it contains the manifest;
                    otherwise ./quarkus-super-heroes
  --no-forward      Deploy and wait, then exit without port-forwarding
  --forward-only    Start port-forwards for an existing deployment; no changes
  -h, --help        Show this help

Optional environment variables:
  UI_PORT=8080 API_PORT=8082 STATS_PORT=8085 WAIT_TIMEOUT=10m
  KIND_EXPERIMENTAL_PROVIDER=docker (or podman) is honored by kind.
  In --forward-only mode, API_PORT must match the deployed UI's API_BASE_URL.

Examples:
  bash setup-superheroes.sh
  bash setup-superheroes.sh --cluster kind --repo ~/src/quarkus-super-heroes
  bash setup-superheroes.sh --cluster kind --forward-only

The script always targets context kind-<NAME>; it does not change your current
kubectl context. Existing checkouts are used as-is: no pull, checkout, or reset.
New clones use upstream main and its prebuilt image tags (which are mutable).
Keep the checkout/images pinned separately for a reproducible video recording.

Port-forwards run in the foreground under this script. Ctrl+C stops only those
processes; the cluster, application, and databases remain running. Stop an old
instance before rerunning. If a forwarded pod is replaced, rerun --forward-only.
USAGE
}

die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
log() { printf '\n==> %s\n' "$*"; }
need_value() { [[ $# -ge 2 && -n $2 && $2 != --* ]] || die "$1 needs a value"; }

cluster=superheroes
namespace=superheroes
repo=
mode=deploy-forward
UI_PORT=${UI_PORT:-8080}
API_PORT=${API_PORT:-8082}
STATS_PORT=${STATS_PORT:-8085}
WAIT_TIMEOUT=${WAIT_TIMEOUT:-10m}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster) need_value "$@"; cluster=$2; shift 2 ;;
    --namespace) need_value "$@"; namespace=$2; shift 2 ;;
    --repo) need_value "$@"; repo=$2; shift 2 ;;
    --no-forward)
      [[ $mode == deploy-forward ]] || die 'Choose only one forwarding mode'
      mode=deploy; shift ;;
    --forward-only)
      [[ $mode == deploy-forward ]] || die 'Choose only one forwarding mode'
      mode=forward; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown option: $1 (see --help)" ;;
  esac
done

for name in "$cluster" "$namespace"; do
  [[ $name =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#name} -le 63 ]] ||
    die "Invalid cluster/namespace name: $name"
done
for port in "$UI_PORT" "$API_PORT" "$STATS_PORT"; do
  if [[ ! $port =~ ^[1-9][0-9]{0,4}$ ]] || ((port > 65535)); then
    die "Invalid port: $port"
  fi
done
[[ $UI_PORT != "$API_PORT" && $UI_PORT != "$STATS_PORT" && $API_PORT != "$STATS_PORT" ]] ||
  die 'UI_PORT, API_PORT, and STATS_PORT must be distinct'

for cmd in kubectl kind; do
  command -v "$cmd" >/dev/null || die "Install $cmd and add it to PATH first"
done

context="kind-$cluster"
k() { kubectl --context "$context" --namespace "$namespace" "$@"; }
forward_pids=()
forward_names=()
forward_logs=()
work_tmp=
cluster_ready=false

cleanup() {
  local pid
  for pid in "${forward_pids[@]:-}"; do
    [[ -n $pid ]] || continue
    kill "$pid" 2>/dev/null || true
  done
  for pid in "${forward_pids[@]:-}"; do
    [[ -n $pid ]] || continue
    wait "$pid" 2>/dev/null || true
  done
  [[ -z $work_tmp ]] || rm -rf -- "$work_tmp"
}
on_error() {
  local rc=$1 line=$2
  trap - ERR
  printf '\nSetup failed at line %s (exit %s).\n' "$line" "$rc" >&2
  if [[ $cluster_ready == true ]]; then
    k get pods,jobs --request-timeout=10s >&2 || true
    k get events --sort-by=.metadata.creationTimestamp --request-timeout=10s | tail -n 15 >&2 || true
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'on_error "$?" "$LINENO"' ERR

work_tmp=$(mktemp -d "${TMPDIR:-/tmp}/superheroes.XXXXXXXX")
clusters=$(kind get clusters)
if ! printf '%s\n' "$clusters" | grep -Fxq -- "$cluster"; then
  [[ $mode != forward ]] || die "kind cluster '$cluster' does not exist"
  log "Creating kind cluster $cluster"
  # Avoid switching the user's current context as a side effect of creation.
  kind create cluster --name "$cluster" --wait 120s --kubeconfig "$work_tmp/kubeconfig"
fi
# Regenerate an isolated kubeconfig on every invocation, including --forward-only.
# Never depend on, modify, or remove the user's persistent kubeconfig.
kind get kubeconfig --name "$cluster" > "$work_tmp/kubeconfig"
export KUBECONFIG="$work_tmp/kubeconfig"
k cluster-info
cluster_ready=true

if [[ $mode != forward ]]; then
  manifest_rel=deploy/k8s/java25-kubernetes.yml
  if [[ -z $repo ]]; then
    if [[ -f $manifest_rel ]]; then repo=$PWD; else repo="$PWD/quarkus-super-heroes"; fi
  fi
  if [[ ! -e $repo ]]; then
    command -v git >/dev/null || die 'Install git and add it to PATH first'
    log "Cloning Quarkus Super Heroes into $repo"
    git clone --depth 1 -- https://github.com/quarkusio/quarkus-super-heroes.git "$repo"
  fi
  [[ -f $repo/$manifest_rel ]] || die "Manifest not found: $repo/$manifest_rel"

  log "Deploying to $context / $namespace"
  k create namespace "$namespace" --dry-run=client -o yaml | k apply -f -
  k apply -f "$repo/$manifest_rel"

  log 'Applying the working demo configuration'
  # No otel-lgtm monitoring stack is installed. Disable its exporters at runtime
  # in all application services to avoid DNS retries and blocked event loops.
  k set env deployment/rest-fights deployment/rest-heroes deployment/rest-villains \
    deployment/rest-narration deployment/grpc-locations deployment/event-statistics \
    deployment/ui-super-heroes QUARKUS_OTEL_SDK_DISABLED=true

  # Bypass the Stork EndpointSlice informer that timed out in this kind setup.
  # HTTP still uses Kubernetes Services; all three Service ports are 80.
  k set env deployment/rest-fights \
    QUARKUS_REST_CLIENT_HERO_CLIENT_URL=http://rest-heroes:80 \
    FIGHT_VILLAIN_CLIENT_BASE_URL=http://rest-villains:80 \
    QUARKUS_REST_CLIENT_NARRATION_CLIENT_URL=http://rest-narration:80

  # This URL is consumed by the browser, not resolved inside the UI pod.
  k set env deployment/ui-super-heroes \
    "API_BASE_URL=http://localhost:$API_PORT" CALCULATE_API_BASE_URL=false

  log 'Waiting for database initialization and deployment rollouts'
  k wait job/rest-fights-liquibase-mongodb-init --for=condition=Complete --timeout="$WAIT_TIMEOUT"
  deployments=$(k get deployments -l system=quarkus-super-heroes -o name)
  [[ -n $deployments ]] || die 'No Super Heroes deployments found'
  while IFS= read -r deployment; do
    k rollout status "$deployment" --timeout="$WAIT_TIMEOUT"
  done <<< "$deployments"
  k get pods
fi

if [[ $mode == deploy ]]; then
  log "Deployment ready. Run again with --cluster $cluster --namespace $namespace --forward-only to open the UIs."
  exit 0
fi

[[ -n $work_tmp ]] || work_tmp=$(mktemp -d "${TMPDIR:-/tmp}/superheroes.XXXXXXXX")
start_forward() {
  local service=$1 port=$2 pid logfile attempt
  logfile="$work_tmp/$service.log"
  kubectl --context "$context" --namespace "$namespace" port-forward \
    --address=127.0.0.1 "svc/$service" "$port:80" >"$logfile" 2>&1 &
  pid=$!
  forward_pids+=("$pid")
  forward_names+=("$service")
  forward_logs+=("$logfile")
  for ((attempt=0; attempt<60; attempt++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      cat "$logfile" >&2
      die "Port-forward for $service failed. Check whether port $port is already in use."
    fi
    if grep -q '^Forwarding from ' "$logfile"; then return 0; fi
    sleep 1
  done
  cat "$logfile" >&2
  die "Timed out starting port-forward for $service"
}

log 'Starting port-forwards'
start_forward ui-super-heroes "$UI_PORT"
start_forward rest-fights "$API_PORT"
start_forward event-statistics "$STATS_PORT"

cat <<EOF

Battle UI:   http://localhost:$UI_PORT
Fight API:   http://localhost:$API_PORT
Statistics:  http://localhost:$STATS_PORT

Open the battle UI and statistics dashboard. Click New Fighters, New Location,
then Fight!; the statistics should update through Kafka.

Leave this terminal running. Ctrl+C stops these port-forwards only.
EOF

while :; do
  for ((i=0; i<${#forward_pids[@]}; i++)); do
    if ! kill -0 "${forward_pids[$i]}" 2>/dev/null; then
      cat "${forward_logs[$i]}" >&2
      die "${forward_names[$i]} forwarding stopped; rerun with --forward-only"
    fi
  done
  sleep 2
done
