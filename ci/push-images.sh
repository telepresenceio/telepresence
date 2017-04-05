#!/bin/bash
set -e

export TELEPRESENCE_VERSION
TELEPRESENCE_VERSION=$(make version)

# Build local image, tag, then push to GCR:
make build-local && \
    docker tag datawire/telepresence-local:${TELEPRESENCE_VERSION} \
           gcr.io/${PROJECT_NAME}/telepresence-local:${TELEPRESENCE_VERSION} && \
    $HOME/google-cloud-sdk/bin/gcloud docker -- push gcr.io/${PROJECT_NAME}/telepresence-local:${TELEPRESENCE_VERSION} &


# Tag Docker images with GCR tags:
make build-remote && \
    docker tag datawire/telepresence-k8s:${TELEPRESENCE_VERSION} \
           gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION} && \
    $HOME/google-cloud-sdk/bin/gcloud docker -- push gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION} &

# Wait for jobs to finish
wait
