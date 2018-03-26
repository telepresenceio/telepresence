#!/bin/bash
# Run the release!
set -eu

# Inputs
echo "$HOMEBREW_KEY $DOCKER_PASSWORD $PACKAGECLOUD_TOKEN" > /dev/null
if [ ! -x dist/upload_linux_packages.sh ]; then
    echo "Built distribution not found."
    exit 1
fi
VERSION=$(cat dist/release_version.txt)
echo Releasing Telepresence $VERSION

# Preparation
# -----------

# ssh stuff to allow a push to github.com/datawire/homebrew-blackbird
eval $(ssh-agent)
echo -e "$HOMEBREW_KEY" > homebrew.rsa
chmod 600 homebrew.rsa
ssh-add homebrew.rsa
ssh -oStrictHostKeyChecking=no -T git@github.com || true

# Install/test package cloud CLI (uses PACKAGECLOUD_TOKEN)
package_cloud repository list | fgrep public

# Login to Docker Hub
docker login -p "$DOCKER_PASSWORD" -u d6eautomaton
gcloud docker --authorize-only

# Release
# -------

# Docker Images
docker pull gcr.io/datawireio/telepresence-k8s:$VERSION
docker pull gcr.io/datawireio/telepresence-local:$VERSION
docker tag gcr.io/datawireio/telepresence-k8s:$VERSION datawire/telepresence-k8s:$VERSION
docker tag gcr.io/datawireio/telepresence-local:$VERSION datawire/telepresence-local:$VERSION
docker push datawire/telepresence-k8s:$VERSION
docker push datawire/telepresence-local:$VERSION

# Linux Packages
dist/upload_linux_packages.sh

# Homebrew
env TELEPRESENCE_VERSION=$VERSION dist/homebrew-package.sh

# Scout blobs
dist/s3_uploader.sh

# Ask user to post on Gitter
cat dist/announcement.md
