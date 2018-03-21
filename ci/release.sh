#!/bin/bash
# Run the release!
set -eu

# Inputs
echo Releasing Telepresence $VERSION
echo "$HOMEBREW_KEY $DOCKER_PASSWORD $PACKAGECLOUD_TOKEN" > /dev/null
if [ ! -x dist/upload_linux_packages.sh ]; then
    echo "Built distribution not found."
    exit 1
fi

# Preparation
# -----------

# ssh stuff to allow a push to github.com/datawire/homebrew-blackbird
mkdir -p ~/.ssh
ssh-keyscan -H github.com > ~/.ssh/known_hosts
echo -e "$HOMEBREW_KEY" > ~/.ssh/homebrew.rsa
chmod 600 ~/.ssh/homebrew.rsa
eval $(ssh-agent)
ssh-add ~/.ssh/homebrew.rsa

# Git wants this stuff set
git config --global user.email "services@datawire.io"
git config --global user.name "d6e automaton"

# Install/test package cloud CLI (uses PACKAGECLOUD_TOKEN)
gem install package_cloud
package_cloud repository list | fgrep public

# Login to Docker Hub
docker login -p "$DOCKER_PASSWORD" -u d6eautomaton
gcloud docker --authorize-only

# Release
# -------

# Docker Images
docker pull gcr.io/datawireio/telepresence-k8s:$VERSION
docker pull gcr.io/datawireio/telepresence-local:$VERSION
docker push datawireio/telepresence-k8s:$VERSION
docker push datawireio/telepresence-local:$VERSION

# Linux Packages
dist/upload_linux_packages.sh

# Homebrew
env TELEPRESENCE_VERSION=$VERSION packaging/homebrew-package.sh

# Scout blobs
dist/s3_uploader.sh

# Ask user to post on Gitter
cat dist/announcement.md
