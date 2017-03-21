#!/bin/bash
# Build Docker images:
make build
# Tag Docker images with GCR tags:
docker tag datawire/telepresence-local:${TELEPRESENCE_VERSION} \
           gcr.io/{PROJECT_NAME}/teleperesence-local:${TELEPRESENCE_VERSION}
docker tag datawire/telepresence-k8s:${TELEPRESENCE_VERSION} \
       gcr.io/{PROJECT_NAME}/teleperesence-k8s:${TELEPRESENCE_VERSION}

# Push to Google Docker registry:
export TELEPRESENCE_VERSION=$(make version)
$HOME/google-cloud-sdk/bin/gcloud docker -- push gcr.io/${PROJECT_NAME}/telepresence-local:${TELEPRESENCE_VERSION}
$HOME/google-cloud-sdk/bin/gcloud docker -- push gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION}
