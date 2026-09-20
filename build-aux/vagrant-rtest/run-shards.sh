#!/usr/bin/env bash
# Host orchestrator for the parallel Vagrant regression shards: builds
# and saves the images once on the host, brings up the three shard VMs,
# runs one regression shard per VM concurrently, and collects logs.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR" && git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

"$SCRIPT_DIR/preflight.sh"

export TELEPRESENCE_REGISTRY=local
make build save-tel2-image save-client-image save-routecontroller-image

export VAGRANT_CWD="$SCRIPT_DIR"
vagrant up --parallel --provision
vagrant rsync

LOG_DIR="build-output/vagrant-rtest"
mkdir -p "$LOG_DIR"

declare -A pids
for n in 1 2 3; do
    vagrant ssh "shard${n}" \
        -c "/home/vagrant/tp/build-aux/vagrant-rtest/run-shard.sh ${n}" -- -T \
        >"${LOG_DIR}/shard-${n}.log" 2>&1 &
    pids[$n]=$!
done

declare -A statuses
for n in 1 2 3; do
    if wait "${pids[$n]}"; then
        statuses[$n]=0
    else
        statuses[$n]=$?
    fi
done

# The directory may not exist if a shard failed before any test ran, so
# a missing tar is not itself a failure.
for n in 1 2 3; do
    vagrant ssh "shard${n}" \
        -c "tar czf - -C /home/vagrant/tp/build-output rtest/logs" -- -T \
        >"${LOG_DIR}/shard-${n}-rtest-logs.tgz" 2>/dev/null || true
done

echo ""
echo "Regression shard results:"
overall=0
for n in 1 2 3; do
    if [ "${statuses[$n]}" -eq 0 ]; then
        result="PASS"
    else
        result="FAIL"
        overall=1
    fi
    printf '  shard%d: %-4s  log: %s\n' "$n" "$result" "${LOG_DIR}/shard-${n}.log"
done

exit "$overall"
