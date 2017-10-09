#!/bin/bash
# Build the proxy in the Minishift docker

set -ex

VERSION=$(git describe --tags)

minishift ip
eval $(minishift docker-env --shell bash)
cd k8s-proxy
docker build . -q -t "datawire/telepresence-k8s:${VERSION}"
