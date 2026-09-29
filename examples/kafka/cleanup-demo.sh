#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: cleanup-demo.sh [--telepresence PATH]

Stop the local Quarkus process first. This script leaves the personal intercept,
waits for the Kafka route to drain, disables the split, and restores the two
Deployment templates saved by prepare-demo.sh. It keeps the app and cluster.
USAGE
}
die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root_dir=$(cd -- "$script_dir/../.." && pwd)
state_dir=$script_dir/.demo-state
tp=$root_dir/build-output/bin/telepresence
while (($#)); do
  case $1 in
    --telepresence) (($# >= 2)) || die '--telepresence needs a path'; tp=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done
[[ -f $state_dir/kubeconfig && -f $state_dir/context && -f $state_dir/namespace && -f $state_dir/event-statistics.json && -f $state_dir/rest-fights.json ]] || die "No complete demo state in $state_dir"
[[ -x $tp ]] || die "Telepresence client not executable: $tp"
export KUBECONFIG=$state_dir/kubeconfig
context=$(cat "$state_dir/context")
namespace=$(cat "$state_dir/namespace")
k() { kubectl --context "$context" --namespace "$namespace" "$@"; }

# A KafkaRoute must close before the split can hand consumption back.
"$tp" leave hero-stats || true
if k get ksplit/hero-stats >/dev/null 2>&1; then
  route_closed=false
  for ((attempt=0; attempt<60; attempt++)); do
    k get routes.kafka.telepresence.io -o json > "$state_dir/routes.json"
    if ! python3 - "$state_dir/routes.json" <<'PY'
import json
import sys
routes = json.load(open(sys.argv[1]))['items']
sys.exit(0 if any(route['spec']['splitRef']['name'] == 'hero-stats' for route in routes) else 1)
PY
    then
      route_closed=true
      break
    fi
    sleep 5
  done
  if [[ $route_closed != true ]]; then
    die 'The hero-stats Kafka route did not close within five minutes. Wait and retry cleanup.'
  fi
  k patch ksplit/hero-stats --type=merge -p '{"spec":{"desiredState":"Disabled"}}'
  k wait ksplit/hero-stats --for=jsonpath='{.status.phase}'=Disabled --timeout=10m
  k delete ksplit/hero-stats
fi

for deployment in event-statistics rest-fights; do
  k get "deployment/$deployment" -o json > "$state_dir/$deployment.current.json"
  python3 - "$state_dir/$deployment.json" "$state_dir/$deployment.current.json" "$state_dir/$deployment.restore-patch.json" "$deployment" "$state_dir/group" "$state_dir/producer-image" <<'PY'
import json
import sys
original_path, current_path, patch_path, name, group_path, image_path = sys.argv[1:]
original = json.load(open(original_path))
current = json.load(open(current_path))
old_containers = original['spec']['template']['spec']['containers']
new_containers = current['spec']['template']['spec']['containers']
old = next(c for c in old_containers if c['name'] == name)
index = next(i for i, c in enumerate(new_containers) if c['name'] == name)
new = new_containers[index]
base = f'/spec/template/spec/containers/{index}'
ops = []
if name == 'rest-fights':
    assert new['image'] == open(image_path).read().strip(), 'producer image changed after preparation'
    assert new.get('imagePullPolicy') == 'IfNotPresent', 'producer pull policy changed after preparation'
    ops.extend([{'op': 'replace', 'path': base + '/image', 'value': old['image']},
                {'op': 'replace', 'path': base + '/imagePullPolicy', 'value': old.get('imagePullPolicy', 'Always')}])
else:
    expected = {'MP_MESSAGING_INCOMING_FIGHTS_TOPIC': 'fights',
                'MP_MESSAGING_INCOMING_FIGHTS_GROUP_ID': open(group_path).read().strip(),
                'MP_MESSAGING_INCOMING_FIGHTS_ISOLATION_LEVEL': 'read_committed'}
    old_env = {item['name']: item for item in old.get('env', [])}
    current_env = new.get('env', [])
    for idx in range(len(current_env) - 1, -1, -1):
        item = current_env[idx]
        key = item['name']
        if key not in expected:
            continue
        assert item.get('value') == expected[key], f'{key} changed after preparation'
        op = {'op': 'replace', 'path': base + f'/env/{idx}', 'value': old_env[key]} if key in old_env else {'op': 'remove', 'path': base + f'/env/{idx}'}
        ops.append(op)
with open(patch_path, 'w') as out:
    json.dump(ops, out)
PY
  k patch "deployment/$deployment" --type=json --patch-file "$state_dir/$deployment.restore-patch.json"
  k rollout status "deployment/$deployment" --timeout=5m
done

archive=$script_dir/.demo-state-$(date +%Y%m%d-%H%M%S)
[[ ! -e $archive ]] || die "Previous state archive already exists: $archive"
mv -- "$state_dir" "$archive"
printf 'Demo cleanup finished. Saved state is in %s. Preparation can run again.\n' "$archive"
