#!/bin/bash
set -ex

PROJECT_NAME=$1

# Makefile computes version on its own.  If we want the tags to line up, we
# better use the same version.
VERSION=$(make version)

# This script can only push to gcr.io.
REPOSITORY="gcr.io/${PROJECT_NAME}"

GCLOUD="$HOME/google-cloud-sdk/bin/gcloud docker --"

make build-local
make build-k8s-proxy

docker tag \
       "datawire/telepresence-k8s:${VERSION}" \
       "gcr.io/${PROJECT_NAME}/telepresence-k8s:${VERSION}"
${GCLOUD} push "${REPOSITORY}/telepresence-k8s:${VERSION}"

docker tag \
       "datawire/telepresence-local:${VERSION}" \
       "${REPOSITORY}/telepresence-local:${VERSION}"
${GCLOUD} push "${REPOSITORY}/telepresence-local:${VERSION}"
