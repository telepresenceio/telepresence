#!/usr/bin/env bash
# Refuses to start the parallel Vagrant regression shards when the host
# lacks the headroom to run three VirtualBox VMs (8 vCPU / 14G RAM each)
# concurrently.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

MIN_MEM_AVAILABLE_GIB=50
MIN_MEM_HEADROOM_GIB=4
VM_MEM_GIB=14
MIN_DISK_FIRST_RUN_GIB=60
MIN_DISK_WARM_RUN_GIB=25
LOADAVG_WARN_THRESHOLD=8

fail() {
    echo "preflight: $*" >&2
    exit 1
}

warn() {
    echo "preflight: warning: $*" >&2
}

require_command() {
    local cmd="$1"
    command -v "$cmd" >/dev/null 2>&1 || fail "'$cmd' not found on PATH"
}

check_tools() {
    require_command vagrant
    require_command VBoxManage
    vagrant --version >/dev/null || fail "'vagrant --version' failed"
    VBoxManage --version >/dev/null || fail "'VBoxManage --version' failed"
}

check_no_kvm_guests() {
    if ! command -v virsh >/dev/null 2>&1; then
        return 0
    fi
    local running
    running="$(virsh --connect qemu:///system list --name 2>/dev/null || true)"
    running="$(printf '%s' "$running" | sed '/^\s*$/d')"
    if [ -n "$running" ]; then
        fail "KVM guests are running (VirtualBox cannot share the CPU" \
            "virtualization extensions with them): $running"
    fi
}

machine_id() {
    cat "$SCRIPT_DIR/.vagrant/machines/shard${1}/virtualbox/id" 2>/dev/null || true
}

# Shard VMs that are already up hold their RAM, so only the ones still to
# be started have to fit in what is available.
running_machine_count() {
    local running n id count=0
    running="$(VBoxManage list runningvms | sed -n 's/.*{\(.*\)}.*/\1/p')"
    for n in 1 2 3; do
        id="$(machine_id "$n")"
        [ -n "$id" ] || continue
        if printf '%s\n' "$running" | grep -qxF "$id"; then
            count=$((count + 1))
        fi
    done
    printf '%s' "$count"
}

check_mem_available() {
    local mem_kib mem_gib running needed
    mem_kib="$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)"
    [ -n "$mem_kib" ] || fail "could not read MemAvailable from /proc/meminfo"
    mem_gib=$((mem_kib / 1024 / 1024))

    running="$(running_machine_count)"
    needed=$((MIN_MEM_AVAILABLE_GIB - running * VM_MEM_GIB))
    if [ "$needed" -lt "$MIN_MEM_HEADROOM_GIB" ]; then
        needed="$MIN_MEM_HEADROOM_GIB"
    fi

    if [ "$mem_gib" -lt "$needed" ]; then
        fail "only ${mem_gib}G RAM available, need ${needed}G with" \
            "${running} of 3 shard VMs already running" \
            "(close other VMs/apps and retry)"
    fi
}

all_machines_exist() {
    local n
    for n in 1 2 3; do
        [ -f "$SCRIPT_DIR/.vagrant/machines/shard${n}/virtualbox/id" ] || return 1
    done
    return 0
}

default_machine_folder() {
    VBoxManage list systemproperties \
        | awk -F': *' '/^Default machine folder:/ {print $2}'
}

check_disk_space() {
    local folder min_gib avail_kib avail_gib
    folder="$(default_machine_folder)"
    [ -n "$folder" ] || fail "could not determine the VirtualBox default machine folder"
    [ -d "$folder" ] || fail "VirtualBox default machine folder does not exist: $folder"

    if all_machines_exist; then
        min_gib="$MIN_DISK_WARM_RUN_GIB"
    else
        min_gib="$MIN_DISK_FIRST_RUN_GIB"
    fi

    avail_kib="$(df -Pk "$folder" | awk 'NR==2 {print $4}')"
    [ -n "$avail_kib" ] || fail "could not determine free space on $folder"
    avail_gib=$((avail_kib / 1024 / 1024))
    if [ "$avail_gib" -lt "$min_gib" ]; then
        fail "only ${avail_gib}G free on the filesystem holding $folder," \
            "need ${min_gib}G"
    fi
}

check_loadavg() {
    local loadavg
    loadavg="$(awk '{print $1}' /proc/loadavg)"
    if awk -v l="$loadavg" -v t="$LOADAVG_WARN_THRESHOLD" \
        'BEGIN {exit !(l > t)}'; then
        warn "1-min load average ($loadavg) is above" \
            "$LOADAVG_WARN_THRESHOLD; the host may be oversubscribed"
    fi
}

check_tools
check_no_kvm_guests
check_mem_available
check_disk_space
check_loadavg

echo "preflight: ok"
