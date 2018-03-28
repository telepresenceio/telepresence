#!/bin/bash
# Run the release!
# docker run -e HOMEBREW_KEY -e PACKAGECLOUD_TOKEN \
#            -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
#            -v $(pwd)/dist:/root/dist:ro \
#            --rm -it datawire/telepresence-releaser
set -eu

# Inputs
echo "$HOMEBREW_KEY $PACKAGECLOUD_TOKEN" > /dev/null
echo "$AWS_ACCESS_KEY_ID $AWS_SECRET_ACCESS_KEY" > /dev/null
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
RELTEMP=$(mktemp -d)
echo -e "$HOMEBREW_KEY" > "${RELTEMP}/homebrew.rsa"
chmod 600 "${RELTEMP}/homebrew.rsa"
ssh-add "${RELTEMP}/homebrew.rsa"
ssh -oStrictHostKeyChecking=no -T git@github.com || true

# Validate PACKAGECLOUD_TOKEN
package_cloud repository list | fgrep public

# Validate AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY
aws sts get-caller-identity > /dev/null

# Release
# -------

# Linux Packages
dist/upload_linux_packages.sh

# Homebrew
env TELEPRESENCE_VERSION=$VERSION dist/homebrew-package.sh

# Scout blobs
dist/s3_uploader.sh

# Ask user to post on Gitter
cat dist/announcement.md
