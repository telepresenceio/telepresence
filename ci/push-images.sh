#!/bin/bash
set -e

export TELEPRESENCE_VERSION
TELEPRESENCE_VERSION=$(make version)

make build-k8s-proxy
docker tag "datawire/telepresence-k8s:${TELEPRESENCE_VERSION}" \
           "gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION}"
$HOME/google-cloud-sdk/bin/gcloud docker -- push "gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION}"
