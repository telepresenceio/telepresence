#!/bin/bash

set -ex

SCOUT_DISABLE=1
PROJECT_NAME=datawireio

# mac os x only
brew cask install osxfuse
brew install python3 sshfs

# Record debugging information
python --version
python3 --version
# Make sure torsocks is installed:
./ci/build-torsocks.sh

# Install
make virtualenv
make virtualenv/bin/sshuttle-telepresence

mkdir ~/tpbin
cp virtualenv/bin/sshuttle-telepresence ~/tpbin/
export TELEPRESENCE_VERSION=$(make version)
export TELEPRESENCE_REGISTRY=gcr.io/${PROJECT_NAME}
export PATH=$HOME/google-cloud-sdk/bin:$PATH:~/tpbin/

# Get a Kubernetes cluster
kubernaut claim

# Docker not available on OS X Travis:
if [[ "$TRAVIS_OS_NAME" == "linux" ]]; then TELEPRESENCE_METHOD=container ./ci/test.sh; fi
env TELEPRESENCE_METHOD=inject-tcp ./ci/test.sh
env TELEPRESENCE_METHOD=vpn-tcp ./ci/test.sh

# Discard
kubernaut discard

