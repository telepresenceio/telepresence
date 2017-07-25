#!/bin/bash
# Build the proxy in the Minikube docker

set -ex

VERSION=$(git describe --tags)

eval $(minishift docker-env --shell bash)
cd k8s-proxy
docker build . -q -t "datawire/telepresence-k8s:${VERSION}"
