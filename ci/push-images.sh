#!/bin/bash
# Build Docker images:
make build
# Push to Google Docker registry:
export TELEPRESENCE_VERSION=$(make version)
gcloud docker push gcr.io/${PROJECT_NAME}/telepresence-local:${TELEPRESENCE_VERSION}
gcloud docker push gcr.io/${PROJECT_NAME}/telepresence-k8s:${TELEPRESENCE_VERSION}
