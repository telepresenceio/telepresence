#!/bin/bash
set -ex

export TELEPRESENCE_VERSION
TELEPRESENCE_VERSION=$(make version)

make build-local
make build-k8s-proxy

docker tag "datawire/telepresence-k8s:${TELEPRESENCE_VERSION}" \
           "gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION}"
$HOME/google-cloud-sdk/bin/gcloud docker -- push "gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION}"

docker tag "datawire/telepresence-local:${TELEPRESENCE_VERSION}" \
       "gcr.io/${PROJECT_NAME}/telepresence-local:${TELEPRESENCE_VERSION}"
$HOME/google-cloud-sdk/bin/gcloud docker -- push "gcr.io/${PROJECT_NAME}/telepresence-local:${TELEPRESENCE_VERSION}"
