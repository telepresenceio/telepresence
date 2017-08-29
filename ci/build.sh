#!/bin/bash

set -ex

TRAVIS_OS_NAME=osx # delete this line to run in Travis
SCOUT_DISABLE=1
PROJECT_NAME=datawireio
CLUSTER_NAME=telepresence-testing
CLOUDSDK_COMPUTE_ZONE=us-central1-a

if [[ "$TRAVIS_OS_NAME" == "osx" ]]; then brew cask install osxfuse; brew install python3 sshfs fi
if [[ "$TRAVIS_OS_NAME" == "linux" ]]; then sudo apt install sshfs conntrack; fi

# Record debugging information
python --version
python2 --version
python3 --version
# Make sure torsocks is installed:
./build-torsocks.sh

# Install
make virtualenv
make virtualenv/bin/sshuttle-telepresence

# If on Linux, push images
if [[ "$TRAVIS_OS_NAME" == "linux" ]]; then ./ci/push-images.sh; fi

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
