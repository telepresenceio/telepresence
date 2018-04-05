#!/bin/bash
# This will be run inside a Docker image for each operating system, which is
# presumed to have fpm pre-installed.
#
# Inputs:
# $PACKAGE_VERSION is the package version to use.
# $PACKAGE_TYPE is rpm or deb.
# Command line arguments are the dependencies.
set -e

# Set proper ownership before exiting, so the created packages aren't owned by
# root.
trap 'chown -R --reference /build-inside/build-package.sh /out/' EXIT

# XXX: Ubuntu needs software installed while Fedora is fine as-is
command -v python3 >/dev/null 2>&1 || \
    (echo "Installing required packages" && \
     apt-get -qq update && \
     apt-get -qq install python3-venv git > /dev/null)

# Install in /usr/share/telepresence and /usr/bin
PREFIX=/usr /source/install.sh

echo "Building package using FPM"
cd /out
fpm -t "$PACKAGE_TYPE" \
    --name telepresence \
    --version "$PACKAGE_VERSION" \
    --description "Local development for a remote Kubernetes cluster." \
    ${@/#/--depends } \
    --input-type dir \
    /usr/share/telepresence \
    /usr/bin/sshuttle-telepresence \
    /usr/bin/telepresence
