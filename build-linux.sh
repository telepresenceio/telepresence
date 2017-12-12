#!/bin/bash

# Build script for Linux automated builds

set -eEx

export SCOUT_DISABLE=1
export PROJECT_NAME=datawireio
export CLUSTER_NAME=telepresence-testing
export CLOUDSDK_COMPUTE_ZONE=us-central1-a
export TELEPRESENCE_VER_SUFFIX=$(date +-LNX-%s)
export TELEPRESENCE_VERSION=$(make version)
export TELEPRESENCE_REGISTRY=ark3
export PATH=$PATH:$PWD/virtualenv/bin:~/datawire/kubernaut/virtualenv/bin

ci/setup-gcloud.sh

cleanup() {
  printf "Performing cleanup...\n"
  #kubernaut discard
}

trap cleanup ERR

# record debugging information
python --version
python3 --version

# install
rm -rf virtualenv
make setup

# Build and push images
make build-local build-k8s-proxy
docker tag "datawire/telepresence-local:${TELEPRESENCE_VERSION}" \
           "${TELEPRESENCE_REGISTRY}/telepresence-local:${TELEPRESENCE_VERSION}"
docker push "${TELEPRESENCE_REGISTRY}/telepresence-local:${TELEPRESENCE_VERSION}"
docker tag "datawire/telepresence-k8s:${TELEPRESENCE_VERSION}" \
           "${TELEPRESENCE_REGISTRY}/telepresence-k8s:${TELEPRESENCE_VERSION}"
docker push "${TELEPRESENCE_REGISTRY}/telepresence-k8s:${TELEPRESENCE_VERSION}"

# Get a Kubernetes cluster
#kubernaut claim
#export KUBECONFIG=${HOME}/.kube/kubernaut

# Run tests
#export TELEPRESENCE_TESTS="-xs"
export TELEPRESENCE_TESTS="-xs -k fromcluster"
#env TELEPRESENCE_METHOD=container ./ci/test.sh
env TELEPRESENCE_METHOD=inject-tcp ./ci/test.sh
env TELEPRESENCE_METHOD=vpn-tcp ./ci/test.sh

# Cleanup
#kubernaut discard
