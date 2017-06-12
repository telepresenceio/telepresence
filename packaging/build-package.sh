#!/bin/bash
# This will be run inside a Docker image for each operating system, which is
# presumed to have fpm pre-installed.
#
# Inputs:
# $PACKAGE_VERSION is the package version to use.
# $PACKAGE_TYPE is rpm, deb or apk.
# Command line arguments are the dependencies.
set -e

# Set proper ownership before exiting, so the created packages aren't owned by
# root. (--reference not supported on Alpine)
if [ ! -f /etc/alpine-release ]; then
    trap 'chown -R --reference /build-inside/build-package.sh /out/' EXIT
fi

# Package only includes /usr/bin/telepresence:
mkdir /tmp/build
cp /source/cli/telepresence /tmp/build
cp /source/virtualenv/bin/sshuttle-telepresence /tmp/build
cd /out
fpm -t "$PACKAGE_TYPE" \
    --name telepresence \
    --version "$PACKAGE_VERSION" \
    --description "Local development for a remote Kubernetes cluster." \
    ${@/#/--depends } \
    --prefix /usr/bin \
    --chdir /tmp/build \
    --input-type dir \
    telepresence sshuttle-telepresence
