#!/bin/bash

set -eu

cd "$(dirname "$0")"/..

echo "$HOMEBREW_KEY $PACKAGECLOUD_TOKEN" > /dev/null
echo "$AWS_ACCESS_KEY_ID $AWS_SECRET_ACCESS_KEY" > /dev/null
if [ ! -x dist/upload_linux_packages.sh ]; then
    echo "Built distribution not found."
    exit 1
fi

docker build \
    --file ci/Dockerfile.releaser \
    -t datawire/telepresence-releaser \
    ci

docker run \
    -e HOMEBREW_KEY -e PACKAGECLOUD_TOKEN \
    -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
    -v $(pwd)/dist:/root/dist:ro \
    --rm -it datawire/telepresence-releaser \
    dist/release-in-docker.sh
