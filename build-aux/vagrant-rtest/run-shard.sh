#!/usr/bin/env bash
# In-VM entry point for one regression-test shard. Starts (or reuses) the
# VM's own minikube cluster, loads the images built and saved on the
# host, and runs that shard of "make check-regression". Runs as the
# vagrant user.
set -euo pipefail

usage() {
    echo "usage: $(basename "$0") <1|2|3>" >&2
    exit 1
}

[ $# -eq 1 ] || usage
case "$1" in
    1 | 2 | 3) ;;
    *) usage ;;
esac
SHARD="$1"

# shellcheck source=/dev/null
source /etc/profile.d/golang.sh

cd /home/vagrant/tp

export TELEPRESENCE_VERSION
TELEPRESENCE_VERSION="$(cat build-output/version.txt)"
export TELEPRESENCE_REGISTRY=local
export RTEST_REGISTRY=local
export RTEST_CONTEXT=minikube

minikube status >/dev/null 2>&1 || minikube start \
    --driver=docker \
    --container-runtime=containerd \
    --kubernetes-version=v1.33.5 \
    --cpus=6 \
    --memory=10g

# "telepresence connect --docker" runs the client as a container on the
# local docker daemon, so the images must be present there as well as in
# the cluster.
for image in tel2-image client-image routecontroller-image; do
    docker load -i "build-output/${image}.tar"
    minikube image load "build-output/${image}.tar"
done

exec make check-regression "SHARD=${SHARD}"
